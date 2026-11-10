package main

import (
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
)

func TestConfigCheck(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	stdout, stderr, code := execute(t, "config", "check", "--dir", dir)
	if code != 0 || !strings.Contains(stdout, "built-in defaults apply") || stderr != "" {
		t.Fatalf("no file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	writeFiles(t, dir, map[string]string{".deps.yaml": "pin: digest\nrules:\n  - managers: [gomod]\n    group: go\n"})
	stdout, stderr, code = execute(t, "config", "check", "--dir", dir)
	if want := filepath.Join(dir, ".deps.yaml") + ": ok\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("valid: exit %d, stdout %q (want %q), stderr %q", code, stdout, want, stderr)
	}
	for name, src := range map[string]string{
		"unknown key": "pinn: digest\n",
		"bad value":   "rules:\n  - allow: '<<7'\n",
	} {
		writeFiles(t, dir, map[string]string{"bad.yaml": src})
		stdout, stderr, code = execute(t, "config", "check", "--dir", dir, "--config", filepath.Join(dir, "bad.yaml"))
		if code != 2 || stdout != "" || !strings.Contains(stderr, "bad.yaml") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
}

func TestConfigShow(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".deps.yaml": "# a comment\npin: digest\nignorePaths: []\n" +
		"rules:\n  - names: ['k8s.io/**'] # inline\n    commitScope: ''\n" +
		"custom:\n  - files: [Makefile]\n" +
		"customDatasources:\n  envtest:\n    url: https://example.com/v.json\n" +
		"lockFileMaintenance:\n  enabled: true\n"})
	stdout, stderr, code := execute(t, "config", "show", "--dir", dir)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"# effective configuration of " + filepath.Join(dir, ".deps.yaml") + "\n",
		"\nversion: 1\npin: digest\nrangeStrategy: auto\n",
		"\nbuiltins:\n  security: true\n  monorepos: true\n  replacements: true\n",
		"\ngit:\n  author: deps[bot] <deps-bot@noreply.invalid>\n",
		"\nactions:\n  defaultHost: github.com\n",
		"\ngitlabci:\n  url: https://gitlab.com\n",
		"\n  platformCommit: auto\n",
		"\nignorePaths: []\n",
		"\nrules:\n  - names: ['k8s.io/**']\n    commitScope: ''\n",
		"\ncustom:\n  - files: [Makefile]\n    versioning: semver\n",
		"\ncustomDatasources:\n  envtest:\n    url: https://example.com/v.json\n    format: json\n    versions: .releases[].version\n",
		"\nlockFileMaintenance:\n  enabled: true\n  schedule: []\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "a comment") || strings.Contains(stdout, "inline") || strings.Contains(stdout, " # ") {
		t.Errorf("comments without --sources:\n%s", stdout)
	}

	t.Setenv("DEPS_GIT_AUTHOR", "bot <bot@example.com>")
	t.Setenv("DEPS_DEFAULT_ACTIONS_URL", "https://gitea.com")
	t.Setenv("CI_SERVER_URL", "https://gitlab.example.com/")
	t.Setenv("DEPS_PLATFORM_COMMIT", "disabled")
	stdout, _, code = execute(t, "config", "show", "--dir", dir, "--sources")
	if code != 0 {
		t.Fatalf("--sources: exit %d", code)
	}
	for _, want := range []string{
		"\nversion: 1 # default\npin: digest # file\n",
		"\nschedule: [] # default\n",
		"\ngit:\n  author: bot <bot@example.com> # env\n",
		"\nactions:\n  defaultHost: gitea.com # env\n",
		"\ngitlabci:\n  url: https://gitlab.example.com # env\n",
		"\n  platformCommit: disabled # env\n",
		"\nterraform:\n  defaultRegistry: \"\" # default\n",
		"\nignorePaths: [] # file\n",
		"\nrules: # file\n",
		"\nlockFileMaintenance:\n  enabled: true # file\n  schedule: [] # default\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--sources: missing %q in\n%s", want, stdout)
		}
	}
	stdout, _, _ = execute(t, "config", "show", "--dir", dir, "--sources", "--author-email", "me@example.com")
	if want := "\ngit:\n  author: bot <me@example.com> # flag\n"; !strings.Contains(stdout, want) {
		t.Errorf("flag: missing %q in\n%s", want, stdout)
	}

	t.Setenv("DEPS_GIT_AUTHOR", "")
	t.Setenv("DEPS_DEFAULT_ACTIONS_URL", "")
	t.Setenv("CI_SERVER_URL", "")
	t.Setenv("DEPS_PLATFORM_COMMIT", "")
	stdout, _, code = execute(t, "config", "show", "--dir", t.TempDir(), "--sources")
	for _, want := range []string{
		"# effective configuration: built-in defaults (no config file)\n",
		"\nignorePaths: # default\n  - '**/node_modules/**'\n",
		"\nrules: [] # default\n",
	} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("defaults: exit %d, missing %q in\n%s", code, want, stdout)
		}
	}
	if _, _, code := execute(t, "config", "show", "--dir", dir, "--config", filepath.Join(dir, "missing.yaml")); code != 2 {
		t.Errorf("missing --config: exit %d, want 2", code)
	}
	t.Setenv("DEPS_PLATFORM_COMMIT", "true")
	if _, stderr, code := execute(t, "config", "show", "--dir", dir); code != 2 || !strings.Contains(stderr, `DEPS_PLATFORM_COMMIT: must be one of auto, enabled, disabled, got "true"`) {
		t.Errorf("invalid DEPS_PLATFORM_COMMIT: exit %d, stderr %q", code, stderr)
	}
	t.Setenv("DEPS_PLATFORM_COMMIT", "")

	// Anchors, aliases and merge keys are expanded: the output is valid config of its own.
	anchored := t.TempDir()
	writeFiles(t, anchored, map[string]string{".deps.yaml": "rules:\n  - &base {names: [a], group: g, automerge: true}\n" +
		"  - <<: *base\n    group: h\n  - *base\n"})
	stdout, stderr, code = execute(t, "config", "show", "--dir", anchored)
	if code != 0 || strings.Contains(stdout, "&") || strings.Contains(stdout, "*base") || strings.Contains(stdout, "<<") ||
		!strings.Contains(stdout, "\nrules:\n  - names: [a]\n    automerge: true\n    group: g\n  - names: [a]\n    automerge: true\n    group: h\n  - names: [a]\n    automerge: true\n    group: g\n") {
		t.Fatalf("anchors: exit %d, stderr %q\n%s", code, stderr, stdout)
	}
	shown := t.TempDir()
	writeFiles(t, shown, map[string]string{".deps.yaml": stdout})
	if _, stderr, code := execute(t, "config", "check", "--dir", shown); code != 0 {
		t.Errorf("config show output does not check: exit %d, stderr %q", code, stderr)
	}
}

func TestConfigDescribe(t *testing.T) {
	cleanEnv(t)
	s, err := reflectConfig()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := docgen.ConfigKeys(s)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := execute(t, "config", "describe")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != 0 || stderr != "" || len(lines) != len(keys) {
		t.Fatalf("list: exit %d, %d lines for %d keys, stderr %q", code, len(lines), len(keys), stderr)
	}
	for _, want := range []string{"pin ", "actions.defaultHost ", "rules[].allow ", "custom[].checksum.asset ", "customDatasources.<name>.url "} {
		if !strings.Contains(stdout, "\n"+want) && !strings.HasPrefix(stdout, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	if !strings.Contains(stdout, "Default pin mode for actions and docker\n") {
		t.Errorf("list summary of pin not shortened:\n%s", stdout)
	}

	want, _, code := execute(t, "config", "describe", "rules[].allow")
	if code != 0 || !strings.HasPrefix(want, "rules[].allow\n\nType:  string\n\nNew versions must satisfy") ||
		!strings.Contains(want, "- A constraint (< 7, ~1.2") || strings.ContainsAny(want, "`*") {
		t.Fatalf("rules[].allow: exit %d\n%s", code, want)
	}
	for _, alias := range []string{"rules.allow", "rules[2].allow"} {
		if got, _, code := execute(t, "config", "describe", alias); code != 0 || got != want {
			t.Errorf("%s: exit %d, differs from rules[].allow:\n%s", alias, code, got)
		}
	}
	got, _, code := execute(t, "config", "describe", "customDatasources.envtest.format")
	if code != 0 || !strings.Contains(got, "Type:     string\nDefault:  json\nValues:   json, yaml, plain\n") {
		t.Errorf("customDatasources.envtest.format: exit %d\n%s", code, got)
	}

	for q, hint := range map[string]string{
		"allow":      "did you mean rules[].allow?",
		"rules.alow": "did you mean rules[].allow?",
		"pinn":       "did you mean pin?",
		"bogus":      `unknown config key "bogus" (deps config describe lists every key)`,
	} {
		stdout, stderr, code := execute(t, "config", "describe", q)
		if code != 2 || stdout != "" || !strings.Contains(stderr, hint) {
			t.Errorf("%s: exit %d, stderr %q, want %q", q, code, stderr, hint)
		}
	}
	if _, _, code := execute(t, "config", "describe", "a", "b"); code != 2 {
		t.Errorf("two keys: exit %d, want 2", code)
	}
}

func TestShortSummary(t *testing.T) {
	for in, want := range map[string]string{
		"Config format version; must be 1 if set.":                     "Config format version",
		"With separateMajorMinor: one PR per newer major version.":     "With separateMajorMinor: one PR per newer major version",
		"Look up OSV advisories, e.g. on osv.dev. Then more.":          "Look up OSV advisories, e.g. on osv.dev",
		"How versions (1.+, [1.0,2.0)) are updated: update-lockfile x": "How versions (1.+, [1.0,2.0)) are updated",
		"Only match deps (a; b) of this kind; rest":                    "Only match deps (a; b) of this kind",
	} {
		if got := shortSummary(in); got != want {
			t.Errorf("shortSummary(%q) = %q, want %q", in, got, want)
		}
	}
}
