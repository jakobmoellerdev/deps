package main

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
