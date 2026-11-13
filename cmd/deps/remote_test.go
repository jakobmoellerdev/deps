package main

import (
	"encoding/base64"
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestParseRepoSpec(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		want        repoSpec
		err         string
	}{
		{name: "owner and name", entry: "o/r", want: repoSpec{Repo: "o/r"}},
		{name: "ref", entry: "o/r#v1.2.0", want: repoSpec{Repo: "o/r", Ref: "v1.2.0"}},
		{name: "directory and ref", entry: "o/r//services/api#main", want: repoSpec{Repo: "o/r", Path: "services/api", Ref: "main"}},
		{name: "glob", entry: "o/svc-*", want: repoSpec{Repo: "o/svc-*"}},
		{name: "gitlab path", entry: "g/sub/p", want: repoSpec{Repo: "g/sub/p"}},
		{name: "url", entry: "https://git.test/o/r.git#main", want: repoSpec{Repo: "https://git.test/o/r.git", Ref: "main", URL: true}},
		{name: "url with directory", entry: "https://git.test/o/r.git//sub", want: repoSpec{Repo: "https://git.test/o/r.git", Path: "sub", URL: true}},
		{name: "ref and glob", entry: "o/*#main", err: "needs one repository, not a glob"},
		{name: "escaping path", entry: "o/r//../etc", err: "clean relative slash path"},
		{name: "bad ref", entry: "o/r#-x", err: "invalid ref"},
		{name: "bad scheme", entry: "ftp://git.test/o/r", err: "scheme must be"},
		{name: "url without path", entry: "https://git.test/", err: "has no path"},
		{name: "url with password", entry: "https://u:p@git.test/o/r", err: "must not contain a password"},
		{name: "no name", entry: "o", err: "must be owner/name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRepoSpec(tc.entry)
			switch {
			case tc.err != "":
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
			case err != nil:
				t.Fatal(err)
			case got != tc.want:
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// plan --repositories clones every repository of the glob, plans it with the config found
// there, prefixes the diff paths with the repository and writes one report over all of them.
func TestPlanRepositories(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{
		"o/r":   {".github/workflows/ci.yml": workflow},
		"o/s":   {".github/workflows/ci.yml": workflow, ".deps.yaml": "extends: [o/cfg]\n"},
		"o/cfg": {".deps.yaml": orgConfig},
	})
	f.mux.HandleFunc("GET git.test/api/v1/orgs/o/repos", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"full_name":"o/r"},{"full_name":"o/s"}]`)
	})
	report := filepath.Join(t.TempDir(), "report.json")

	stdout, stderr, code := execute(t, "plan", "--dir", t.TempDir(), "--repositories", "o/*", "--report", report)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{"repository o/r\n", "repository o/s\n", "2 repositories, 0 failed\n"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	for _, want := range []string{"--- a/o/r/.github/workflows/ci.yml\n", "--- a/o/s/.github/workflows/ci.yml\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Command      string `json:"command"`
		Repositories []struct {
			Repository string `json:"repository"`
		} `json:"repositories"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Command != "plan" || len(doc.Repositories) != 2 || doc.Repositories[0].Repository != "o/r" || doc.Repositories[1].Repository != "o/s" || doc.Counts["updates"] != 3 {
		t.Errorf("report = %s", b)
	}
}

// A single entry with a ref keeps the plain diff paths, and explain reads the same clone.
func TestPlanRepositoryRefAndExplain(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow}})

	stdout, stderr, code := execute(t, "plan", "--dir", t.TempDir(), "--repositories", "o/r#main")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "--- a/o/r/.github/workflows/ci.yml\n") {
		t.Errorf("stdout:\n%s", stdout)
	}
	stdout, stderr, code = execute(t, "explain", "--dir", t.TempDir(), "--repositories", "o/r", "actions/checkout")
	if code != 0 {
		t.Fatalf("explain: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "repository o/r\n") || !strings.Contains(stdout, "actions/checkout  .github/workflows/ci.yml") {
		t.Errorf("explain stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// A repository that fails does not stop the others and is recorded in the report.
func TestPlanRepositoriesError(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow}})
	f.mux.HandleFunc("GET git.test/api/v1/orgs/o/repos", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"full_name":"o/gone"},{"full_name":"o/r"}]`)
	})

	_, stderr, code := execute(t, "plan", "--dir", t.TempDir(), "--repositories", "o/*")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "o/gone: clone:") || !strings.Contains(stderr, "2 repositories, 1 failed\n") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

// run rejects the entry forms that only the read-only commands support.
func TestRunRejectsRefAndURLEntries(t *testing.T) {
	cleanEnv(t)
	for _, tc := range []struct{ entry, want string }{
		{"o/r#v1", "run reads the base branch; use --base"},
		{"https://git.test/o/r.git", "run needs owner/name entries on its forge"},
	} {
		_, stderr, code := execute(t, "run", "--dir", t.TempDir(), "--repositories", tc.entry)
		if code != 2 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d\n%s", tc.entry, code, stderr)
		}
	}
}

// apply --repositories keeps each clone under --into with the updates written.
func TestApplyRepositoriesInto(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow}, "o/s": {".github/workflows/ci.yml": workflow}})
	f.mux.HandleFunc("GET git.test/api/v1/orgs/o/repos", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"full_name":"o/r"},{"full_name":"o/s"}]`)
	})
	into := filepath.Join(t.TempDir(), "work")

	_, stderr, code := execute(t, "apply", "--dir", t.TempDir(), "--repositories", "o/*", "--into", into)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, repo := range []string{"o/r", "o/s"} {
		p := filepath.Join(into, filepath.FromSlash(repo), ".github", "workflows", "ci.yml")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		if strings.Contains(string(b), "@v6.0.0\n") {
			t.Errorf("%s: not updated:\n%s", repo, b)
		}
		if _, err := os.Stat(filepath.Join(into, filepath.FromSlash(repo), ".git")); err != nil {
			t.Errorf("%s: clone not kept: %v", repo, err)
		}
	}
	if !strings.Contains(stderr, "checkout "+filepath.Join(into, "o", "r")+"\n") {
		t.Errorf("stderr lacks the checkout line:\n%s", stderr)
	}
}

// --into and --repositories need each other.
func TestApplyIntoUsage(t *testing.T) {
	cleanEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"apply", "--repositories", "o/r"}, "--repositories needs --into"},
		{[]string{"apply", "--into", "x"}, "--into needs --repositories"},
	} {
		_, stderr, code := execute(t, tc.args...)
		if code != 2 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit %d\n%s", tc.args, code, stderr)
		}
	}
}

// config check and config show read a clone of --repositories.
func TestConfigRepositories(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{"o/r": {".deps.yaml": "pin: digest\n"}})

	stdout, stderr, code := execute(t, "config", "check", "--dir", t.TempDir(), "--repositories", "o/r")
	if code != 0 || !strings.Contains(stdout, ".deps.yaml: ok\n") || !strings.Contains(stderr, "repository o/r\n") {
		t.Fatalf("check: exit %d\n%s%s", code, stdout, stderr)
	}
	stdout, stderr, code = execute(t, "config", "show", "--dir", t.TempDir(), "--repositories", "o/r")
	if code != 0 || !strings.Contains(stdout, "pin: digest") {
		t.Errorf("show: exit %d\n%s%s", code, stdout, stderr)
	}
}

// preview --repositories --pr clones the pull request's head ref and its base branch.
func TestPreviewPullRequest(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	root := forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow}})
	// The pull request adds a second workflow, under refs/pull/7/head.
	work := t.TempDir()
	git(t, "", "clone", "-q", filepath.Join(root, "o", "r.git"), work)
	writeFiles(t, work, map[string]string{".github/workflows/pr.yml": workflow})
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "pr")
	git(t, work, "push", "-q", "origin", "HEAD:refs/pull/7/head")
	var auth string
	f.mux.HandleFunc("GET git.test/api/v1/repos/o/r/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		io.WriteString(w, `{"number":7,"title":"t","head":{"ref":"feature","sha":"deadbeef"},"base":{"ref":"main"},"state":"open"}`)
	})

	stdout, stderr, code := execute(t, "preview", "--dir", t.TempDir(), "--repositories", "o/r", "--pr", "7")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "pull request o/r#7  feature -> main\n") {
		t.Errorf("stderr:\n%s", stderr)
	}
	// Both sides carry the same dependency, so the pull request changes no update PR.
	// The head clone has the file the pull request adds, the base clone does not, so the
	// update PR of the pull request's side differs.
	if !strings.Contains(stderr, ".github/workflows/pr.yml") || !strings.Contains(stderr, "changed deps/actions-checkout-major") {
		t.Errorf("stderr:\n%s", stderr)
	}
	_ = stdout
	// The repository is on the token's own forge, so the API client carries the token and
	// the clones carry the git credentials.
	if auth != "token tok" {
		t.Errorf("pull request request authorization = %q, want the token", auth)
	}
	if !strings.Contains(stderr, "::add-mask::"+base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))) {
		t.Errorf("clone not authenticated:\n%s", stderr)
	}
}

// A --repositories entry given as a git URL carries its own forge, which may be a host the
// forge token does not belong to. Its clone must stay anonymous: the header is written into
// the clone's config, so attaching it would hand the token to a foreign server.
func TestRemoteSourceAuthHeaderOnlyOwnHost(t *testing.T) {
	cleanEnv(t)
	giteaEnv(t)
	src, err := newRemoteSource(t.Context(), &rootOpts{dir: t.TempDir()},
		[]string{"https://git.test/o/r.git", "https://gitlab.example.org/x/y.git"}, "tok", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	own := repoTarget{Info: platform.Info{Kind: platform.Gitea, Host: "git.test", ServerURL: "https://git.test"}}
	if h := src.authHeader(own); !strings.Contains(h, base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))) {
		t.Errorf("header for the token's own host = %q, want the token", h)
	}
	foreign := repoTarget{Info: platform.Info{Kind: platform.GitLab, Host: "gitlab.example.org", ServerURL: "https://gitlab.example.org"}}
	if h := src.authHeader(foreign); h != "" {
		t.Errorf("header for a foreign host = %q, want none", h)
	}
	// A URL without a forge (file://) has no host either.
	if h := src.authHeader(repoTarget{}); h != "" {
		t.Errorf("header without a forge = %q, want none", h)
	}
}

// run --repositories clones with the forge token: the source it builds names the token's
// host, so every clone of that forge carries the auth header. Without it the clones go out
// anonymously and a private repository fails at the fetch, a public one at the push.
func TestRunRepositoriesAuthenticatesClones(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow}})

	_, stderr, code := execute(t, "run", "--dry-run", "--dir", t.TempDir(), "--repositories", "o/r")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	// The credentials are masked in the job log right before they are written into the
	// clone's config (gitAuthHeader), so their mask tells the clone is authenticated.
	creds := base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))
	if !strings.Contains(stderr, "::add-mask::"+creds+"\n") {
		t.Errorf("clone not authenticated:\n%s", stderr)
	}

	// That source hands the token to its own forge alone.
	info := platform.Info{Kind: platform.Gitea, Host: "git.test", ServerURL: "https://git.test"}
	src := runRemoteSource([]repoSpec{{Repo: "o/r"}}, info, &runShared{token: "tok"})
	if h := src.authHeader(repoTarget{Info: info}); !strings.Contains(h, creds) {
		t.Errorf("header for the token's own host = %q, want the token", h)
	}
	foreign := repoTarget{Info: platform.Info{Kind: platform.GitLab, Host: "gitlab.example.org", ServerURL: "https://gitlab.example.org"}}
	if h := src.authHeader(foreign); h != "" {
		t.Errorf("header for a foreign host = %q, want none", h)
	}
}

// A --repositories entry naming a foreign forge must see no forge credentials: neither the
// token in the API request that reads the pull request nor the git credentials in the clone
// config, and the session of a clone there is opened without the token too.
func TestRemoteSourceForeignForgeKeepsTheToken(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t) // the token belongs to git.test
	t.Setenv("DEPS_TOKEN", "tok")
	root := forge(t, map[string]map[string]string{"x/y": {".github/workflows/ci.yml": workflow}})
	// github.com is the foreign forge here; its clones are served from the same local root.
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_1", "url.file://"+filepath.ToSlash(root)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_1", "https://github.com/")
	work := t.TempDir()
	git(t, "", "clone", "-q", filepath.Join(root, "x", "y.git"), work)
	writeFiles(t, work, map[string]string{".github/workflows/pr.yml": workflow})
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "pr")
	git(t, work, "push", "-q", "origin", "HEAD:refs/pull/3/head")
	// Every request of the commands, so the lookups of the session are seen too.
	var mu sync.Mutex
	sent := map[string]string{}
	prevClient := newHTTPClient
	newHTTPClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if a := r.Header.Get("Authorization"); a != "" {
				mu.Lock()
				sent[r.URL.Host] = a
				mu.Unlock()
			}
			return f.RoundTrip(r)
		})}
	}
	t.Cleanup(func() { newHTTPClient = prevClient })
	var auth string
	f.mux.HandleFunc("GET api.github.com/repos/x/y/pulls/3", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		io.WriteString(w, `{"number":3,"title":"t","head":{"ref":"feature","sha":"deadbeef"},"base":{"ref":"main"},"state":"open"}`)
	})

	_, stderr, code := execute(t, "preview", "--dir", t.TempDir(), "--repositories", "https://github.com/x/y.git", "--pr", "3")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if auth != "" {
		t.Errorf("foreign forge API request authorization = %q, want none", auth)
	}
	// The clone credentials are masked right before they are written into a clone's config,
	// so their mask in the job log tells that the foreign clone got them.
	if creds := base64.StdEncoding.EncodeToString([]byte("x-access-token:tok")); strings.Contains(stderr, creds) {
		t.Errorf("foreign clone got the git credentials:\n%s", stderr)
	}
	// plan of the same entry opens the session of the clone without the token: its auth
	// would otherwise register the token for the foreign forge and send it with every
	// lookup there, such as the tags of the actions the cloned workflow uses.
	_, stderr, code = execute(t, "plan", "--dir", t.TempDir(), "--repositories", "https://github.com/x/y.git")
	if code != 0 {
		t.Fatalf("plan: exit %d\n%s", code, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, host := range []string{"github.com", "api.github.com"} {
		if a := sent[host]; a != "" {
			t.Errorf("request to the foreign forge %s carried %q, want no credentials", host, a)
		}
	}
}

// roundTripFunc is an http.RoundTripper of one function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
