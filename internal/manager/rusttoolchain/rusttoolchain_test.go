package rusttoolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		fixture, file, version, versioning, skip string
		line                                     int
	}{
		{"nearcore.rust-toolchain.toml", "rust-toolchain.toml", "1.95.0", "semver", "", 12},
		{"firecracker.rust-toolchain.toml", "rust-toolchain.toml", "1.99.1", "semver", "", 14},
		{"zed.rust-toolchain.toml", "rust-toolchain.toml", "1.98.1", "semver", "", 2},
		{"clippy.rust-toolchain.toml", "rust-toolchain.toml", "nightly-2026-10-30", "", skipDated, 3},
		{"diem.rust-toolchain", "rust-toolchain", "1.69.0", "semver", "", 1},
	} {
		src := fixture(t, tc.fixture)
		d := extractOne(t, tc.file, src)
		if d.Version != tc.version || d.Versioning != tc.versioning || d.Skip != tc.skip || d.Line != tc.line {
			t.Errorf("%s: version %q versioning %q skip %q line %d", tc.fixture, d.Version, d.Versioning, d.Skip, d.Line)
		}
		if tc.skip != "" {
			continue
		}
		got := apply(t, src, d, "1.100.0")
		if want := strings.Replace(string(src), tc.version, "1.100.0", 1); got != want {
			t.Errorf("%s: edited\n%s", tc.fixture, got)
		}
	}
}

func TestExtractToolchains(t *testing.T) {
	for _, tc := range []struct {
		file, src, version, versioning, skip, edited string
	}{
		// Precision is kept: X.Y moves as X.Y under tag versioning.
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.82\"\n", "1.82", "tag", "", "[toolchain]\nchannel = \"1.99\"\n"},
		{"rust-toolchain.toml", "[toolchain]\nchannel = '1.82.0'\ncomponents = [\"clippy\"]\n", "1.82.0", "semver", "", "[toolchain]\nchannel = '1.99.1'\ncomponents = [\"clippy\"]\n"},
		// A host triple stays after the version.
		{"rust-toolchain", "1.82.0-x86_64-unknown-linux-gnu\n", "1.82.0", "semver", "", "1.99.1-x86_64-unknown-linux-gnu\n"},
		{"rust-toolchain", "  1.82\n\n", "1.82", "tag", "", "  1.99\n\n"},
		// A legacy-named file in TOML form.
		{"rust-toolchain", "[toolchain]\nchannel = \"1.82.0\"\n", "1.82.0", "semver", "", "[toolchain]\nchannel = \"1.99.1\"\n"},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"stable\"\n", "stable", "", skipChannel, ""},
		{"rust-toolchain", "nightly\n", "nightly", "", skipChannel, ""},
		{"rust-toolchain", "beta-aarch64-apple-darwin\n", "beta-aarch64-apple-darwin", "", skipChannel, ""},
		{"rust-toolchain", "nightly-2025-10-10\n", "nightly-2025-10-10", "", skipDated, ""},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.83.0-beta.5\"\n", "1.83.0-beta.5", "", skipUnsupported, ""},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"my-toolchain\"\n", "my-toolchain", "", skipUnsupported, ""},
		{"rust-toolchain.toml", "[toolchain]\npath = \"/opt/rust\"\n", "", "", skipPath, ""},
		{"rust-toolchain.toml", "[toolchain]\ncomponents = [\"clippy\"]\n", "", "", skipNoChannel, ""},
		{"rust-toolchain.toml", "[toolchain]\nchannel = \"1.82\\u002e0\"\n", "", "", skipLocate, ""},
	} {
		src := []byte(tc.src)
		d := extractOne(t, tc.file, src)
		if d.Version != tc.version || d.Versioning != tc.versioning || d.Skip != tc.skip {
			t.Errorf("%q: version %q versioning %q skip %q", tc.src, d.Version, d.Versioning, d.Skip)
			continue
		}
		if tc.edited == "" {
			continue
		}
		v := "1.99.1"
		if tc.versioning == "tag" {
			v = "1.99"
		}
		if got := apply(t, src, d, v); got != tc.edited {
			t.Errorf("%q: edited %q, want %q", tc.src, got, tc.edited)
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
