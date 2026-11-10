package main

import (
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

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
	for _, f := range []string{"configuration.md", "cli.md", "action.md", "managers.md", "renovate-options.md"} {
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
	cliRef := read("reference/cli.md")
	for _, want := range []string{"## deps run\n", "| `--trigger-schedule` |", "| `--log-level` | string | `\"\"` |", "## Environment\n", "`DEPS_TRIGGER_SCHEDULE`", "## Exit codes\n", "## Output\n", "`skipped: outside schedule`"} {
		if !strings.Contains(cliRef, want) {
			t.Errorf("cli.md lacks %q", want)
		}
	}
	if strings.Contains(cliRef, "## deps docs") || strings.Contains(cliRef, "`--cpuprofile` |") {
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
	s, err := newSession(t.Context(), &rootOpts{dir: t.TempDir(), concurrency: 1}, platform.Info{}, "", http.DefaultClient, io.Discard)
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
	if code != 0 || !strings.Contains(stdout, "DEPS_GITHUB_COM_TOKEN") ||
		!strings.Contains(stdout, "| `DEPS_CONCURRENCY` | `apply`, `explain`, `migrate`, `plan`, `preview`, `run`, `submit` | default of `--concurrency`: ") ||
		!strings.Contains(stdout, "| `DEPS_LOG_LEVEL` | all | ") || strings.Count(stdout, "| `DEPS_TOKEN`") != 1 {
		t.Errorf("help environment: exit %d\n%s", code, stdout)
	}
	stdout, _, _ = execute(t, "--help")
	if !strings.Contains(stdout, "Additional help topics:") || !strings.Contains(stdout, "exit-codes") || strings.Contains(stdout, " docs ") {
		t.Errorf("root help:\n%s", stdout)
	}
	stdout, _, _ = execute(t, "run", "--help")
	if !strings.Contains(stdout, "skipped: branch modified by someone else") || !strings.Contains(stdout, "deps run --dry-run --show-skipped") {
		t.Errorf("run help:\n%s", stdout)
	}
}

// Help keeps its layout: usage, examples, local flags with -h and the inherited global flags.
func TestHelpLayout(t *testing.T) {
	cleanEnv(t) // a CI runner's GITHUB_ACTIONS adds an ::error annotation to the unknown-command stderr
	for _, args := range [][]string{{"explain", "--help"}, {"help", "explain"}, {"explain", "-h"}} {
		stdout, _, code := execute(t, args...)
		for _, want := range []string{
			"\n\nUsage:\n  deps explain <name-glob>... [flags]\n\nExamples:\n  deps explain 'actions/*'\n",
			"\n\nFlags:\n      --base string       explain base branch <base>",
			"\n      --concurrency int",
			"\n      --dir string        repository directory to scan (default \".\")\n  -h, --help              help for explain\n      --renovate          compatibility mode: ",
			"\n\nGlobal Flags:\n",
			"\n      --log-level string    log level: debug, info, warn, error (when not set: debug with $RUNNER_DEBUG=1, else warn)\n",
		} {
			if code != 0 || !strings.Contains(stdout, want) {
				t.Errorf("%v: exit %d, lacks %q:\n%s", args, code, want, stdout)
			}
		}
	}
	stdout, _, code := execute(t, "help", "nope")
	if code != 0 || !strings.HasPrefix(stdout, "Unknown help topic [`nope`]\n") {
		t.Errorf("help nope: exit %d\n%s", code, stdout)
	}
	if _, stderr, code := execute(t, "nope"); code != 1 || stderr != "deps: unknown command \"nope\" for \"deps\"\n" {
		t.Errorf("unknown command: exit %d %q", code, stderr)
	}
}

// packaging/common.sh installs the output of `deps completion <shell>`.
func TestCompletion(t *testing.T) {
	for shell, want := range map[string]string{
		"bash":       "complete -o bashdefault -o default -F __deps_bash_autocomplete deps\n",
		"zsh":        "#compdef deps\n",
		"fish":       "complete -c deps -f -a '(__deps_perform_completion)'\n",
		"powershell": "Register-ArgumentCompleter",
		"pwsh":       "Register-ArgumentCompleter",
	} {
		if stdout, stderr, code := execute(t, "completion", shell); code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("completion %s: exit %d, lacks %q\n%s%s", shell, code, want, stdout, stderr)
		}
	}
	if stdout, _, _ := execute(t, "--help"); !strings.Contains(stdout, "\n  completion  Generate the autocompletion script for the specified shell\n") {
		t.Errorf("root help lacks completion:\n%s", stdout)
	}
	// The scripts ask deps itself: flags of plan (and the global ones) starting with --co.
	if stdout, _, _ := execute(t, "plan", "--co", "--generate-shell-completion"); !strings.HasPrefix(stdout, "--config:") ||
		!strings.Contains(stdout, "\n--color:") || !strings.Contains(stdout, "\n--concurrency:") || strings.Contains(stdout, "--dir") {
		t.Errorf("flag completion:\n%s", stdout)
	}
}

// TestHelpFiles checks that every command and help topic has a help file and every help file a
// command: newRoot leaves Description to fillHelp.
func TestHelpFiles(t *testing.T) {
	root := newRoot()
	if root.Description != "" {
		t.Errorf("newRoot filled Description: %q", root.Description)
	}
	fillAllHelp(root)
	used := map[string]bool{}
	var walk func(c *cli.Command, name string)
	walk = func(c *cli.Command, name string) {
		used[name+".md"] = true
		if c.Description == "" && !c.Hidden {
			t.Errorf("%s: no help/%s.md", c.Name, name)
		}
		for _, sub := range c.Commands {
			n := sub.Name
			if c != root {
				n = name + "-" + sub.Name
			}
			walk(sub, n)
		}
	}
	walk(root, "root")
	files, err := helpFS.ReadDir("help")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !used[f.Name()] {
			t.Errorf("help/%s belongs to no command", f.Name())
		}
	}
}
