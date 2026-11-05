package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const renovateIgnoreA = `{
  // example.com/a stays where it is
  packageRules: [{matchPackageNames: ['example.com/a'], enabled: false}],
}
`

func TestPlanRenovate(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow, "go.mod": gomodSrc, "renovate.json": renovateIgnoreA})

	stdout, stderr, code := execute(t, "plan", "--show-skipped", "--renovate", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "skip    go.mod:5  example.com/a  ignored by rule #0\n") || strings.Contains(stdout, "example.com/a v1.1.0") {
		t.Errorf("example.com/a not ignored:\n%s\n%s", stderr, stdout)
	}
	if !strings.Contains(stdout, "+      - uses: actions/checkout@v7.0.1\n") {
		t.Errorf("checkout not updated:\n%s", stdout)
	}

	// --config naming a Renovate file turns compatibility mode on.
	stdout2, stderr2, code := execute(t, "plan", "--show-skipped", "--dir", dir, "--config", filepath.Join(dir, "renovate.json"))
	if code != 0 || stdout2 != stdout {
		t.Errorf("--config renovate.json: exit %d\n%s\n%s", code, stderr2, stdout2)
	}
}

func TestMigrate(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow, "go.mod": gomodSrc, ".github/renovate.json5": renovateIgnoreA})

	_, stderr, code := execute(t, "migrate", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	out := filepath.Join(dir, ".deps.yaml")
	if !strings.Contains(stderr, "wrote "+out+" from "+filepath.Join(dir, ".github", "renovate.json5")+": 6 rules, 0 custom managers, 0 custom datasources; 1 warnings, 1 notes\n") ||
		!strings.Contains(stderr, "warn    branchPrefix: deps run closes open PRs under renovate/") {
		t.Errorf("stderr:\n%s", stderr)
	}
	written, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(written), "# Translated from .github/renovate.json5 by deps (Renovate compatibility).\n") {
		t.Fatalf("written = %q, %v", written, err)
	}

	_, stderr, code = execute(t, "migrate", "--dir", dir)
	if code != 2 || !strings.Contains(stderr, out+" exists; use --force to overwrite it or --output to write elsewhere") {
		t.Errorf("second run: exit %d\n%s", code, stderr)
	}
	stdout, _, code := execute(t, "migrate", "--dir", dir, "--output", "-")
	if code != 0 || stdout != string(written) {
		t.Errorf("--output -: exit %d\n%s", code, stdout)
	}

	// The migrated file and compatibility mode plan the same changes.
	migrated, _, code := execute(t, "plan", "--dir", dir, "--config", out)
	if code != 0 {
		t.Fatalf("plan --config: exit %d", code)
	}
	compat, _, code := execute(t, "plan", "--dir", dir, "--renovate")
	if code != 0 || compat != migrated || !strings.Contains(compat, "actions/checkout@v7.0.1") {
		t.Errorf("plan --renovate differs from the migrated config:\n%s\n---\n%s", compat, migrated)
	}
}
