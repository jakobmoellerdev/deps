package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

func TestInit(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4.1.0\n      - uses: actions/setup-go@v5\n",
		"go.mod":                   "module example.com/x\n\ngo 1.24\n\nrequire example.com/a v1.0.0\n",
		"test/go.mod":              "module example.com/t\n\nrequire example.com/b v1.0.0\n", // default ignorePaths
	})
	stdout, stderr, code := execute(t, "init", "--dir", dir)
	target := filepath.Join(dir, ".deps.yaml")
	if code != 0 || stdout != "wrote "+target+"\n" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "# yaml-language-server: $schema=" + schemaURL(version) + "\n" +
		"# Written by `deps init`; `deps config describe` documents every key.\n" +
		"#\n" +
		"# Detected managers (offline scan):\n" +
		"#   github-actions  1 file, 2 dependencies\n" +
		"#   gomod           1 file, 2 dependencies\n" +
		"version: 1\n"
	if string(got) != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}

	_, stderr, code = execute(t, "init", "--dir", dir)
	if code != 1 || !strings.Contains(stderr, target+" exists; pass --force to overwrite it") {
		t.Errorf("existing file: exit %d, stderr %q", code, stderr)
	}
	if err := os.Rename(target, filepath.Join(dir, ".deps.yml")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code = execute(t, "init", "--dir", dir); code != 1 || !strings.Contains(stderr, ".deps.yml exists") {
		t.Errorf("existing .deps.yml: exit %d, stderr %q", code, stderr)
	}
	if stdout, _, code = execute(t, "init", "--dir", dir, "--force"); code != 0 || !strings.HasSuffix(stdout, ".deps.yml\n") {
		t.Errorf("--force: exit %d, stdout %q (want the existing .deps.yml rewritten)", code, stdout)
	}

	empty := t.TempDir()
	other := filepath.Join(empty, "conf", "deps.yaml")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code = execute(t, "init", "--dir", empty, "--config", other); code != 0 {
		t.Fatalf("--config: exit %d, stderr %q", code, stderr)
	}
	got, _ = os.ReadFile(other)
	if !strings.Contains(string(got), "# No dependency files detected") {
		t.Errorf("--config file:\n%s", got)
	}

	broken := t.TempDir()
	writeFiles(t, broken, map[string]string{"package.json": "{"})
	if _, stderr, code = execute(t, "init", "--dir", broken); code != 0 || !strings.HasPrefix(stderr, "warn    package.json: ") {
		t.Errorf("extraction error: exit %d, stderr %q", code, stderr)
	}
}

// TestInitFromRenovate checks that --from-renovate writes the starter file around deps
// migrate's translation (internal/renovate, tested there) and prints its warnings.
func TestInitFromRenovate(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"renovate.json5": `{
  // JSON5, as Renovate reads it
  extends: ["config:recommended"],
  ignorePaths: ["**/testdata/**"],
  prCreation: "not-pending",
  abandonmentThreshold: "2 years",
  packageRules: [
    {matchManagers: ["gomod", "composer"], matchUpdateTypes: ["minor", "patch"], groupName: "minor", draftPR: true},
    {matchManagers: ["gomod"], allowedVersions: "< 2", prPriority: 3},
  ],
}
`,
		"go.mod":          "module example.com/x\n\ngo 1.24\n\nrequire example.com/a v1.0.0\n",
		"testdata/go.mod": "module example.com/t\n\nrequire example.com/b v1.0.0\n", // the translated ignorePaths
	})
	stdout, stderr, code := execute(t, "init", "--dir", dir, "--from-renovate", filepath.Join(dir, "renovate.json5"))
	if code != 0 || !strings.HasPrefix(stdout, "wrote ") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "warn    branchPrefix: deps run closes open PRs under renovate/") {
		t.Errorf("stderr lacks the translation warning:\n%s", stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".deps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"# yaml-language-server: $schema=" + schemaURL(version) + "\n# Written by `deps init`",
		"#   gomod  1 file, 2 dependencies\n#\n# Translated from renovate.json5 by deps (Renovate compatibility).\n",
		"\nversion: 1\nbranchPrefix: renovate/\n",
		"prCreation: not-pending\n",
		"abandonmentThreshold: 2 years\n",
		"managers: [gomod, composer]",
		"draftPR: true",
	} {
		if !strings.Contains(string(got), w) {
			t.Errorf("file lacks %q:\n%s", w, got)
		}
	}
	if _, err := config.Load(t.Context(), dir, "", nil); err != nil {
		t.Errorf("written config does not load: %v", err)
	}
	_, stderr, code = execute(t, "init", "--dir", t.TempDir(), "--from-renovate", filepath.Join(dir, "nope.json"))
	if code != 2 || !strings.Contains(stderr, "nope.json") {
		t.Errorf("missing file: exit %d, stderr %q", code, stderr)
	}
}
