package precommit

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const (
	sha  = "1af3b93b6815bc44a9784bd300feb67ff0d1eeb3"
	sha2 = "2bf3b93b6815bc44a9784bd300feb67ff0d1eeb4"
)

const fixture = `# hooks
repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.5.0  # keep
    hooks:
      - id: trailing-whitespace
  - repo: https://github.com/psf/black.git
    rev: "24.1.0"
  - repo: git@github.com:astral-sh/ruff-pre-commit.git
    rev: '` + sha + `'  # frozen: v0.3.0
  - repo: https://gitlab.com/group/sub/proj
    rev: ` + sha + ` # v1.0.0
  - repo: https://github.com/a/b
    rev: ` + sha + `
  - repo: https://github.com/c/d
    rev: main
  - repo: local
    hooks:
      - id: x
  - repo: meta
    hooks:
      - id: check-hooks-apply
  - repo: https://github.com/e/f
  - repo: file:///tmp/x
    rev: v1
  - repo: https://github.com/rhysd/actionlint
    rev: ` + sha + ` # ShellCheck stdin deadlock fix (#651)
  - repo: https://github.com/lycheeverse/lychee
    rev: ` + sha + `  # frozen: nightly
  - repo: https://github.com/pre-commit/mirrors-prettier
    rev: v4.0.0-alpha.8
  - repo: https://github.com/g/h
    rev: ` + sha + `  # frozen: v4.0.0-alpha.8
  - repo: https://codeberg.org/forgejo/hooks
    rev: v1.2.0
  - repo: https://git.sr.ht/~sircmpwn/hut
    rev: v0.6.0
  - repo: https://git.example.com:8443/team/hooks
    rev: v2.0.0
  - repo: ssh://git@git.example.com:2222/team/hooks
    rev: v2.0.0
  - repo: https://pagure.io/pagure.git
    rev: 5.14
  - repo: ../hooks
    rev: v1
`

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		".pre-commit-config.yaml":     true,
		"sub/.pre-commit-config.yml":  true,
		"pre-commit-config.yaml":      false,
		".pre-commit-config.yaml.bak": false,
		"x.pre-commit-config.yaml":    false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestExtract(t *testing.T) {
	for name, src := range map[string]string{"lf": fixture, "crlf": strings.ReplaceAll(fixture, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			b := []byte(src)
			deps, err := New().Extract(".pre-commit-config.yaml", b, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			type want struct{ name, source, version, versioning, digest, ref, vspan, skip, afterInsert string }
			wants := []want{
				{"pre-commit/pre-commit-hooks", "https://github.com/pre-commit/pre-commit-hooks", "v4.5.0", "tag", "", "v4.5.0", "", "", "  # keep"},
				{"psf/black", "https://github.com/psf/black", "24.1.0", "tag", "", "24.1.0", "", "", ""},
				{"astral-sh/ruff-pre-commit", "https://github.com/astral-sh/ruff-pre-commit", "v0.3.0", "tag", sha, sha, "v0.3.0", "", "  # frozen"},
				{"group/sub/proj", "https://gitlab.com/group/sub/proj", "v1.0.0", "tag", sha, sha, "v1.0.0", "", " # v1.0.0"},
				// A bare SHA is a commit pin: the engine resolves it (config.UnversionedPin).
				{"a/b", "https://github.com/a/b", "", "tag", sha, sha, "", "", ""},
				{"c/d", "https://github.com/c/d", "", "tag", "", "main", "", "non-version ref", ""},
				{"local", "", "", "tag", "", "", "", "local repository", ""},
				{"meta", "", "", "tag", "", "", "", "meta repository", ""},
				{"e/f", "https://github.com/e/f", "", "tag", "", "", "", "no rev", ""},
				{"file:///tmp/x", "", "", "tag", "", "", "", "unrecognized repo", ""},
				{"rhysd/actionlint", "https://github.com/rhysd/actionlint", "", "tag", sha, sha, "", "pinned without version comment", ""},
				{"lycheeverse/lychee", "https://github.com/lycheeverse/lychee", "nightly", "tag", sha, sha, "nightly", "", ""},
				// semver would sort mirrors-prettier's v4.0.0-alpha.3-1 above alpha.8.
				{"pre-commit/mirrors-prettier", "https://github.com/pre-commit/mirrors-prettier", "", "tag", "", "v4.0.0-alpha.8", "", "non-version ref", ""},
				// Frozen at a tag that is no tag version: its digest is kept current.
				{"g/h", "https://github.com/g/h", "v4.0.0-alpha.8", "tag", sha, sha, "v4.0.0-alpha.8", "", ""},
				{"forgejo/hooks", "https://codeberg.org/forgejo/hooks", "v1.2.0", "tag", "", "v1.2.0", "", "", ""},
				{"~sircmpwn/hut", "https://git.sr.ht/~sircmpwn/hut", "v0.6.0", "tag", "", "v0.6.0", "", "", ""},
				{"team/hooks", "https://git.example.com:8443/team/hooks", "v2.0.0", "tag", "", "v2.0.0", "", "", ""},
				{"team/hooks", "https://git.example.com/team/hooks", "v2.0.0", "tag", "", "v2.0.0", "", "", ""},
				{"pagure", "https://pagure.io/pagure", "5.14", "tag", "", "5.14", "", "", ""},
				{"../hooks", "", "", "tag", "", "", "", "unrecognized repo", ""},
			}
			if len(deps) != len(wants) {
				t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
			}
			for i, w := range wants {
				d := deps[i]
				if d.Manager != Name || d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip ||
					d.Datasource != "git-tags" || d.Versioning != w.versioning || d.DepType != DepTypeRepository {
					t.Errorf("dep %d = %+v, want %+v", i, d, w)
					continue
				}
				if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ref {
					t.Errorf("dep %d ref span = %q, want %q", i, got, w.ref)
				}
				if got := string(b[d.VersionSpan.Start:d.VersionSpan.End]); got != w.vspan {
					t.Errorf("dep %d version span = %q, want %q", i, got, w.vspan)
				}
				if w.afterInsert != "" && !strings.HasPrefix(string(b[d.InsertAt:]), w.afterInsert) {
					t.Errorf("dep %d insert at %q", i, b[d.InsertAt:min(d.InsertAt+12, len(b))])
				}
			}
		})
	}
}

func TestExtractEmpty(t *testing.T) {
	for _, src := range []string{"", "repos: []\n", "- a\n", "repos: x\n", "repos: [x, {repo: [a]}]\n"} {
		deps, err := New().Extract(".pre-commit-config.yaml", []byte(src), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("%q: %v %v", src, deps, err)
		}
	}
	if _, err := New().Extract(".pre-commit-config.yaml", []byte("repos: [\n"), manager.Env{}); err == nil {
		t.Error("want yaml error")
	}
}

// TestExtractFlowStyleRev: the rev of a flow mapping is located by its node; searching the
// line for "rev" finds the one inside the repository URL.
func TestExtractFlowStyleRev(t *testing.T) {
	const src = "repos:\n  - {repo: https://github.com/org/review-v1.0, rev: v1.0}\n"
	deps, err := New().Extract(".pre-commit-config.yaml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("got %d deps: %+v", len(deps), deps)
	}
	d := deps[0]
	if d.Name != "org/review-v1.0" || d.Version != "v1.0" || d.Skip != "" {
		t.Fatalf("dep = %+v", d)
	}
	if want := strings.LastIndex(src, "v1.0"); d.RefSpan.Start != want {
		t.Errorf("RefSpan %v, want start %d: %q", d.RefSpan, want, src[d.RefSpan.Start:d.RefSpan.End])
	}
}

func TestEdits(t *testing.T) {
	src := []byte(fixture)
	deps, err := New().Extract(".pre-commit-config.yaml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		u    dep.Update
		old  string
		new  string
	}{
		{"tag", dep.Update{Dep: deps[0], NewVersion: "v4.6.0"}, "rev: v4.5.0  # keep", "rev: v4.6.0  # keep"},
		{"quoted tag", dep.Update{Dep: deps[1], NewVersion: "24.2.0"}, `rev: "24.1.0"`, `rev: "24.2.0"`},
		{"frozen", dep.Update{Dep: deps[2], NewVersion: "v0.4.0", NewDigest: sha2}, "rev: '" + sha + "'  # frozen: v0.3.0", "rev: '" + sha2 + "'  # frozen: v0.4.0"},
		{"frozen digest only", dep.Update{Dep: deps[3], NewVersion: "v1.0.0", NewDigest: sha2}, "rev: " + sha + " # v1.0.0", "rev: " + sha2 + " # v1.0.0"},
		{"pin quoted", dep.Update{Dep: deps[1], NewVersion: "24.1.0", NewDigest: sha2}, `rev: "24.1.0"`, `rev: "` + sha2 + `" # frozen: 24.1.0`},
		// A commit pin the engine resolved to a tag gets the version as pre-commit's comment;
		// one tracking the default branch only its new SHA.
		{"commit pin at a tag", dep.Update{Dep: deps[4], NewVersion: "v2.1.0", NewDigest: sha2}, "b\n    rev: " + sha + "\n", "b\n    rev: " + sha2 + " # frozen: v2.1.0\n"},
		{"commit pin head", dep.Update{Dep: deps[4], Type: dep.Digest, NewDigest: sha2}, "b\n    rev: " + sha + "\n", "b\n    rev: " + sha2 + "\n"},
		{"frozen tag digest", dep.Update{Dep: deps[11], Type: dep.Digest, NewVersion: "nightly", NewDigest: sha2}, "rev: " + sha + "  # frozen: nightly", "rev: " + sha2 + "  # frozen: nightly"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			edits, err := New().Edits(tt.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply(src, edits)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Replace(fixture, tt.old, tt.new, 1); string(out) != want {
				t.Errorf("got:\n%s\nwant:\n%s", out, want)
			}
		})
	}
	if _, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "v1"}); err == nil {
		t.Error("want error unpinning a frozen rev")
	}
}

// additionalFixture holds additional_dependencies as written in real configurations: Renovate's
// documented examples (actionlint's go-shellcheck, mirrors-prettier's plugins, black's
// requests), airflow's flow sequences of quoted specs and scoped npm packages, and the forms
// that are skipped.
const additionalFixture = `repos:
  - repo: https://github.com/rhysd/actionlint
    rev: v1.7.7
    hooks:
      - id: actionlint
        language: golang # means renovate will also update additional_dependencies
        additional_dependencies:
          - "github.com/wasilibs/go-shellcheck/cmd/shellcheck@v0.11.1"
          - mvdan.cc/sh/v3/cmd/shfmt@latest
          - github.com/x/y
          - ./tools@v1.0.0
  - repo: https://github.com/pre-commit/mirrors-prettier
    rev: v3.1.0
    hooks:
      - id: prettier
        language: node
        additional_dependencies:
          - '@trivago/prettier-plugin-sort-imports@^5.2.2'
          - prettier@^3.6.2
          - prettier-plugin-svelte
          - "esbuild@\u0030.21.5"
  - repo: local
    hooks:
      - id: run-skill-eval
        language: node
        language_version: '22.22.0'
        additional_dependencies: ['promptfoo@0.121.17', '@anthropic-ai/claude-agent-sdk@0.3.185']
      - id: check
        language: python
        additional_dependencies: ['PyYAML>=6.0', "rich>=13.6.0", pyyaml, 'black==26.1.0', 'x @ https://e.org/x.whl', '-r req.txt']
      - id: system
        language: system
        additional_dependencies: [ignored==1.0]
      - id: inherited
        additional_dependencies: [ignored==1.0]
  - repo: meta
    hooks:
      - id: check-useless-excludes
        language: python
        additional_dependencies: [ignored==1.0]
`

func TestExtractAdditionalDependencies(t *testing.T) {
	b := []byte(additionalFixture)
	deps, err := New().Extract(".pre-commit-config.yaml", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ depType, name, datasource, version, rng, ref, skip string }
	wants := []want{
		{DepTypeRepository, "rhysd/actionlint", "git-tags", "v1.7.7", "", "v1.7.7", ""},
		{DepTypeGolang, "github.com/wasilibs/go-shellcheck/cmd/shellcheck", "go", "v0.11.1", "", "v0.11.1", ""},
		{DepTypeGolang, "mvdan.cc/sh/v3/cmd/shfmt", "go", "latest", "", "latest", ""},
		{DepTypeGolang, "github.com/x/y", "go", "", "", "", SkipNoVersion},
		{DepTypeGolang, "./tools", "go", "", "", "", SkipModulePath},
		{DepTypeRepository, "pre-commit/mirrors-prettier", "git-tags", "v3.1.0", "", "v3.1.0", ""},
		{DepTypeNode, "@trivago/prettier-plugin-sort-imports", "npm", "5.2.2", "^5.2.2", "^5.2.2", ""},
		{DepTypeNode, "prettier", "npm", "3.6.2", "^3.6.2", "^3.6.2", ""},
		{DepTypeNode, "prettier-plugin-svelte", "npm", "", "", "", SkipNoVersion},
		{DepTypeNode, "esbuild", "npm", "", "", "", skipLocateValue},
		{DepTypeRepository, "local", "git-tags", "", "", "", "local repository"},
		{DepTypeNode, "promptfoo", "npm", "0.121.17", "0.121.17", "0.121.17", ""},
		{DepTypeNode, "@anthropic-ai/claude-agent-sdk", "npm", "0.3.185", "0.3.185", "0.3.185", ""},
		{DepTypePython, "PyYAML", "pypi", "6.0", ">=6.0", ">=6.0", ""},
		{DepTypePython, "rich", "pypi", "13.6.0", ">=13.6.0", ">=13.6.0", ""},
		{DepTypePython, "pyyaml", "pypi", "", "", "", "unconstrained"},
		{DepTypePython, "black", "pypi", "26.1.0", "==26.1.0", "==26.1.0", ""},
		{DepTypePython, "x", "pypi", "", "", "", "url dependency"},
		{DepTypePython, "-r req.txt", "pypi", "", "", "", SkipInvalid},
		{DepTypeRepository, "meta", "git-tags", "", "", "", "meta repository"},
	}
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.DepType != w.depType || d.Name != w.name || d.Datasource != w.datasource || d.Version != w.version || d.Range != w.rng || d.Skip != w.skip {
			t.Errorf("dep %d = %+v, want %+v", i, d, w)
			continue
		}
		if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ref {
			t.Errorf("dep %d ref span = %q, want %q", i, got, w.ref)
		}
	}

	for _, tt := range []struct {
		name     string
		u        dep.Update
		old, new string
	}{
		{"golang", dep.Update{Dep: deps[1], NewVersion: "v0.12.0"}, "shellcheck@v0.11.1", "shellcheck@v0.12.0"},
		{"node scoped", dep.Update{Dep: deps[6], NewVersion: "6.0.0"}, "imports@^5.2.2", "imports@^6.0.0"},
		{"node exact in flow", dep.Update{Dep: deps[11], NewVersion: "0.122.0"}, "'promptfoo@0.121.17'", "'promptfoo@0.122.0'"},
		{"python pin", dep.Update{Dep: deps[16], NewVersion: "26.3.1"}, "'black==26.1.0'", "'black==26.3.1'"},
		{"python range", dep.Update{Dep: deps[14], NewVersion: "15.0.0", RangeStrategy: "replace"}, `"rich>=13.6.0"`, `"rich>=15.0.0"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			edits, err := New().Edits(tt.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply(b, edits)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Replace(additionalFixture, tt.old, tt.new, 1); string(out) != want {
				t.Errorf("got:\n%s\nwant:\n%s", out, want)
			}
		})
	}
}

// The engine matches the ranges of additional_dependencies through the manager
// (manager.RangeUpdater): PEP 440 for python hooks, npm ranges for node hooks.
func TestRanges(t *testing.T) {
	deps, err := New().Extract(".pre-commit-config.yaml", []byte(additionalFixture), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m := New().(manager.RangeUpdater)
	node, py, golang := deps[7], deps[14], deps[1]
	for _, tt := range []struct {
		d    dep.Dep
		v    string
		want bool
	}{
		{node, "3.9.0", true}, {node, "4.0.0", false}, {node, "x", false},
		{py, "14.0.0", true}, {py, "13.0.0", false},
		{golang, "v0.12.0", false},
	} {
		if got := m.InRange(tt.d, tt.v); got != tt.want {
			t.Errorf("InRange(%s, %s) = %v, want %v", tt.d.Name, tt.v, got, tt.want)
		}
	}
	if got, ok := m.NewRange(node, "4.1.0", "widen"); !ok || got != "^3.6.2 || ^4.1.0" {
		t.Errorf("node NewRange = %q, %v", got, ok)
	}
	if got, ok := m.NewRange(py, "15.0.0", "pin"); !ok || got != "==15.0.0" {
		t.Errorf("python NewRange = %q, %v", got, ok)
	}
	if _, ok := m.NewRange(golang, "v0.12.0", "pin"); ok {
		t.Error("golang has no range")
	}
}

// Real configurations: ruff (astral-sh/ruff@614fdf7) and openclaw (openclaw/openclaw@ad419df).
func TestExtractRealWorld(t *testing.T) {
	type want struct{ depType, name, version, skip string }
	for file, wants := range map[string][]want{
		"ruff.yaml": {
			{DepTypeGolang, "github.com/wasilibs/go-shellcheck/cmd/shellcheck", "v0.11.1", ""},
			{DepTypePython, "mdformat-mkdocs", "5.3.0", ""},
			{DepTypePython, "mdformat-footnote", "0.1.3", ""},
			{DepTypeRepository, "astral-sh/uv-pre-commit", "0.13.7", ""},
		},
		"openclaw.yaml": {
			{DepTypeRepository, "rhysd/actionlint", "", "pinned without version comment"},
			{DepTypeRepository, "zizmorcore/zizmor-pre-commit", "v1.31.1", ""},
			{DepTypePython, "pytest", "9", ""},
		},
	} {
		src, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		deps, err := New().Extract(".pre-commit-config.yaml", src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			found := false
			for _, d := range deps {
				if d.DepType == w.depType && d.Name == w.name {
					found = true
					if d.Version != w.version || d.Skip != w.skip {
						t.Errorf("%s: %s = %+v, want %+v", file, w.name, d, w)
					}
				}
			}
			if !found {
				t.Errorf("%s: %s not extracted", file, w.name)
			}
		}
	}
}
