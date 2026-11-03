package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

const (
	sha600 = "1af3b93b6815bc44a9784bd300feb67ff0d1eeb3"
	sha701 = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	head0  = "0000000000000000000000000000000000000001"
)

const workflow = `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: actions/checkout@v6.0.0
      - uses: actions/checkout@` + sha600 + ` # v6.0.0
      - uses: ./local
`

const gomodSrc = "module example.com/x\n\ngo 1.24\n\nrequire example.com/a v1.0.0\n"

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// fakeNet routes every request of the command's HTTP client to an in-process mux,
// so patterns can name hosts (github.com, proxy.test, git.test).
type fakeNet struct {
	mux     *http.ServeMux
	mu      sync.Mutex
	created []map[string]string
}

func (f *fakeNet) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Host = req.URL.Host
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func newFakeNet(t *testing.T) *fakeNet {
	t.Helper()
	f := &fakeNet{mux: http.NewServeMux()}
	// actions/checkout over git protocol v0 with an annotated v7.0.1 tag.
	f.mux.HandleFunc("GET github.com/actions/checkout/info/refs", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, pkt("# service=git-upload-pack\n")+"0000"+
			pkt(head0+" HEAD\x00multi_ack\n")+
			pkt(sha600+" refs/tags/v6.0.0\n")+
			pkt(head0+" refs/tags/v7.0.1\n")+
			pkt(sha701+" refs/tags/v7.0.1^{}\n")+
			"0000")
	})
	f.mux.HandleFunc("GET proxy.test/example.com/a/@v/list", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "v1.0.0\nv1.1.0\nv2.0.0\n")
	})
	f.mux.HandleFunc("GET git.test/api/v1/repos/o/r", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"default_branch":"main"}`)
	})
	f.mux.HandleFunc("GET git.test/api/v1/repos/o/r/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[]`)
	})
	f.mux.HandleFunc("POST git.test/api/v1/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body["auth"] = r.Header.Get("Authorization")
		f.mu.Lock()
		f.created = append(f.created, body)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"number":7,"html_url":"https://git.test/o/r/pulls/7","head":{"ref":%q}}`, body["head"])
	})
	prev := newHTTPClient
	newHTTPClient = func() *http.Client { return &http.Client{Transport: f} }
	t.Cleanup(func() { newHTTPClient = prev })
	return f
}

// cleanEnv isolates the command from the CI runner and the user's git/go configuration.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_ACTIONS", "GITEA_ACTIONS", "FORGEJO_ACTIONS", "GITHUB_STEP_SUMMARY", "GITEA_STEP_SUMMARY",
		"DEPS_TOKEN", "GITHUB_TOKEN", "GITEA_TOKEN", "FORGEJO_TOKEN", "DEPS_GITHUB_COM_TOKEN",
		"DEPS_DEFAULT_ACTIONS_URL", "DEPS_LOG_LEVEL", "DEPS_LOG_FORMAT", "GONOPROXY", "GOPRIVATE",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("GOPROXY", "https://proxy.test")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func execute(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	code = exitCode(root.ExecuteContext(t.Context()), &errb)
	return out.String(), errb.String(), code
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPlan(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow, "go.mod": gomodSrc})
	summary := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)

	stdout, stderr, code := execute(t, "plan", "-v", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"+      - uses: actions/checkout@v7.0.1\n",
		"+      - uses: actions/checkout@" + sha701 + " # v7.0.1\n",
		"+require example.com/a v1.1.0\n",
		"--- a/go.mod\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff lacks %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{
		"update  .github/workflows/ci.yml:6  actions/checkout  v6.0.0 -> v7.0.1  (major)\n",
		"update  .github/workflows/ci.yml:7  actions/checkout  v6.0.0@1af3b93 -> v7.0.1@3d3c42e  (major)\n",
		"update  go.mod:5  example.com/a  v1.0.0 -> v1.1.0  (minor)\n",
		"skip    .github/workflows/ci.yml:8  ./local  local action\n",
		"note    go.sum changes are computed by apply/run\n",
		"3 updates in 2 files, 1 skipped, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "go.mod")); string(got) != gomodSrc {
		t.Error("plan modified go.mod")
	}
	sum, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sum), "| `example.com/a` | `go.mod:5` | `v1.0.0` | `v1.1.0` | minor |") {
		t.Errorf("summary:\n%s", sum)
	}
}

func TestPlanUpstreamErrorExitsOne(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n      - uses: actions/missing@v1\n      - uses: actions/checkout@v6.0.0\n"})
	_, stderr, code := execute(t, "plan", "--dir", dir)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, want := range []string{"error   .github/workflows/ci.yml:4: actions/missing: ", "1 updates in 1 files, 0 skipped, 1 errors"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "deps: ") {
		t.Errorf("reported errors printed twice:\n%s", stderr)
	}
}

func TestApplyWritesFiles(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow})
	stdout, stderr, code := execute(t, "apply", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("apply printed a diff: %q", stdout)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
	want := strings.NewReplacer("checkout@v6.0.0", "checkout@v7.0.1", sha600+" # v6.0.0", sha701+" # v7.0.1").Replace(workflow)
	if string(got) != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	bad := t.TempDir()
	writeFiles(t, bad, map[string]string{".deps.yaml": "pinn: digest\n"})
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"plan", "--nope"}, "unknown flag: --nope"},
		{"extra args", []string{"plan", "x"}, "unexpected arguments: [x]"},
		{"log level", []string{"plan", "--dir", dir, "--log-level", "loud"}, `--log-level: invalid value "loud"`},
		{"log format", []string{"plan", "--dir", dir, "--log-format", "xml"}, `--log-format: invalid value "xml"`},
		{"config", []string{"plan", "--dir", bad}, "line 1: field pinn not found"},
		{"missing explicit config", []string{"plan", "--dir", dir, "--config", filepath.Join(dir, "nope.yaml")}, "nope.yaml"},
		{"platform", []string{"run", "--dir", dir, "--platform", "gitlab"}, `--platform: invalid value "gitlab"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := execute(t, tt.args...)
			if code != 2 {
				t.Errorf("exit %d, want 2\n%s", code, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr lacks %q:\n%s", tt.want, stderr)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	var w bytes.Buffer
	if c := exitCode(nil, &w); c != 0 || w.Len() != 0 {
		t.Errorf("nil: %d %q", c, w.String())
	}
	if c := exitCode(errors.New("boom"), &w); c != 1 || w.String() != "deps: boom\n" {
		t.Errorf("plain: %d %q", c, w.String())
	}
	w.Reset()
	if c := exitCode(fmt.Errorf("load: %w", &config.Error{Err: errors.New("bad")}), &w); c != 2 {
		t.Errorf("config error: %d", c)
	}
}

func TestVersionAndLogging(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	stdout, _, code := execute(t, "version")
	if code != 0 || stdout != version+"\n" {
		t.Errorf("version: %d %q", code, stdout)
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow})
	_, stderr, _ := execute(t, "plan", "--dir", dir, "--log-level", "info", "--log-format", "json")
	if !strings.Contains(stderr, `"msg":"actions default host","host":"github.com","source":"platform default"`) {
		t.Errorf("no json info log:\n%s", stderr)
	}
	t.Setenv("DEPS_LOG_LEVEL", "debug")
	_, stderr, _ = execute(t, "plan", "--dir", dir)
	if !strings.Contains(stderr, `level=DEBUG msg="tags listed" repo=https://github.com/actions/checkout protocol=v0 tags=2`) {
		t.Errorf("no debug log from env level:\n%s", stderr)
	}
	_, stderr, _ = execute(t, "plan", "--dir", dir, "--log-level", "warn")
	if strings.Contains(stderr, "level=") {
		t.Errorf("flag did not override env level:\n%s", stderr)
	}
}

// gitRepo creates a bare origin with one commit on main and returns a clone of it.
func gitRepo(t *testing.T, files map[string]string) (origin, work string) {
	t.Helper()
	origin = filepath.Join(t.TempDir(), "origin.git")
	git(t, "", "init", "-q", "--bare", "-b", "main", origin)
	work = t.TempDir()
	git(t, work, "clone", "-q", origin, ".")
	writeFiles(t, work, files)
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "init")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	return origin, work
}

func giteaEnv(t *testing.T) {
	t.Setenv("GITEA_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://git.test")
	t.Setenv("GITHUB_API_URL", "https://git.test/api/v1")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
}

func TestRunCreatesBranchAndPR(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	summary := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITEA_STEP_SUMMARY", summary)
	origin, work := gitRepo(t, map[string]string{".github/workflows/ci.yml": workflow})

	_, stderr, code := execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	const branch = "deps/github-actions/actions-checkout"
	for _, want := range []string{
		"pr      created  " + branch + "  chore(deps): update actions/checkout to v7.0.1  https://git.test/o/r/pulls/7\n",
		"1 created, 0 updated, 0 closed, 0 up-to-date, 0 skipped, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if len(f.created) != 1 || f.created[0]["head"] != branch || f.created[0]["base"] != "main" || f.created[0]["auth"] != "token tok" {
		t.Errorf("created PRs = %v", f.created)
	}
	content := git(t, origin, "show", branch+":.github/workflows/ci.yml")
	if !strings.Contains(content, "actions/checkout@v7.0.1") || !strings.Contains(content, sha701+" # v7.0.1") {
		t.Errorf("pushed file:\n%s", content)
	}
	if got := git(t, origin, "log", "-1", "--format=%an <%ae>", branch); got != "deps[bot] <bot@example.invalid>" {
		t.Errorf("author = %q", got)
	}
	if git(t, work, "status", "--porcelain") != "" {
		t.Error("run modified the working tree")
	}
	sum, _ := os.ReadFile(summary)
	if !strings.Contains(string(sum), "| chore(deps): update actions/checkout to v7.0.1 | created | [#7](https://git.test/o/r/pulls/7) |") {
		t.Errorf("summary:\n%s", sum)
	}
}

func TestRunDryRunAndToken(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	origin, work := gitRepo(t, map[string]string{".github/workflows/ci.yml": workflow})

	_, stderr, code := execute(t, "run", "--dir", work)
	if code != 1 || !strings.Contains(stderr, "deps: no token for git.test\n") {
		t.Errorf("no token: exit %d\n%s", code, stderr)
	}

	_, stderr, code = execute(t, "run", "--dir", work, "--dry-run")
	if code != 0 {
		t.Fatalf("dry run exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "pr      would-create  deps/github-actions/actions-checkout") ||
		!strings.Contains(stderr, "(dry run: 1 would change)") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if len(f.created) != 0 || git(t, origin, "branch", "--list", "deps/*") != "" {
		t.Error("dry run wrote to the platform or origin")
	}
}
