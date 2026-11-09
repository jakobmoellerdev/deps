package rusttoolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rustversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures (verbatim):
//   - testdata/nearcore.rust-toolchain.toml: github.com/near/nearcore@a79a148ee53a rust-toolchain.toml
//   - testdata/firecracker.rust-toolchain.toml: github.com/firecracker-microvm/firecracker@6ea6634994fa rust-toolchain.toml
//   - testdata/zed.rust-toolchain.toml: github.com/zed-industries/zed@4a6121bb0f10 rust-toolchain.toml
//   - testdata/clippy.rust-toolchain.toml: github.com/rust-lang/rust-clippy@32a833324c16 rust-toolchain.toml
//   - testdata/diem.rust-toolchain: github.com/diem/diem@fc4714a8ea27 rust-toolchain (legacy form)
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func extractOne(t *testing.T, file string, src []byte) dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, src, manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatalf("Extract(%s) = %+v, %v", file, deps, err)
	}
	d := deps[0]
	if d.Manager != Name || d.Datasource != Datasource || d.DepType != DepType || d.Name != DepName || d.File != file {
		t.Errorf("dep = %+v", d)
	}
	return d
}

func apply(t *testing.T, src []byte, d dep.Dep, v string) string {
	t.Helper()
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: v})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"rust-toolchain.toml": true, "rust-toolchain": true, "crates/x/rust-toolchain.toml": true,
		"rust-toolchain.yml": false, "my-rust-toolchain.toml": false, "rust-toolchain/x.toml": false, "testdata/zed.rust-toolchain.toml": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
	if m.Name() != Name {
		t.Errorf("Name = %q", m.Name())
	}
}

func TestExtractFixtures(t *testing.T) {
	for _, tc := range []struct {
		fixture, file, version, versioning, skip, next string
		line                                           int
	}{
		{"nearcore.rust-toolchain.toml", "rust-toolchain.toml", "1.95.0", "semver", "", "1.100.0", 12},
		{"firecracker.rust-toolchain.toml", "rust-toolchain.toml", "1.99.1", "semver", "", "1.100.0", 14},
		{"zed.rust-toolchain.toml", "rust-toolchain.toml", "1.98.1", "semver", "", "1.100.0", 2},
		{"clippy.rust-toolchain.toml", "rust-toolchain.toml", "nightly-2026-10-30", rustversion.NightlyVersioning, "", "nightly-2026-11-05", 3},
		{"diem.rust-toolchain", "rust-toolchain", "1.69.0", "semver", "", "1.100.0", 1},
	} {
		src := fixture(t, tc.fixture)
		d := extractOne(t, tc.file, src)
		if d.Version != tc.version || d.Versioning != tc.versioning || d.Skip != tc.skip || d.Line != tc.line || d.Range != "" {
			t.Errorf("%s: version %q versioning %q skip %q line %d range %q", tc.fixture, d.Version, d.Versioning, d.Skip, d.Line, d.Range)
		}
		got := apply(t, src, d, tc.next)
		if want := strings.Replace(string(src), tc.version, tc.next, 1); got != want {
			t.Errorf("%s: edited\n%s", tc.fixture, got)
		}
	}
}

func TestExtractToolchains(t *testing.T) {
	const nightly = rustversion.NightlyVersioning
	for _, tc := range []struct {
		file, src, version, rng, versioning, skip, next, edited string
	}{
		// Precision is kept: X.Y moves as X.Y under tag versioning.
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.82\"\n", "1.82", "", "tag", "", "1.99", "[toolchain]\nchannel = \"1.99\"\n"},
		{"rust-toolchain.toml", "[toolchain]\nchannel = '1.82.0'\ncomponents = [\"clippy\"]\n", "1.82.0", "", "semver", "", "1.99.1", "[toolchain]\nchannel = '1.99.1'\ncomponents = [\"clippy\"]\n"},
		// A host triple stays after the version.
		{"rust-toolchain", "1.82.0-x86_64-unknown-linux-gnu\n", "1.82.0", "", "semver", "", "1.99.1", "1.99.1-x86_64-unknown-linux-gnu\n"},
		{"rust-toolchain", "  1.82\n\n", "1.82", "", "tag", "", "1.99", "  1.99\n\n"},
		// A legacy-named file in TOML form.
		{"rust-toolchain", "[toolchain]\nchannel = \"1.82.0\"\n", "1.82.0", "", "semver", "", "1.99.1", "[toolchain]\nchannel = \"1.99.1\"\n"},
		// A dated nightly moves to the newest nightly, its host triple kept.
		{"rust-toolchain", "nightly-2025-10-10\n", "nightly-2025-10-10", "", nightly, "", "nightly-2026-11-05", "nightly-2026-11-05\n"},
		{"rust-toolchain", "nightly-2025-10-10-aarch64-apple-darwin\n", "nightly-2025-10-10", "", nightly, "", "nightly-2026-11-05", "nightly-2026-11-05-aarch64-apple-darwin\n"},
		// stable and nightly are ranges without a version, which pin writes a release over.
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"stable\"\n", "", "stable", "semver", "", "1.99.1", "[toolchain]\nchannel = \"1.99.1\"\n"},
		{"rust-toolchain", "nightly\n", "", "nightly", nightly, "", "nightly-2026-11-05", "nightly-2026-11-05\n"},
		{"rust-toolchain", "stable-x86_64-pc-windows-msvc\n", "", "stable", "semver", "", "1.99.1", "1.99.1-x86_64-pc-windows-msvc\n"},
		{"rust-toolchain", "beta-aarch64-apple-darwin\n", "beta-aarch64-apple-darwin", "", "", skipChannel, "", ""},
		{"rust-toolchain", "beta-2025-10-01\n", "beta-2025-10-01", "", "", skipDated, "", ""},
		{"rust-toolchain", "stable-2025-10-01\n", "stable-2025-10-01", "", "", skipDated, "", ""},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.83.0-beta.5\"\n", "1.83.0-beta.5", "", "", skipUnsupported, "", ""},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"my-toolchain\"\n", "my-toolchain", "", "", skipUnsupported, "", ""},
		{"rust-toolchain.toml", "[toolchain]\npath = \"/opt/rust\"\n", "", "", "", skipPath, "", ""},
		{"rust-toolchain.toml", "[toolchain]\ncomponents = [\"clippy\"]\n", "", "", "", skipNoChannel, "", ""},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.82\\u002e0\"\n", "", "", "", skipLocate, "", ""},
	} {
		src := []byte(tc.src)
		d := extractOne(t, tc.file, src)
		if d.Version != tc.version || d.Range != tc.rng || d.Versioning != tc.versioning || d.Skip != tc.skip {
			t.Errorf("%q: version %q range %q versioning %q skip %q", tc.src, d.Version, d.Range, d.Versioning, d.Skip)
			continue
		}
		if Channel(d) != (tc.rng != "") {
			t.Errorf("%q: Channel = %v", tc.src, tc.rng == "")
		}
		if tc.edited == "" {
			continue
		}
		if got := apply(t, src, d, tc.next); got != tc.edited {
			t.Errorf("%q: edited %q, want %q", tc.src, got, tc.edited)
		}
	}
}

// NewRange writes a release over a channel under rangeStrategy pin only.
func TestNewRange(t *testing.T) {
	rw, ok := New().(manager.RangeRewriter)
	if !ok {
		t.Fatal("not a RangeRewriter")
	}
	ch := dep.Dep{Manager: Name, Range: "stable"}
	for _, tc := range []struct {
		d          dep.Dep
		v, s, want string
	}{
		{ch, "1.99.1", dep.RangePin, "1.99.1"},
		{ch, "1.99.1", "replace", ""},
		{ch, "", dep.RangePin, ""},
		{dep.Dep{Manager: Name, Version: "1.82.0"}, "1.99.1", dep.RangePin, ""},
	} {
		if got, ok := rw.NewRange(tc.d, tc.v, tc.s); got != tc.want || ok != (tc.want != "") {
			t.Errorf("NewRange(%+v, %s, %s) = %q, %v", tc.d, tc.v, tc.s, got, ok)
		}
	}
	if Channel(dep.Dep{Manager: "mise", Range: "stable"}) {
		t.Error("Channel of another manager's dep")
	}
}

// Toolchain is the version a file pins, without its host triple; channels pin none.
func TestToolchain(t *testing.T) {
	for _, tc := range []struct{ file, src, want string }{
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.82.0\"\n", "1.82.0"},
		{"rust-toolchain", "1.82-x86_64-unknown-linux-gnu\n", "1.82"},
		{"rust-toolchain", "nightly-2025-10-10\n", "nightly-2025-10-10"},
		{"rust-toolchain", "stable\n", ""},
		{"rust-toolchain", "beta\n", ""},
		{"rust-toolchain.toml", "[toolchain]\npath = \"/opt/rust\"\n", ""},
		{"rust-toolchain.toml", "[other]\n", ""},
		{"rust-toolchain.toml", "[toolchain\n", ""},
	} {
		if got, ok := Toolchain(tc.file, []byte(tc.src)); got != tc.want || ok != (tc.want != "") {
			t.Errorf("Toolchain(%q) = %q, %v", tc.src, got, ok)
		}
	}
}

func TestExtractNone(t *testing.T) {
	for _, src := range []string{"", "# empty\n", "[other]\nchannel = \"1.82.0\"\n", "toolchain = 1\n"} {
		deps, err := New().Extract("rust-toolchain.toml", []byte(src), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("%q: %+v, %v", src, deps, err)
		}
	}
	if _, err := New().Extract("rust-toolchain.toml", []byte("[toolchain\n"), manager.Env{}); err == nil {
		t.Error("invalid TOML: no error")
	}
}
