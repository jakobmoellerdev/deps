package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
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

func TestActionsRunner(t *testing.T) {
	for _, tt := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"GITHUB_ACTIONS": "true"}, true},
		{map[string]string{"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true"}, true},
		{map[string]string{"GITEA_ACTIONS": "true"}, true},
		{map[string]string{"GITHUB_ACTIONS": "true", "FORGEJO_ACTIONS": "true"}, false},
		{map[string]string{"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true", "FORGEJO_ACTIONS": "true"}, false},
	} {
		if got := actionsRunner(func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("env %v: %v, want %v", tt.env, got, tt.want)
		}
	}
	// Off, an annotator writes nothing.
	var b bytes.Buffer
	a := newAnnotator(func(string) string { return "" }, &b, ".")
	a.error(errors.New("x"))
	a.group("g")
	a.endGroup()
	a.mask("secret")
	a.security(dep.Update{Advisories: []dep.Advisory{{ID: "GHSA-1"}}})
	if b.Len() > 0 {
		t.Errorf("wrote %q", b.String())
	}
}

// The actions format is auto on GitHub and Gitea runners and forced by --log-format actions;
// Forgejo and an explicit other format get none. Phases are log groups.
func TestActionsFormatSelection(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		args []string
		want bool
	}{
		{"github", map[string]string{"GITHUB_ACTIONS": "true"}, nil, true},
		{"gitea", map[string]string{"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true"}, nil, true},
		{"forgejo", map[string]string{"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true", "FORGEJO_ACTIONS": "true"}, nil, false},
		{"forced", nil, []string{"--log-format", "actions"}, true},
		{"forced on forgejo", map[string]string{"FORGEJO_ACTIONS": "true"}, []string{"--log-format", "actions"}, true},
		{"text on github", map[string]string{"GITHUB_ACTIONS": "true"}, []string{"--log-format", "text"}, false},
		{"none", nil, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cleanEnv(t)
			newFakeNet(t)
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n      - uses: actions/missing@v1\n"})
			githubActions(t, dir)
			t.Setenv("GITHUB_ACTIONS", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, stderr, _ := execute(t, append([]string{"plan", "--dir", dir}, tt.args...)...)
			for _, want := range []string{
				"::group::Discovering and extracting dependency files\n",
				"deps: extracting ",
				"::group::Resolving 1 dependency\n",
				"deps: resolving 1/1 · 1 error · ",
				"::error file=.github/workflows/ci.yml,line=4,title=deps::actions/missing: ",
			} {
				if got := strings.Contains(stderr, want); got != tt.want {
					t.Errorf("contains %q = %v, want %v:\n%s", want, got, tt.want, stderr)
				}
			}
			if !tt.want && strings.Contains(stderr, "::") {
				t.Errorf("workflow commands without the actions format:\n%s", stderr)
			}
		})
	}
}

// Security updates are notices at their file and line, at most maxAnnotations.
func TestAnnotationsSecurityNotice(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":     "module example.com/x\n\ngo 1.24\n\nrequire example.com/vuln v1.0.0\n",
		".deps.yaml": "rules: [{manager: gomod, updates: [patch]}]\n",
	})
	githubActions(t, dir)
	_, stderr, code := execute(t, "plan", "--dir", dir)
	if want := "::notice file=go.mod,line=5,title=deps::security update example.com/vuln v1.0.0 -> v1.1.0: GO-TEST-1"; code != 0 || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr lacks %q:\n%s", code, want, stderr)
	}
	var b bytes.Buffer
	a := &annotator{on: true, w: &b}
	for range maxAnnotations + 2 {
		a.security(dep.Update{Dep: dep.Dep{Name: "n", File: "f", Line: 1, Version: "1"}, NewVersion: "2", Advisories: []dep.Advisory{{ID: "A", Summary: "50%"}, {ID: "B"}}})
	}
	a.error(errors.New("e")) // notices have their own budget
	if n := strings.Count(b.String(), "::notice "); n != maxAnnotations || !strings.Contains(b.String(), "::notice file=f,line=1,title=deps::security update n 1 -> 2: A (50%25), B\n") || !strings.Contains(b.String(), "::error ") {
		t.Errorf("%d notices:\n%s", n, b.String())
	}
}

func TestAnnotatorMask(t *testing.T) {
	var b bytes.Buffer
	a := &annotator{on: true, w: &b}
	a.mask("")
	a.mask("s3cr%t\nx")
	if want := "::add-mask::s3cr%25t%0Ax\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// RUNNER_DEBUG=1 makes debug the default level; $DEPS_LOG_LEVEL and the flag still win.
func TestRunnerDebug(t *testing.T) {
	cleanEnv(t)
	t.Setenv("RUNNER_DEBUG", "1")
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow})
	const debug = "level=DEBUG"
	if _, stderr, _ := execute(t, "plan", "--dir", dir); !strings.Contains(stderr, debug) {
		t.Errorf("RUNNER_DEBUG=1: no debug records:\n%s", stderr)
	}
	if _, stderr, _ := execute(t, "plan", "--dir", dir, "--log-level", "warn"); strings.Contains(stderr, debug) {
		t.Errorf("--log-level warn: debug records:\n%s", stderr)
	}
	t.Setenv("DEPS_LOG_LEVEL", "info")
	if _, stderr, _ := execute(t, "plan", "--dir", dir); strings.Contains(stderr, debug) || !strings.Contains(stderr, "level=INFO") {
		t.Errorf("DEPS_LOG_LEVEL=info:\n%s", stderr)
	}
}

func TestAnnotatorLimitAndEscaping(t *testing.T) {
	var b bytes.Buffer
	env := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_WORKSPACE": "/ws"}
	a := newAnnotator(func(k string) string { return env[k] }, &b, "/ws/sub")
	a.on = true
	a.error(&engine.DepError{File: "a,b:c.yml", Line: 2, Name: "n", Err: errors.New("50% off\nnext")})
	if want := "::error file=sub/a%2Cb%3Ac.yml,line=2,title=deps::n: 50%25 off%0Anext\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	b.Reset()
	a.error(fmt.Errorf("wrapped: %w", &engine.FileError{File: "go.mod", Err: errors.New("bad")}))
	if want := "::error file=sub/go.mod,title=deps::bad\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	b.Reset()
	for i := range maxAnnotations + 5 {
		a.error(fmt.Errorf("e%d", i))
		a.command("warning", "", 0, "w")
	}
	if n := strings.Count(b.String(), "::error "); n != maxAnnotations-2 {
		t.Errorf("%d error annotations after the first two, want %d", n, maxAnnotations-2)
	}
	if n := strings.Count(b.String(), "::warning "); n != maxAnnotations {
		t.Errorf("%d warning annotations, want %d", n, maxAnnotations)
	}
}

// The actions format writes pretty records without the elapsed column, colored unless
// NO_COLOR or --color never; each phase's summary record is inside its group.
func TestActionsRecords(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow})
	githubActions(t, dir)
	_, stderr, _ := execute(t, "plan", "--dir", dir, "--log-level", "info")
	if !strings.Contains(stderr, "\x1b[36mINF\x1b[0m files discovered  \x1b[2mfiles=") {
		t.Errorf("no colored record without elapsed column:\n%q", stderr)
	}
	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(stderr, "")
	for _, order := range [][]string{
		{"::group::Discovering and extracting dependency files\n", "INF files discovered  ", "INF dependencies extracted  ", "deps: extracting ", "::endgroup::\n::group::Resolving 2 dependencies\n"},
		{"::group::Resolving 2 dependencies\n", "INF dependencies resolved  ", "deps: resolving 2/2 · ", "::endgroup::\n"},
	} {
		at := 0
		for _, s := range order {
			i := strings.Index(plain[at:], s)
			if i < 0 {
				t.Fatalf("%q missing after offset %d:\n%s", s, at, plain)
			}
			at += i + len(s)
		}
	}
	for _, args := range [][]string{{"--color", "never"}, nil} {
		if args == nil {
			t.Setenv("NO_COLOR", "1")
		}
		if _, stderr, _ := execute(t, append([]string{"plan", "--dir", dir, "--log-level", "info"}, args...)...); strings.Contains(stderr, "\x1b[") || !strings.Contains(stderr, "\nINF files discovered  files=") {
			t.Errorf("%v: colors or no actions records:\n%q", args, stderr)
		}
	}
}
