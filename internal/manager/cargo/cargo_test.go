package cargo

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// describe renders deps one per line as `<depType> <name> <line> <version> <versioning|skip>`,
// failing when a RefSpan does not cover exactly the version.
func describe(t *testing.T, src []byte, deps []dep.Dep) string {
	t.Helper()
	var b strings.Builder
	for _, d := range deps {
		if d.Manager != Name || d.File != "Cargo.toml" {
			t.Errorf("%s: manager %q, file %q", d.Name, d.Manager, d.File)
		}
		if d.Skip != "" {
			fmt.Fprintf(&b, "%s %s %d skip:%s\n", d.DepType, d.Name, d.Line, d.Skip)
			continue
		}
		if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
			t.Errorf("%s: span %q, version %q", d.Name, got, d.Version)
		}
		fmt.Fprintf(&b, "%s %s %d %s %s/%s\n", d.DepType, d.Name, d.Line, d.Version, d.Datasource, d.Versioning)
	}
	return b.String()
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("Cargo.toml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

// Real-world manifests (testdata, copied verbatim):
//   - ripgrep.Cargo.toml: github.com/BurntSushi/ripgrep@91f5d22 Cargo.toml (path deps with
//     versions, a [target.'cfg(...)'.dependencies.<name>] table, a `walkdir = "2"` partial);
//   - tokio.Cargo.toml: github.com/tokio-rs/tokio@695a07d tokio/Cargo.toml (many target
//     tables, `~2.7.0`, `= 0.1.0-beta.1`, inline tables with features);
//   - tokio-workspace.Cargo.toml: the workspace root of the same commit ([patch.crates-io]
//     path overrides and a multi-line inline table, no deps);
//   - helix.Cargo.toml and helix-term.Cargo.toml: github.com/helix-editor/helix@ba40e54
//     ([workspace.dependencies] with partial versions, `name.workspace = true` inheritance).
func TestExtractFixtures(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"ripgrep", `dependencies anyhow 56 1.0.75 crate/semver
dependencies bstr 57 1.7.0 crate/semver
dependencies grep 58 skip:path dependency
dependencies ignore 59 skip:path dependency
dependencies lexopt 60 0.3.0 crate/semver
dependencies log 61 0.4.5 crate/semver
dependencies serde_json 62 1.0.23 crate/semver
dependencies termcolor 63 1.4.0 crate/semver
dependencies textwrap 64 0.16.0 crate/semver
dependencies tikv-jemallocator 67 0.7.0 crate/semver
dev-dependencies serde 70 1.0.77 crate/semver
dev-dependencies serde_derive 71 1.0.77 crate/semver
dev-dependencies walkdir 72 2 crate/tag
`},
		{"tokio-workspace", ""},
		{"helix", `workspace.dependencies tree-house 41 0.4 crate/tag
workspace.dependencies nucleo 42 0.5.0 crate/semver
workspace.dependencies slotmap 43 1.1.1 crate/semver
workspace.dependencies thiserror 44 2.0 crate/tag
workspace.dependencies tempfile 45 3.27.0 crate/semver
workspace.dependencies bitflags 46 2.12 crate/tag
workspace.dependencies unicode-segmentation 47 1.13 crate/tag
workspace.dependencies ropey 48 1.6.1 crate/semver
workspace.dependencies foldhash 49 0.2 crate/tag
workspace.dependencies parking_lot 50 0.12 crate/tag
workspace.dependencies futures-executor 51 0.3 crate/tag
workspace.dependencies futures-util 52 0.3 crate/tag
workspace.dependencies tokio-stream 53 0.1.18 crate/semver
workspace.dependencies toml 54 1.1 crate/tag
workspace.dependencies termina 55 0.3 crate/tag
workspace.dependencies sonic-rs 56 0.5 crate/tag
workspace.dependencies globset 57 0.4 crate/tag
workspace.dependencies etcetera 58 0.11 crate/tag
workspace.dependencies arc-swap 59 1.9 crate/tag
workspace.dependencies arrayvec 60 0.7 crate/tag
workspace.dependencies criterion 63 0.8 crate/tag
workspace.dependencies quickcheck 64 1 crate/tag
`},
	} {
		src, err := os.ReadFile("testdata/" + tc.file + ".Cargo.toml")
		if err != nil {
			t.Fatal(err)
		}
		if got := describe(t, src, extract(t, src)); got != tc.want {
			t.Errorf("%s:\n%s\nwant:\n%s", tc.file, got, tc.want)
		}
	}
}

func TestExtractFixtureExcerpts(t *testing.T) {
	for _, tc := range []struct {
		file  string
		count int
		want  []string // lines of describe
	}{
		{"tokio", 35, []string{
			"dependencies tokio-macros 95 2.7.0 crate/semver\n",             // "~2.7.0"
			"dependencies socket2 105 0.6.3 crate/semver\n",                 // [target.'cfg(any(...))'.dependencies]
			"dev-dependencies backtrace 122 0.3.58 crate/semver\n",          // [target.'cfg(...)'.dev-dependencies]
			"dependencies libc 125 0.2.168 crate/semver\n",                  // the same crate under several targets
			"dependencies windows-sys 138 0.61 crate/tag\n",                 // [target.'cfg(windows)'.dependencies.windows-sys]
			"dev-dependencies windows-sys 142 0.61 crate/tag\n",             // its dev twin, version before a multi-line array
			"dev-dependencies tracing-mock 177 0.1.0-beta.1 crate/semver\n", // "= 0.1.0-beta.1"
		}},
		{"helix-term", 40, []string{
			"dependencies helix-core 45 skip:path dependency\n",
			"dependencies termina 57 skip:workspace dependency\n",  // an inline table with workspace = true
			"dependencies arc-swap 61 skip:workspace dependency\n", // the dotted workspace key
			"dependencies indexmap 63 2.14 crate/tag\n",
			"dependencies open 79 5.3.5 crate/semver\n",
			"build-dependencies helix-loader 99 skip:path dependency\n",
			"dev-dependencies same-file 105 1.0.1 crate/semver\n",
		}},
	} {
		src, err := os.ReadFile("testdata/" + tc.file + ".Cargo.toml")
		if err != nil {
			t.Fatal(err)
		}
		deps := extract(t, src)
		got := describe(t, src, deps)
		if len(deps) != tc.count {
			t.Errorf("%s: %d deps, want %d:\n%s", tc.file, len(deps), tc.count, got)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", tc.file, w, got)
			}
		}
	}
}

func TestExtractForms(t *testing.T) {
	src := `[package]
name = "x"
version = "0.1.0"
dependencies = "not a table"

[dependencies]
simple = "1.2.3"
caret = "^1.2"
tilde = "~0.4"
exact = "=2.0.0"
atleast = ">= 1.0.0"
"quoted" = '1'
dotted.version = "0.3.1"
dotted.features = ["a"]
renamed = { package = "real-crate", version = "0.9.0" }
star = "*"
wild = "1.*"
range = ">=1.0, <2.0"
lt = "<2"
gt = ">1"
build = "1.0.0+meta"
empty = ""
escaped = "1.0.\u0030"
multi = """1.0.0"""
nover = { features = ["x"] }
pathdep = { path = "../p" }
ws = { workspace = true }
wsfalse = { workspace = false, version = "1.0.0" }
reg = { version = "1.0.0", registry = "corp" }
regidx = { version = "1.0.0", registry-index = "sparse+https://corp/index/" }
gittag = { git = "https://github.com/o/r.git/?x=1#frag", tag = "v1.2.0" }
gitbranch = { git = "https://github.com/o/r", branch = "main" }
gitrev = { git = "https://github.com/o/r", rev = "abc" }
gitssh = { git = "ssh://git@github.com/o/r", tag = "v1" }
gitescaped = { git = "https://github.com/o/r", tag = "v\u0031" }
gitbad = { git = "https://[::1", tag = "v1" }
odd = 1

[dependencies.table]
version = "4.5.6"
features = ["full"]

[dev_dependencies]
legacydev = "1"

[build_dependencies]
legacybuild = "2"

[target.x86_64-pc-windows-gnu.build-dependencies]
winbuild = "0.1.0"

[target.'cfg(unix)'.dependencies.unixtable]
version = "0.2"

[workspace.dependencies]
shared = { version = "3.0", default-features = false }

[patch.crates-io]
patched = { version = "9.9.9" }

[features]
dependencies = ["x"]
`
	want := `dependencies simple 7 1.2.3 crate/semver
dependencies caret 8 1.2 crate/tag
dependencies tilde 9 0.4 crate/tag
dependencies exact 10 2.0.0 crate/semver
dependencies atleast 11 1.0.0 crate/semver
dependencies quoted 12 1 crate/tag
dependencies dotted 13 0.3.1 crate/semver
dependencies real-crate 15 0.9.0 crate/semver
dependencies star 16 skip:unsupported requirement
dependencies wild 17 skip:unsupported requirement
dependencies range 18 skip:unsupported requirement
dependencies lt 19 skip:unsupported requirement
dependencies gt 20 skip:unsupported requirement
dependencies build 21 skip:unsupported requirement
dependencies empty 22 skip:unsupported requirement
dependencies escaped 23 skip:cannot locate value
dependencies multi 24 skip:cannot locate value
dependencies nover 25 skip:no version
dependencies pathdep 26 skip:path dependency
dependencies ws 27 skip:workspace dependency
dependencies wsfalse 28 1.0.0 crate/semver
dependencies reg 29 skip:alternative registry
dependencies regidx 30 skip:alternative registry
dependencies gittag 31 v1.2.0 git-tags/tag
dependencies gitbranch 32 skip:git dependency
dependencies gitrev 33 skip:git dependency
dependencies gitssh 34 skip:git dependency
dependencies gitescaped 35 skip:cannot locate value
dependencies gitbad 36 skip:git dependency
dependencies odd 37 skip:no version
dependencies table 40 4.5.6 crate/semver
dev-dependencies legacydev 44 1 crate/tag
build-dependencies legacybuild 47 2 crate/tag
build-dependencies winbuild 50 0.1.0 crate/semver
dependencies unixtable 53 0.2 crate/tag
workspace.dependencies shared 56 3.0 crate/tag
`
	deps := extract(t, []byte(src))
	if got := describe(t, []byte(src), deps); got != want {
		t.Errorf("deps:\n%s\nwant:\n%s", got, want)
	}
	for _, d := range deps {
		if d.Name == "gittag" && d.Source != "https://github.com/o/r" {
			t.Errorf("git source = %q", d.Source)
		}
		if d.Name != "gittag" && d.Source != "" {
			t.Errorf("%s: source %q", d.Name, d.Source)
		}
	}
}

func TestExtractError(t *testing.T) {
	_, err := New().Extract("Cargo.toml", []byte("[dependencies]\nserde = \"1.0\n"), manager.Env{})
	if err == nil || err.Error() != "line 2: unterminated string" {
		t.Errorf("err = %v", err)
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if m.Name() != "cargo" {
		t.Errorf("Name = %q", m.Name())
	}
	for p, want := range map[string]bool{
		"Cargo.toml": true, "crates/a/Cargo.toml": true, "Cargo.lock": false, "xCargo.toml": false, "cargo.toml": false, "Cargo.toml.orig": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}

func TestEdits(t *testing.T) {
	src := []byte("[dependencies]\nserde = { version = \"~1.0\", features = [\"derive\"] } # keep\ntokio = \"= 1.40.0\"\n")
	deps := extract(t, src)
	var edits []patch.Edit
	for d, v := range map[int]string{0: "1.4", 1: "1.48.1"} {
		e, err := New().Edits(dep.Update{Dep: deps[d], NewVersion: v})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[dependencies]\nserde = { version = \"~1.4\", features = [\"derive\"] } # keep\ntokio = \"= 1.48.1\"\n"; string(out) != want {
		t.Errorf("patched:\n%s", out)
	}
}
