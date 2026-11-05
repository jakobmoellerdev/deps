package actions

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

var env = manager.Env{DefaultHost: "github.com"}

const sha = "1af3b93b6815bc44a9784bd300feb67ff0d1eeb3"

const fixture = `on: push
jobs:
  reuse:
    uses: org/wf/.github/workflows/x.yml@v1.2.0
  a:
    runs-on: x
    steps:
      - uses: actions/checkout@v6.0.0
      - uses: 'actions/setup-go@v5'
      - uses: "actions/cache@v4.1"
      - uses: actions/checkout@` + sha + ` # v7.0.1
      - uses: actions/checkout@` + sha + ` # ratchet:actions/checkout@v4
      - uses: actions/checkout@` + sha + `
      - uses: ./local
      - uses: $/x/y@v1
      - uses: docker://alpine:3
      - uses: actions/checkout@main
      - uses: github/codeql-action/init@v3
      - uses: https://gitea.com/actions/checkout@v4
      - with:
          uses: not/extracted@v1
        uses: a/b@v2
      - uses: 'q/q@v9' # v1
  p:
    steps:
      - parallel:
          - uses: par/allel@v3
`

func TestExtract(t *testing.T) {
	for name, src := range map[string]string{"lf": fixture, "crlf": strings.ReplaceAll(fixture, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			b := []byte(src)
			deps, err := New().Extract(".github/workflows/ci.yml", b, env)
			if err != nil {
				t.Fatal(err)
			}
			type want struct {
				name, source, version, digest, ref, vspan, skip, afterInsert string
			}
			wants := []want{
				{"org/wf/.github/workflows/x.yml", "https://github.com/org/wf", "v1.2.0", "", "v1.2.0", "", "", ""},
				{"actions/checkout", "https://github.com/actions/checkout", "v6.0.0", "", "v6.0.0", "", "", ""},
				{"actions/setup-go", "https://github.com/actions/setup-go", "v5", "", "v5", "", "", ""},
				{"actions/cache", "https://github.com/actions/cache", "v4.1", "", "v4.1", "", "", ""},
				{"actions/checkout", "https://github.com/actions/checkout", "v7.0.1", sha, sha, "v7.0.1", "", " # v7.0.1"},
				{"actions/checkout", "https://github.com/actions/checkout", "v4", sha, sha, "v4", "", " # ratchet"},
				{"actions/checkout", "https://github.com/actions/checkout", "", sha, sha, "", "", ""},
				{"./local", "", "", "", "", "", "local action", ""},
				{"$/x/y@v1", "", "", "", "", "", "local action", ""},
				{"docker://alpine:3", "", "", "", "", "", "docker reference (docker manager)", ""},
				{"actions/checkout", "https://github.com/actions/checkout", "", "", "main", "", "non-version ref", ""},
				{"github/codeql-action/init", "https://github.com/github/codeql-action", "v3", "", "v3", "", "", ""},
				{"actions/checkout", "https://gitea.com/actions/checkout", "v4", "", "v4", "", "", ""},
				{"a/b", "https://github.com/a/b", "v2", "", "v2", "", "", ""},
				{"q/q", "https://github.com/q/q", "v9", "", "v9", "", "", " # v1"},
				{"par/allel", "https://github.com/par/allel", "v3", "", "v3", "", "", ""},
			}
			if len(deps) != len(wants) {
				t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
			}
			for i, w := range wants {
				d := deps[i]
				if d.Manager != Name || d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip ||
					d.Datasource != "git-tags" || d.Versioning != "tag" || d.DepType != "action" {
					t.Errorf("%d: got %+v, want %+v", i, d, w)
				}
				if w.skip == "" || w.skip == "non-version ref" {
					if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ref {
						t.Errorf("%d: ref span %q, want %q", i, got, w.ref)
					}
				}
				if w.vspan != "" {
					if got := string(b[d.VersionSpan.Start:d.VersionSpan.End]); got != w.vspan {
						t.Errorf("%d: version span %q, want %q", i, got, w.vspan)
					}
				}
				if w.afterInsert != "" && !strings.HasPrefix(string(b[d.InsertAt:]), w.afterInsert) {
					t.Errorf("%d: after InsertAt %q", i, b[d.InsertAt:d.InsertAt+10])
				}
			}
			// quoted: InsertAt is past the closing quote
			if c := b[deps[2].InsertAt-1]; c != '\'' {
				t.Errorf("single-quoted InsertAt after %q", c)
			}
			if c := b[deps[3].InsertAt-1]; c != '"' {
				t.Errorf("double-quoted InsertAt after %q", c)
			}
			if deps[1].Line != 8 {
				t.Errorf("line = %d", deps[1].Line)
			}
		})
	}
}

func TestExtractParseError(t *testing.T) {
	if _, err := New().Extract("a.yml", []byte("a: [\n"), env); err == nil {
		t.Fatal("want error")
	}
}

func TestExtractSkips(t *testing.T) {
	src := "steps:\n" +
		"  - uses: a/b@" + sha + " # ratchet:exclude\n" +
		"  - uses: a/b@" + sha + " # main\n" +
		"  - uses: a/b@" + strings.Repeat("g", 40) + "\n" +
		"  - uses:\n      a/c@v1\n" +
		"  - uses: \"a/d\\x40v1\"\n"
	deps, err := New().Extract("action.yml", []byte(src), env)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, skip string
		line       int
	}{
		{"a/b", "pinned without version comment", 2},
		{"a/b", "pinned without version comment", 3},
		{"a/b", "non-version ref", 4},
		{"a/c", "cannot locate value", 6}, // value on a continuation line
		{"a/d", "cannot locate value", 7}, // escaped in the source
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps: %+v", len(deps), deps)
	}
	for i, w := range want {
		if d := deps[i]; d.Name != w.name || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("%d: got %+v, want %+v", i, d, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		".github/workflows/ci.yml":     true,
		".gitea/workflows/x.yaml":      true,
		".forgejo/actions/a/b.yml":     true,
		"sub/.github/workflows/ci.yml": true,
		"workflow-templates/t.yml":     true,
		"action.yml":                   true,
		"x/action.yaml":                true,
		"myaction.yml":                 false,
		".github/dependabot.yml":       false,
		".github/workflows/README.md":  false,
	} {
		if New().Match(p) != want {
			t.Errorf("Match(%q) != %v", p, want)
		}
	}
}

func TestEdits(t *testing.T) {
	src := "- uses: a/b@v1 # c\n- uses: 'a/c@" + sha + "' # v1.0.0\n- uses: \"a/d@v2\"\n"
	deps, err := New().Extract("action.yml", []byte(src), env)
	if err != nil || len(deps) != 3 {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	sha2 := strings.Repeat("b", 40)
	tests := []struct {
		u    dep.Update
		want string
	}{
		{dep.Update{Dep: deps[0], NewVersion: "v2"}, "- uses: a/b@v2 # c\n"},
		{dep.Update{Dep: deps[1], NewVersion: "v1.1.0", NewDigest: sha2}, "- uses: 'a/c@" + sha2 + "' # v1.1.0\n"},
		{dep.Update{Dep: deps[1], NewVersion: "v1.0.0", NewDigest: sha2}, "- uses: 'a/c@" + sha2 + "' # v1.0.0\n"},
		{dep.Update{Dep: deps[2], NewVersion: "v3", NewDigest: sha2}, "- uses: \"a/d@" + sha2 + "\" # v3\n"},
	}
	lines := strings.SplitAfter(src, "\n")
	for i, tt := range tests {
		edits, err := New().Edits(tt.u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatal(err)
		}
		l := tt.u.Dep.Line - 1
		if got := strings.SplitAfter(string(out), "\n")[l]; got != tt.want {
			t.Errorf("%d: got %q, want %q (was %q)", i, got, tt.want, lines[l])
		}
	}
	if _, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "v2"}); err == nil {
		t.Error("unpinning: want error")
	}
	if New().Name() != Name {
		t.Error("Name")
	}
}

// Bare commit pins (openclaw's `useblacksmith/begin-testbox@<sha>`): the engine either resolves
// the commit to a tag's version, which is then written as a comment, or moves it to the
// default branch head without a version.
func TestEditsCommitPin(t *testing.T) {
	const pin = "233448af4bfdc6fca509a7f0974411ac6d8a8043"
	src := "jobs:\n" +
		"  testbox:\n" +
		"    steps:\n" +
		"      - name: Begin Testbox\n" +
		"        uses: useblacksmith/begin-testbox@" + pin + "\n" +
		"      - uses: 'actions/create-github-app-token@" + pin + "'\n" +
		"  e2e:\n" +
		"    uses: openclaw/openclaw-windows-node/.github/workflows/release-candidate-e2e.yml@" + pin + "\n"
	deps, err := New().Extract(".github/workflows/ci.yml", []byte(src), env)
	if err != nil || len(deps) != 3 {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	for i, d := range deps {
		if d.Skip != "" || d.Version != "" || d.Digest != pin || d.VersionSpan != (dep.Span{}) || d.InputSpan != (dep.Span{}) {
			t.Fatalf("%d: %+v", i, d)
		}
	}
	sha2 := strings.Repeat("b", 40)
	tagged := deps[0]
	tagged.Version = "v0.4.1" // resolved by the engine from the tag at the commit
	quoted := deps[1]
	quoted.Version = "v2.1.0"
	tests := []struct {
		u    dep.Update
		want string
	}{
		{dep.Update{Dep: tagged, Type: dep.Minor, NewVersion: "v0.5.0", NewDigest: sha2}, "        uses: useblacksmith/begin-testbox@" + sha2 + " # v0.5.0\n"},
		{dep.Update{Dep: tagged, Type: dep.Digest, NewVersion: "v0.4.1", NewDigest: sha2}, "        uses: useblacksmith/begin-testbox@" + sha2 + " # v0.4.1\n"},
		{dep.Update{Dep: quoted, Type: dep.Major, NewVersion: "v3.0.0", NewDigest: sha2}, "      - uses: 'actions/create-github-app-token@" + sha2 + "' # v3.0.0\n"},
		{dep.Update{Dep: deps[0], Type: dep.Digest, NewDigest: sha2}, "        uses: useblacksmith/begin-testbox@" + sha2 + "\n"},
		{dep.Update{Dep: deps[2], Type: dep.Digest, NewDigest: sha2}, "    uses: openclaw/openclaw-windows-node/.github/workflows/release-candidate-e2e.yml@" + sha2 + "\n"},
	}
	for i, tt := range tests {
		edits, err := New().Edits(tt.u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := strings.SplitAfter(string(out), "\n")[tt.u.Dep.Line-1]; got != tt.want {
			t.Errorf("%d: got %q, want %q", i, got, tt.want)
		}
	}
}

// Per-action tag prefixes of monorepo actions (grafana/shared-workflows tags
// actions/get-vault-secrets `get-vault-secrets/v1.3.1`) are not part of the version and are
// kept by edits; other prefixes are not versions.
func TestEditsTagPrefix(t *testing.T) {
	src := "- uses: o/r/actions/x@x/v1.0.2\n" +
		"- uses: o/r/actions/x@" + sha + " # x/v1.0.2\n" +
		"- uses: 'o/r/actions/x@actions/x/v1'\n" +
		"- uses: o/r/actions/x@y/v1\n"
	deps, err := New().Extract("action.yml", []byte(src), env)
	if err != nil || len(deps) != 4 {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	for i, w := range []struct{ version, skip string }{{"v1.0.2", ""}, {"v1.0.2", ""}, {"v1", ""}, {"", "non-version ref"}} {
		if d := deps[i]; d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: version %q skip %q, want %q %q", i, d.Version, d.Skip, w.version, w.skip)
		}
	}
	sha2 := strings.Repeat("b", 40)
	tests := []struct {
		u    dep.Update
		want string
	}{
		{dep.Update{Dep: deps[0], NewVersion: "v1.0.4"}, "- uses: o/r/actions/x@x/v1.0.4\n"},
		{dep.Update{Dep: deps[0], NewVersion: "v1.0.4", NewDigest: sha2}, "- uses: o/r/actions/x@" + sha2 + " # x/v1.0.4\n"},
		{dep.Update{Dep: deps[1], NewVersion: "v1.0.4", NewDigest: sha2}, "- uses: o/r/actions/x@" + sha2 + " # x/v1.0.4\n"},
		{dep.Update{Dep: deps[2], NewVersion: "v2"}, "- uses: 'o/r/actions/x@actions/x/v2'\n"},
		{dep.Update{Dep: deps[2], NewVersion: "v2", NewDigest: sha2}, "- uses: 'o/r/actions/x@" + sha2 + "' # actions/x/v2\n"},
	}
	for i, tt := range tests {
		edits, err := New().Edits(tt.u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := strings.SplitAfter(string(out), "\n")[tt.u.Dep.Line-1]; got != tt.want {
			t.Errorf("%d: got %q, want %q", i, got, tt.want)
		}
	}
}

// Replacements rewrite root and subdirectory action names; a per-action tag prefix follows
// the new path in its written form (full subpath or last element).
func TestEditsRename(t *testing.T) {
	src := "- uses: a/b@v1\n" +
		"- uses: o/r/actions/x@v1.0.0\n" +
		"- uses: o/r/actions/x@x/v1.0.2\n" +
		"- uses: o/r/actions/x@" + sha + " # x/v1.0.2\n" +
		"- uses: 'o/r/actions/x@actions/x/v1'\n" +
		"- uses: o/r/x@x/v1\n"
	deps, err := New().Extract("action.yml", []byte(src), env)
	if err != nil || len(deps) != 6 {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	for i, n := range []int{1, 1, 2, 2, 2, 2} {
		if got := len(deps[i].NameParts); got != n {
			t.Errorf("%d: %d name parts %+v, want %d", i, got, deps[i].NameParts, n)
		}
	}
	sha2 := strings.Repeat("b", 40)
	tests := []struct {
		u    dep.Update
		want string
	}{
		{dep.Update{Dep: deps[0], NewVersion: "v2", NewName: "c/d"}, "- uses: c/d@v2\n"},
		{dep.Update{Dep: deps[1], NewVersion: "v1.1.0", NewName: "n/m/y"}, "- uses: n/m/y@v1.1.0\n"},
		{dep.Update{Dep: deps[2], NewVersion: "v1.0.4", NewName: "n/m/actions/y"}, "- uses: n/m/actions/y@y/v1.0.4\n"},
		{dep.Update{Dep: deps[2], NewVersion: "v1.0.4", NewName: "n/m/tools/x"}, "- uses: n/m/tools/x@x/v1.0.4\n"},
		{dep.Update{Dep: deps[2], NewVersion: "v1.0.4", NewDigest: sha2, NewName: "n/m/actions/y"}, "- uses: n/m/actions/y@" + sha2 + " # y/v1.0.4\n"},
		{dep.Update{Dep: deps[3], NewVersion: "v1.0.4", NewDigest: sha2, NewName: "n/m/actions/y"}, "- uses: n/m/actions/y@" + sha2 + " # y/v1.0.4\n"},
		{dep.Update{Dep: deps[4], NewVersion: "v2", NewName: "o/r/tools/y"}, "- uses: 'o/r/tools/y@tools/y/v2'\n"},
		{dep.Update{Dep: deps[4], NewVersion: "v2", NewDigest: sha2, NewName: "o/r/tools/y"}, "- uses: 'o/r/tools/y@" + sha2 + "' # tools/y/v2\n"},
		{dep.Update{Dep: deps[5], NewVersion: "v2", NewName: "o/r/y"}, "- uses: o/r/y@y/v2\n"},
		{dep.Update{Dep: deps[5], NewVersion: "v2", NewName: "o/s"}, "- uses: o/s@v2\n"},
	}
	for i, tt := range tests {
		edits, err := New().Edits(tt.u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := strings.SplitAfter(string(out), "\n")[tt.u.Dep.Line-1]; got != tt.want {
			t.Errorf("%d: got %q, want %q", i, got, tt.want)
		}
	}
}

func TestRename(t *testing.T) {
	src := "- uses: a/b/c@v1\n- uses: o/r/x@x/v1\n- uses: https://gitea.com/a/b@v1\n"
	deps, err := New().Extract("action.yml", []byte(src), env)
	if err != nil || len(deps) != 3 {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	r := New().(manager.Renamer)
	for _, tt := range []struct {
		d                 dep.Dep
		name, wName, wSrc string
	}{
		{deps[0], "n/m/sub/y", "n/m/sub/y", "https://github.com/n/m"},
		{deps[1], "n/m/y", "n/m/y", "https://github.com/n/m"},
		{deps[2], "c/d", "c/d", "https://gitea.com/c/d"},
	} {
		got, err := r.Rename(tt.d, tt.name)
		if err != nil || got.Name != tt.wName || got.Source != tt.wSrc {
			t.Errorf("Rename(%q) = %q %q, %v; want %q %q", tt.name, got.Name, got.Source, err, tt.wName, tt.wSrc)
		}
	}
	for _, tt := range []struct {
		d    dep.Dep
		name string
	}{{deps[0], "n"}, {deps[0], "n/"}, {deps[0], "n/m@v1"}, {deps[1], "n/m/a/y"}} {
		if _, err := r.Rename(tt.d, tt.name); err == nil {
			t.Errorf("Rename(%q): want error", tt.name)
		}
	}
}

func TestTagExtract(t *testing.T) {
	for name, want := range map[string]string{
		"o/r":           "",
		"o/r/x":         `^x/(?<version>.+)$`,
		"o/r/actions/x": `^(?:actions/x|x)/(?<version>.+)$`,
	} {
		if got := TagExtract(name); got != want {
			t.Errorf("TagExtract(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseUses(t *testing.T) {
	for _, tc := range []struct{ in, skip string }{
		{"noat", "unrecognized uses"},
		{"single@v1", "unrecognized uses"},
		{"a/b@", "unrecognized uses"},
		{"a/@v1", "unrecognized uses"},
		{"https:///a/b@v1", "unrecognized uses"},
		{"https://h%zz/a/b@v1", "unrecognized uses"},
	} {
		if got := ParseUses(tc.in, "github.com"); got.Skip != tc.skip {
			t.Errorf("%q: %+v", tc.in, got)
		}
	}
}
