package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// githubActions makes the next command run as a GitHub Actions step with dir as workspace.
func githubActions(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_WORKSPACE", dir)
	t.Cleanup(func() { actionsLog = &annotator{} })
}

func TestAnnotationsPlan(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	ws := t.TempDir()
	dir := filepath.Join(ws, "repo")
	writeFiles(t, dir, map[string]string{
		".github/workflows/ci.yml": workflow,
		"go.mod":                   gomodSrc + "require example.com/missing v1.0.0\n",
	})
	githubActions(t, ws)

	_, stderr, code := execute(t, "plan", "-v", "--dir", dir)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"::group::2 skipped dependencies\nskip    .github/workflows/ci.yml:8  ./local  local action\n",
		"::endgroup::\n",
		"::error file=repo/go.mod,line=6,title=deps::example.com/missing: ",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestAnnotationsConfig(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".deps.yaml": "rules:\n  - manager: gomod\n    bogus: 1\n"})
	githubActions(t, dir)
	_, stderr, code := execute(t, "plan", "--dir", dir)
	if want := "::error file=.deps.yaml,line=3,title=deps::field bogus not found in type config.Rule\n"; code != 2 || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr lacks %q:\n%s", code, want, stderr)
	}
}

func TestAnnotationsOffOutsideGitHub(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true"},
		{"GITHUB_ACTIONS": "true", "FORGEJO_ACTIONS": "true"},
	} {
		var b bytes.Buffer
		a := newAnnotator(func(k string) string { return env[k] }, &b, ".")
		a.error(errors.New("x"))
		a.group("g")
		a.endGroup()
		if b.Len() > 0 {
			t.Errorf("env %v: wrote %q", env, b.String())
		}
	}
}

func TestAnnotatorLimitAndEscaping(t *testing.T) {
	var b bytes.Buffer
	env := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_WORKSPACE": "/ws"}
	a := newAnnotator(func(k string) string { return env[k] }, &b, "/ws/sub")
	a.error(&engine.DepError{File: "a,b:c.yml", Line: 2, Name: "n", Err: errors.New("50% off\nnext")})
	if want := "::error file=sub/a%2Cb%3Ac.yml,line=2,title=deps::n: 50%25 off%0Anext\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	b.Reset()
	for i := range maxAnnotations + 5 {
		a.error(fmt.Errorf("e%d", i))
		a.command("warning", "", 0, "w")
	}
	if n := strings.Count(b.String(), "::error "); n != maxAnnotations-1 {
		t.Errorf("%d error annotations after the first, want %d", n, maxAnnotations-1)
	}
	if n := strings.Count(b.String(), "::warning "); n != maxAnnotations {
		t.Errorf("%d warning annotations, want %d", n, maxAnnotations)
	}
}
