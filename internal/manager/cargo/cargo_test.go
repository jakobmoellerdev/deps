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

// describe renders deps one per line as `<depType> <name> <line> <version> <datasource>/
// <versioning>[ <range>][ @<source>]` or `<depType> <name> <line> skip:<reason>`, failing when
// a RefSpan does not cover exactly the requirement (or the git tag).
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
		want := d.Range
		if want == "" {
			want = d.Version
		}
		if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != want {
			t.Errorf("%s: span %q, want %q", d.Name, got, want)
		}
		fmt.Fprintf(&b, "%s %s %d %s %s/%s", d.DepType, d.Name, d.Line, d.Version, d.Datasource, d.Versioning)
		if d.Range != "" && d.Range != d.Version {
			fmt.Fprintf(&b, " %q", d.Range)
		}
		if d.Source != "" {
			fmt.Fprintf(&b, " @%s", d.Source)
		}
		b.WriteByte('\n')
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

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/" + name + ".Cargo.toml")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// Real-world manifests (testdata, copied verbatim):
//   - ripgrep.Cargo.toml: github.com/BurntSushi/ripgrep@91f5d22 Cargo.toml (path deps with
//     versions, a [target.'cfg(...)'.dependencies.<name>] table, a `walkdir = "2"` partial);
//   - tokio.Cargo.toml: github.com/tokio-rs/tokio@695a07d tokio/Cargo.toml (many target
//     tables, `~2.7.0`, `= 0.1.0-beta.1`, inline tables with features);
//   - tokio-workspace.Cargo.toml: the workspace root of the same commit ([patch.crates-io]
//     path overrides and a multi-line inline table);
//   - helix.Cargo.toml and helix-term.Cargo.toml: github.com/helix-editor/helix@ba40e54
//     ([workspace.dependencies] with partial versions, `name.workspace = true` inheritance);
//   - pyo3.Cargo.toml: github.com/PyO3/pyo3@f41d7e8 Cargo.toml and diesel.Cargo.toml:
//     github.com/diesel-rs/diesel@95f70ae diesel/Cargo.toml (comparator lists
//     `">= 0.10, < 0.11"`, `">=0.2.5, <0.6.0"`, `=0.3.78`);
//   - cargo-book.Cargo.toml: the Cargo book's examples of registry deps, renames, wildcards,
//     [patch] and [replace] (sources in the file).
func TestExtractFixtures(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"ripgrep", `dependencies anyhow 56 1.0.75 crate/cargo
dependencies bstr 57 1.7.0 crate/cargo
dependencies grep 58 skip:path dependency
dependencies ignore 59 skip:path dependency
dependencies lexopt 60 0.3.0 crate/cargo
dependencies log 61 0.4.5 crate/cargo
dependencies serde_json 62 1.0.23 crate/cargo
dependencies termcolor 63 1.4.0 crate/cargo
dependencies textwrap 64 0.16.0 crate/cargo
dependencies tikv-jemallocator 67 0.7.0 crate/cargo
dev-dependencies serde 70 1.0.77 crate/cargo
dev-dependencies serde_derive 71 1.0.77 crate/cargo
dev-dependencies walkdir 72 2 crate/cargo
`},
		{"tokio-workspace", `patch tokio 19 skip:path dependency
patch tokio-macros 20 skip:path dependency
patch tokio-stream 21 skip:path dependency
patch tokio-test 22 skip:path dependency
patch tokio-util 23 skip:path dependency
`},
		{"cargo-book", `dependencies some-crate 11 1.0 crate/cargo @my-registry
dependencies foo 12 0.1 crate/cargo
dependencies foo 13 skip:git dependency
dependencies foo 14 0.1 crate/cargo @custom
dependencies any 15  crate/cargo "*"
dependencies one 16 1 crate/cargo "1.*"
dependencies one-two 17 1.2 crate/cargo "1.2.*"
patch uuid 20 skip:path dependency
patch uuid-2 21 skip:git dependency
patch uuid-3 22 skip:git dependency
patch my-library 25 skip:path dependency
replace foo 28 skip:git dependency
replace bar 29 skip:path dependency
`},
		{"helix", `workspace.dependencies tree-house 41 0.4 crate/cargo
workspace.dependencies nucleo 42 0.5.0 crate/cargo
workspace.dependencies slotmap 43 1.1.1 crate/cargo
workspace.dependencies thiserror 44 2.0 crate/cargo
workspace.dependencies tempfile 45 3.27.0 crate/cargo
workspace.dependencies bitflags 46 2.12 crate/cargo
workspace.dependencies unicode-segmentation 47 1.13 crate/cargo
workspace.dependencies ropey 48 1.6.1 crate/cargo
workspace.dependencies foldhash 49 0.2 crate/cargo
workspace.dependencies parking_lot 50 0.12 crate/cargo
workspace.dependencies futures-executor 51 0.3 crate/cargo
workspace.dependencies futures-util 52 0.3 crate/cargo
workspace.dependencies tokio-stream 53 0.1.18 crate/cargo
workspace.dependencies toml 54 1.1 crate/cargo
workspace.dependencies termina 55 0.3 crate/cargo
workspace.dependencies sonic-rs 56 0.5 crate/cargo
workspace.dependencies globset 57 0.4 crate/cargo
workspace.dependencies etcetera 58 0.11 crate/cargo
workspace.dependencies arc-swap 59 1.9 crate/cargo
workspace.dependencies arrayvec 60 0.7 crate/cargo
workspace.dependencies criterion 63 0.8 crate/cargo
workspace.dependencies quickcheck 64 1 crate/cargo
`},
	} {
		src := fixture(t, tc.file)
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
			"dependencies tokio-macros 95 2.7.0 crate/cargo \"~2.7.0\"\n",
			"dependencies socket2 105 0.6.3 crate/cargo\n",        // [target.'cfg(any(...))'.dependencies]
			"dev-dependencies backtrace 122 0.3.58 crate/cargo\n", // [target.'cfg(...)'.dev-dependencies]
			"dependencies libc 125 0.2.168 crate/cargo\n",         // the same crate under several targets
			"dependencies windows-sys 138 0.61 crate/cargo\n",     // [target.'cfg(windows)'.dependencies.windows-sys]
			"dev-dependencies windows-sys 142 0.61 crate/cargo\n", // its dev twin, version before a multi-line array
			"dev-dependencies tracing-mock 177 0.1.0-beta.1 crate/cargo \"= 0.1.0-beta.1\"\n",
		}},
		{"helix-term", 40, []string{
			"dependencies helix-core 45 skip:path dependency\n",
			"dependencies termina 57 skip:workspace dependency\n",  // an inline table with workspace = true
			"dependencies arc-swap 61 skip:workspace dependency\n", // the dotted workspace key
			"dependencies indexmap 63 2.14 crate/cargo\n",
			"build-dependencies helix-loader 99 skip:path dependency\n",
		}},
		{"pyo3", 46, []string{
			"dependencies chrono-tz 52 0.10 crate/cargo \">= 0.10, < 0.11\"\n",
			"dependencies hashbrown 55 0.15.0 crate/cargo \">= 0.15.0, < 0.18\"\n", // the requirement before a multi-line features array
			"dependencies indexmap 58 2.5.0 crate/cargo \">= 2.5.0, < 3\"\n",
			"dev-dependencies chrono-tz 80 0.10 crate/cargo \">= 0.10, < 0.11\"\n",
			"build-dependencies pyo3-build-config 97 skip:path dependency\n",
		}},
		{"diesel", 37, []string{
			"dependencies mysqlclient-sys 27 0.2.5 crate/cargo \">=0.2.5, <0.6.0\"\n",
			"dependencies bigdecimal 42 0.0.13 crate/cargo \">=0.0.13, < 0.5.0\"\n",
			"dependencies r2d2 44 0.8.2 crate/cargo \">= 0.8.2, < 0.9.0\"\n",
			"dev-dependencies wasm-bindgen-test 67 0.3.78 crate/cargo \"=0.3.78\"\n",
		}},
	} {
		src := fixture(t, tc.file)
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
regio = { version = "1.0.0", registry = "crates-io" }
regidx = { version = "1.0.0", registry-index = "sparse+https://corp/index/" }
gittag = { git = "https://github.com/o/r.git/?x=1#frag", tag = "v1.2.0" }
gitbranch = { git = "https://github.com/o/r", branch = "main" }
gitrev = { git = "https://github.com/o/r", rev = "abc" }
gitssh = { git = "ssh://git@github.com/o/r", tag = "v1" }
gitescaped = { git = "https://github.com/o/r", tag = "v\u0031" }
gitbad = { git = "https://[::1", tag = "v1" }
odd = 1
bad = "~>1.0"

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
patchtag = { git = "https://github.com/o/p", tag = "v2.0.0" }

[replace]
"old@0.1.0" = { git = "https://github.com/o/old", tag = "0.1.1" }

[features]
dependencies = ["x"]
`
	want := `dependencies simple 7 1.2.3 crate/cargo
dependencies caret 8 1.2 crate/cargo "^1.2"
dependencies tilde 9 0.4 crate/cargo "~0.4"
dependencies exact 10 2.0.0 crate/cargo "=2.0.0"
dependencies atleast 11 1.0.0 crate/cargo ">= 1.0.0"
dependencies quoted 12 1 crate/cargo
dependencies dotted 13 0.3.1 crate/cargo
dependencies real-crate 15 0.9.0 crate/cargo
dependencies star 16  crate/cargo "*"
dependencies wild 17 1 crate/cargo "1.*"
dependencies range 18 1.0 crate/cargo ">=1.0, <2.0"
dependencies lt 19  crate/cargo "<2"
dependencies gt 20 1 crate/cargo ">1"
dependencies build 21 1.0.0+meta crate/cargo
dependencies empty 22 skip:unsupported requirement
dependencies escaped 23 skip:cannot locate value
dependencies multi 24 1.0.0 crate/cargo
dependencies nover 25 skip:no version
dependencies pathdep 26 skip:path dependency
dependencies ws 27 skip:workspace dependency
dependencies wsfalse 28 1.0.0 crate/cargo
dependencies reg 29 1.0.0 crate/cargo @corp
dependencies regio 30 1.0.0 crate/cargo
dependencies regidx 31 1.0.0 crate/cargo @sparse+https://corp/index/
dependencies gittag 32 v1.2.0 git-tags/tag @https://github.com/o/r
dependencies gitbranch 33 skip:git dependency
dependencies gitrev 34 skip:git rev is not a commit
dependencies gitssh 35 skip:git dependency
dependencies gitescaped 36 skip:cannot locate value
dependencies gitbad 37 skip:git dependency
dependencies odd 38 skip:no version
dependencies bad 39 skip:unsupported requirement
dependencies table 42 4.5.6 crate/cargo
dev-dependencies legacydev 46 1 crate/cargo
build-dependencies legacybuild 49 2 crate/cargo
build-dependencies winbuild 52 0.1.0 crate/cargo
dependencies unixtable 55 0.2 crate/cargo
workspace.dependencies shared 58 3.0 crate/cargo
patch patched 61 9.9.9 crate/cargo
patch patchtag 62 v2.0.0 git-tags/tag @https://github.com/o/p
replace old 65 0.1.1 git-tags/tag @https://github.com/o/old
`
	deps := extract(t, []byte(src))
	if got := describe(t, []byte(src), deps); got != want {
		t.Errorf("deps:\n%s\nwant:\n%s", got, want)
	}
	for _, d := range deps {
		if d.Name == "caret" && d.Constraint != "" || d.Name == "simple" && d.Constraint != "^1.2.3" || d.Name == "wild" && d.Constraint != "" {
			t.Errorf("%s: constraint %q", d.Name, d.Constraint)
		}
	}
}

func TestExtractError(t *testing.T) {
	_, err := New().Extract("Cargo.toml", []byte("[dependencies]\nserde = \"1.0\n"), manager.Env{})
	if err == nil || !strings.HasPrefix(err.Error(), "toml: line 2: ") {
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
	src := []byte("[dependencies]\n" +
		"serde = { version = \"~1.0\", features = [\"derive\"] } # keep\n" +
		"tokio = \"= 1.40.0\"\n" +
		"eyre = { version = \">= 0.6.8, < 0.7\", optional = true }\n" +
		"one = \"1.*\"\n" +
		"partial = \"1.2\"\n" +
		"tagged = { git = \"https://github.com/o/r\", tag = \"v1.0.0\" }\n")
	deps := extract(t, src)
	var edits []patch.Edit
	for d, v := range map[int]string{0: "1.4", 1: "1.48.1", 2: "0.7.1", 3: "2", 4: "2.0.1", 5: "v1.1.0"} {
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
	want := "[dependencies]\n" +
		"serde = { version = \"~1.4\", features = [\"derive\"] } # keep\n" +
		"tokio = \"= 1.48.1\"\n" +
		"eyre = { version = \">= 0.7.1, < 0.8\", optional = true }\n" +
		"one = \"2.*\"\n" +
		"partial = \"2.0\"\n" +
		"tagged = { git = \"https://github.com/o/r\", tag = \"v1.1.0\" }\n"
	if string(out) != want {
		t.Errorf("patched:\n%s", out)
	}
	bad := deps[0]
	bad.Range = "~>1"
	if _, err := New().Edits(dep.Update{Dep: bad, NewVersion: "2"}); err == nil {
		t.Error("unparsable range accepted")
	}
}

// TestGitRev checks that a git dependency pinned to a commit rev is a git-refs dep tracking its
// branch (else HEAD), that Edits rewrites the rev at its written length and refuses a branch
// change, and that ReadLocks completes an abbreviated rev from Cargo.lock (or skips it).
func TestGitRev(t *testing.T) {
	const full = "0123456789abcdef0123456789abcdef01234567"
	const next = "fedcba9876543210fedcba9876543210fedcba98"
	src := []byte("[dependencies]\n" +
		"pinned = { git = \"https://github.com/o/pinned.git\", rev = \"" + full + "\" }\n" +
		"short = { git = \"https://github.com/o/short\", branch = \"dev\", rev = \"0123456\" }\n" +
		"unlocked = { git = \"https://github.com/o/unlocked\", rev = \"89abcde\" }\n" +
		"tagrev = { git = \"https://github.com/o/t\", tag = \"v1\", rev = \"" + full + "\" }\n")
	root := t.TempDir()
	write(t, root, map[string]string{"Cargo.toml": string(src), "Cargo.lock": "version = 4\n\n" +
		"[[package]]\nname = \"short\"\nversion = \"0.1.0\"\nsource = \"git+https://github.com/o/short?branch=dev&rev=0123456#" + full + "\"\n\n" +
		"[[package]]\nname = \"unlocked\"\nversion = \"0.1.0\"\nsource = \"git+https://github.com/o/unlocked?rev=1111111#" + next + "\"\n"})
	deps := extract(t, src)
	for i, want := range []struct{ ds, version, digest, source string }{
		{DatasourceGitRefs, "HEAD", full, "https://github.com/o/pinned"},
		{DatasourceGitRefs, "dev", "0123456", "https://github.com/o/short"},
		{DatasourceGitRefs, "HEAD", "89abcde", "https://github.com/o/unlocked"},
		{"git-tags", "v1", "", "https://github.com/o/t"},
	} {
		if d := deps[i]; d.Datasource != want.ds || d.Version != want.version || d.Digest != want.digest || d.Source != want.source || d.Skip != "" || d.Line != 2+i {
			t.Errorf("dep %d = %+v, want %+v", i, d, want)
		}
	}
	files := [][]dep.Dep{deps}
	if errs := New().(cargoManager).ReadLocks(root, files); len(errs) > 0 {
		t.Fatal(errs)
	}
	if deps[1].Digest != full || deps[0].Digest != full || deps[2].Skip != "abbreviated git rev without a Cargo.lock commit" {
		t.Errorf("after ReadLocks: %q %q skip %q", deps[0].Digest, deps[1].Digest, deps[2].Skip)
	}
	var edits []patch.Edit
	for _, d := range deps[:2] {
		e, err := New().Edits(dep.Update{Dep: d, Type: dep.Digest, NewVersion: d.Version, NewDigest: next})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(strings.Replace(string(src), full, next, 1), "\"0123456\"", "\"fedcba9\"", 1); string(out) != want {
		t.Errorf("patched:\n%s\nwant:\n%s", out, want)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "main", NewDigest: next}); err == nil {
		t.Error("branch change accepted")
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "HEAD", NewDigest: "abc"}); err == nil {
		t.Error("short digest accepted")
	}
}
