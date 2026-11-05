package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAnnotationsSubmitErrors(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go.mod": "module x\n\nrequire (\n"})
	githubActions(t, dir)
	_, stderr, _ := execute(t, "submit", "--dry-run", "--dir", dir)
	if want := "::error file=go.mod"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}

func TestAnnotationsPreviewErrors(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	work := t.TempDir()
	base, pr := filepath.Join(work, "base"), filepath.Join(work, "pr")
	writeFiles(t, base, map[string]string{"go.mod": gomodSrc})
	writeFiles(t, pr, map[string]string{"go.mod": gomodSrc + "require example.com/missing v1.0.0\n"})
	githubActions(t, work)
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(work, "summary.md"))
	_, stderr, _ := execute(t, "preview", "--dir", pr, "--base-dir", base)
	if want := "::error file=pr/go.mod,line=6,title=deps::example.com/missing: "; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}
