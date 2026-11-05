package main

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forge creates bare repositories <root>/<owner/name>.git with one commit on main each and
// makes git reach https://git.test/<owner/name>.git there.
func forge(t *testing.T, repos map[string]map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, files := range repos {
		origin := filepath.Join(root, filepath.FromSlash(name)+".git")
		git(t, "", "init", "-q", "--bare", "-b", "main", origin)
		work := t.TempDir()
		git(t, work, "clone", "-q", origin, ".")
		writeFiles(t, work, files)
		git(t, work, "add", "-A")
		git(t, work, "commit", "-qm", "init")
		git(t, work, "push", "-q", "origin", "HEAD:main")
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(root)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://git.test/")
	return root
}

const orgConfig = `pin: digest
rules:
  - names: ["actions/*"]
    allow: "< 7"
`

func TestExplainExtends(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	root := forge(t, map[string]map[string]string{"org/deps-config": {".deps.yaml": orgConfig, "presets/ci.yaml": "rules:\n  - managers: [github-actions]\n    group: ci\n"}})
	dir := t.TempDir()
	byURL := "file://" + filepath.ToSlash(root) + "/org/deps-config.git//presets/ci.yaml#main"
	writeFiles(t, dir, map[string]string{
		".github/workflows/ci.yml": workflow,
		".deps.yaml":               "extends:\n  - org/deps-config\n  - " + byURL + "\nrules:\n  - names: [actions/checkout]\n    pin: keep\n",
	})
	stdout, stderr, code := execute(t, "explain", "--dir", dir, "actions/checkout")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"  rules     #0 match: names=[actions/*]; apply: allow=<7 (from org/deps-config//.deps.yaml:3)\n",
		"            #1 match: managers=[github-actions]; apply: group=ci (from " + byURL + ":2)\n",
		"            #2 match: names=[actions/checkout]; apply: pin=keep (from .deps.yaml:5)\n",
		"  policy    pin=keep allow=<7 ",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}

	// An extended file with an unknown key fails the config, located at the extends entry.
	writeFiles(t, dir, map[string]string{".deps.yaml": "extends:\n  - org/deps-config//presets/ci.yaml\n  - org/deps-config//missing.yaml\n"})
	_, stderr, code = execute(t, "explain", "--dir", dir, "actions/checkout")
	if code != 2 || !strings.Contains(stderr, "extends[1]: org/deps-config//missing.yaml: no file missing.yaml at HEAD of https://git.test/org/deps-config.git") {
		t.Errorf("missing file: exit %d\n%s", code, stderr)
	}
}

func TestRunRepositories(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	root := forge(t, map[string]map[string]string{
		"o/r":   {".github/workflows/ci.yml": workflow},
		"o/s":   {".github/workflows/ci.yml": workflow, ".deps.yaml": "extends: [o/cfg]\n"},
		"o/cfg": {".deps.yaml": orgConfig},
	})
	f.mux.HandleFunc("GET git.test/api/v1/orgs/o/repos", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"full_name":"o/r"},{"full_name":"o/s"},{"full_name":"o/old","archived":true}]`)
	})
	f.mux.HandleFunc("GET git.test/api/v1/repos/o/s", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"default_branch":"main"}`)
	})
	for _, p := range []string{"pulls", "issues"} {
		f.mux.HandleFunc("GET git.test/api/v1/repos/o/s/"+p, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `[]`) })
	}
	f.mux.HandleFunc("POST git.test/api/v1/repos/o/s/pulls", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.created = append(f.created, body)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"number":8,"html_url":"https://git.test/o/s/pulls/8","head":{"ref":%q}}`, body["head"])
	})
	report := filepath.Join(t.TempDir(), "report.json")
	outputs := filepath.Join(t.TempDir(), "outputs")
	t.Setenv("GITHUB_OUTPUT", outputs)

	_, stderr, code := execute(t, "run", "--dir", t.TempDir(), "--repositories", "o/*", "--report", report, "--author-email", "bot@example.invalid")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"repository o/r\n",
		"pr      created  deps/github-actions/actions-checkout-major",
		"repository o/s\n",
		"pr      created  deps/github-actions/actions-checkout  chore(deps): pin actions/checkout to 1af3b93  https://git.test/o/s/pulls/8\n",
		"2 repositories, 0 failed\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "o/old") {
		t.Errorf("archived repository ran:\n%s", stderr)
	}
	// o/r got its major update; o/s, whose org config allows < 7 and pins digests, only the
	// pin. Both were pushed to their origins.
	for repo, branch := range map[string]string{"o/r": "deps/github-actions/actions-checkout-major", "o/s": "deps/github-actions/actions-checkout"} {
		if got := git(t, "", "--git-dir", filepath.Join(root, repo+".git"), "branch", "--list", "deps/*"); strings.TrimSpace(got) != branch {
			t.Errorf("%s branches = %q, want %s", repo, got, branch)
		}
	}
	if len(f.created) != 2 {
		t.Errorf("created = %v", f.created)
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
	if doc.Command != "run" || len(doc.Repositories) != 2 || doc.Repositories[0].Repository != "o/r" || doc.Repositories[1].Repository != "o/s" || doc.Counts["created"] != 2 {
		t.Errorf("report = %s", b)
	}
	if out, _ := os.ReadFile(outputs); !strings.Contains(string(out), "created=2\n") || strings.Count(string(out), "created=") != 1 {
		t.Errorf("outputs = %q", out)
	}
}

// A repository of --repositories with baseBranches runs every base in its clone.
func TestRunRepositoriesBaseBranches(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	root := forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow, ".deps.yaml": "baseBranches: [$default, release/v1]\n"}})
	git(t, "", "--git-dir", filepath.Join(root, "o", "r.git"), "branch", "release/v1", "main")
	_, stderr, code := execute(t, "run", "--dry-run", "--dir", t.TempDir(), "--repositories", "o/r")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{"repository o/r\n", "base    main\n", "base    release/v1\n", "deps/main/github-actions/", "deps/release/v1/github-actions/", "1 repositories, 0 failed\n"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestRunRepositoriesErrors(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	forge(t, map[string]map[string]string{"o/r": {".github/workflows/ci.yml": workflow}, "o/bad": {".deps.yaml": "pinn: digest\n"}})
	f.mux.HandleFunc("GET git.test/api/v1/orgs/none/repos", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `[]`) })
	for _, tt := range []struct {
		args []string
		env  string
		code int
		want string
	}{
		{args: []string{"--repositories", "o"}, code: 2, want: `--repositories: "o" must be owner/name or owner/<glob>`},
		{args: []string{"--repositories", "*/r"}, code: 2, want: `must be owner/name or owner/<glob>`},
		{args: []string{"--repositories", "o/["}, code: 2, want: `syntax error in pattern`},
		{args: []string{"--repositories", "o/r", "--repo", "o/r"}, code: 2, want: "--repo and --repositories are mutually exclusive"},
		{args: []string{"--repositories", "none/*"}, code: 1, want: "--repositories: no repository matches none/*"},
		{args: []string{"--repositories", "gone/*"}, code: 1, want: "--repositories: list the repositories of gone: GET /users/gone/repos"},
		// A repository whose config is invalid or that cannot be cloned does not stop the others.
		{env: "o/bad o/missing,o/r", code: 1, want: "3 repositories, 2 failed\n"},
	} {
		t.Setenv("DEPS_REPOSITORIES", tt.env)
		_, stderr, code := execute(t, append([]string{"run", "--dry-run", "--dir", t.TempDir()}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.want) {
			t.Errorf("%v %q: exit %d, want %d and %q:\n%s", tt.args, tt.env, code, tt.code, tt.want, stderr)
		}
		if tt.env != "" {
			for _, want := range []string{"   o/bad: ", "::error title=deps::o/bad: pinn: unknown key (did you mean pin?)", "   o/missing: clone: ", "::error title=deps::o/missing: clone: ", "repository o/r\n", "(dry run: 1 would change"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
		}
	}
}
