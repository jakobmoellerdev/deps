package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// generateDocs runs the hidden docs command into a temp dir and returns it.
func generateDocs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, stderr, code := execute(t, "docs", "--out", filepath.Join(dir, "reference"),
		"--schema", filepath.Join(dir, "deps.schema.json"), "--action", filepath.Join("..", "..", "action.yml"))
	if code != 0 {
		t.Fatalf("docs: exit %d\n%s", code, stderr)
	}
	return dir
}

func TestDocs(t *testing.T) {
	cleanEnv(t)
	dir := generateDocs(t)
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	schema, _, _ := execute(t, "schema")
	if got := read("deps.schema.json"); got != schema {
		t.Error("deps.schema.json differs from `deps schema`")
	}
	for _, f := range []string{"configuration.md", "cli.md", "action.md", "managers.md"} {
		if !strings.HasPrefix(read(filepath.Join("reference", f)), docgen.Header) {
			t.Errorf("%s lacks the generated header", f)
		}
	}
	cfg := read("reference/configuration.md")
	for _, k := range []string{"version", "pin", "schedule", "rules", "custom", "customDatasources"} {
		if !strings.Contains(cfg, "\n## `"+k+"`\n") {
			t.Errorf("configuration.md lacks key %s", k)
		}
	}
	for _, k := range []string{"rules[].allow", "custom[].checksum.url", "customDatasources.<name>.versions", "actions.defaultHost"} {
		if !strings.Contains(cfg, "`"+k+"`\n") {
			t.Errorf("configuration.md lacks key %s", k)
		}
	}
	cli := read("reference/cli.md")
	for _, want := range []string{"## deps run\n", "| `--trigger-schedule` |", "| `--log-level` | string | `\"warn\"` |", "## Environment\n", "`DEPS_TRIGGER_SCHEDULE`", "## Exit codes\n", "## Output\n", "`skipped: outside schedule`"} {
		if !strings.Contains(cli, want) {
			t.Errorf("cli.md lacks %q", want)
		}
	}
	if strings.Contains(cli, "## deps docs") || strings.Contains(cli, "`--cpuprofile` |") {
		t.Error("cli.md documents the hidden docs command or hidden flags")
	}
	if action := read("reference/action.md"); !strings.Contains(action, "| `token` | no | `${{ github.token }}` |") {
		t.Errorf("action.md lacks the token input:\n%s", action)
	}
	if m := read("reference/managers.md"); !strings.Contains(m, "## github-actions\n") || !strings.Contains(m, "| `custom.<name>` |") {
		t.Error("managers.md lacks a manager or datasource")
	}

	if _, stderr, code := execute(t, "docs", "--out", t.TempDir(), "--action", filepath.Join(t.TempDir(), "missing.yml")); code != 1 || !strings.Contains(stderr, "missing.yml") {
		t.Errorf("missing action.yml: exit %d\n%s", code, stderr)
	}
}

var (
	linkRe    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	headingRe = regexp.MustCompile(`(?m)^#{1,6} (.+)$`)
)

// TestDocsLinks checks that every relative link of the generated reference points to an
// existing file and heading anchor, in the generated files or in the hand-written docs.
func TestDocsLinks(t *testing.T) {
	cleanEnv(t)
	dir := generateDocs(t)
	ref := filepath.Join(dir, "reference")
	docs := filepath.Join("..", "..", "docs")
	anchors := map[string]map[string]bool{}
	anchorsOf := func(file string) map[string]bool {
		if a, ok := anchors[file]; ok {
			return a
		}
		b, err := os.ReadFile(file)
		if err != nil {
			anchors[file] = nil
			return nil
		}
		a := map[string]bool{}
		for _, m := range headingRe.FindAllStringSubmatch(withoutFences(string(b)), -1) {
			a[docgen.Anchor(m[1])] = true
		}
		anchors[file] = a
		return a
	}
	entries, err := os.ReadDir(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		src := filepath.Join(ref, e.Name())
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range linkRe.FindAllStringSubmatch(withoutFences(string(b)), -1) {
			link := m[1]
			if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
				continue
			}
			target, anchor, _ := strings.Cut(link, "#")
			file := src
			switch {
			case target == "":
			case strings.HasPrefix(target, "../"):
				file = filepath.Join(docs, filepath.FromSlash(path.Clean(target[3:])))
			default:
				file = filepath.Join(ref, filepath.FromSlash(target))
			}
			a := anchorsOf(file)
			if a == nil {
				t.Errorf("%s: link %s: no such file", e.Name(), link)
				continue
			}
			if anchor != "" && !a[anchor] {
				t.Errorf("%s: link %s: no heading #%s", e.Name(), link, anchor)
			}
		}
	}
}

// withoutFences drops fenced code blocks, whose lines are neither headings nor links.
func withoutFences(s string) string {
	var b strings.Builder
	fence := false
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			continue
		}
		if !fence {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// The datasource reference documents exactly the datasources a session registers.
func TestDatasourceDocsMatchSession(t *testing.T) {
	cleanEnv(t)
	s, err := newSession(&rootOpts{dir: t.TempDir(), concurrency: 1}, platform.Info{}, "", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	var registered, documented []string
	for name := range s.sources {
		registered = append(registered, name)
	}
	registered = append(registered, config.CustomDatasourcePrefix+"<name>")
	for _, d := range datasourceDocs() {
		documented = append(documented, d.Name)
	}
	slices.Sort(registered)
	slices.Sort(documented)
	if !slices.Equal(registered, documented) {
		t.Errorf("documented datasources %v, registered %v", documented, registered)
	}
}

func TestHelpTopics(t *testing.T) {
	stdout, _, code := execute(t, "help", "environment")
	if code != 0 || !strings.Contains(stdout, "DEPS_GITHUB_COM_TOKEN") {
		t.Errorf("help environment: exit %d\n%s", code, stdout)
	}
	stdout, _, _ = execute(t, "--help")
	if !strings.Contains(stdout, "Additional help topics:") || !strings.Contains(stdout, "exit-codes") || strings.Contains(stdout, " docs ") {
		t.Errorf("root help:\n%s", stdout)
	}
	stdout, _, _ = execute(t, "run", "--help")
	if !strings.Contains(stdout, "skipped: branch modified by someone else") || !strings.Contains(stdout, "deps run --dry-run -v") {
		t.Errorf("run help:\n%s", stdout)
	}
}
