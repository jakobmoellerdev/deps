package vcpkg

import (
	"fmt"
	"os"
	"path"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

const builtinRaw = "https://raw.githubusercontent.com/microsoft/vcpkg/"

// extract runs Extract over files (slash path → content) in order, then Link, and renders
// every dep as `<file>:<line> <type> <name> <version>[@<digest7>] src=<source> skip=<skip>`.
func extract(t *testing.T, env manager.Env, files ...string) []string {
	t.Helper()
	m := New()
	var all [][]dep.Dep
	srcs := map[string][]byte{}
	for i := 0; i < len(files); i += 2 {
		src := []byte(files[i+1])
		srcs[files[i]] = src
		if !m.Match(files[i]) {
			t.Fatalf("%s not matched", files[i])
		}
		deps, err := m.Extract(files[i], src, env)
		if err != nil {
			t.Fatalf("%s: %v", files[i], err)
		}
		all = append(all, deps)
	}
	m.(manager.Linker).Link(all)
	var out []string
	for _, deps := range all {
		for _, d := range deps {
			src := srcs[d.File]
			if d.Skip == "" {
				span := d.RefSpan
				want := d.Version
				if d.Datasource == dsRefs {
					span, want = d.DigestSpan, d.Digest
				}
				if got := string(src[span.Start:span.End]); got != want {
					t.Errorf("%s %s: span %q, want %q", d.File, d.Name, got, want)
				}
			}
			v := d.Version
			if d.Digest != "" {
				v += "@" + dep.ShortDigest(d.Digest)
			}
			out = append(out, fmt.Sprintf("%s:%d %s %s %s src=%s skip=%s", d.File, d.Line, d.DepType, d.Name, v, d.Source, d.Skip))
		}
	}
	return out
}

func fixture(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(path.Join("testdata", p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertDeps(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("deps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestExtractCemu(t *testing.T) {
	base := builtinRaw + "f0fb3ddba5135b80982668de39dbaa139c00d281"
	assertDeps(t, extract(t, manager.Env{}, "vcpkg.json", fixture(t, "cemu/vcpkg.json")),
		"vcpkg.json:4 builtin-baseline microsoft/vcpkg HEAD@f0fb3dd src=https://github.com/microsoft/vcpkg skip=",
		"vcpkg.json:33 dependencies fmt 12.1.0 src="+base+" skip=",
		"vcpkg.json:75 overrides glslang 15.1.0 src="+base+" skip=",
		"vcpkg.json:79 overrides sdl3 3.4.10 src="+base+" skip=",
		"vcpkg.json:83 overrides wxwidgets 3.3.3 src="+base+" skip=",
	)
}

func TestExtractArcticDB(t *testing.T) {
	base := builtinRaw + "cea592f4772491abdb7c483387a59ea89889f4be"
	// third_party/vcpkg_overlays has the overlay ports the project patches.
	env := manager.Env{Exists: func(p string) bool {
		return p == "cpp/third_party/vcpkg_overlays/aws-sdk-cpp/portfile.cmake" || p == "cpp/third_party/vcpkg_overlays/arcticdb-sparrow/portfile.cmake"
	}}
	got := extract(t, env, "cpp/vcpkg.json", fixture(t, "arcticdb/vcpkg.json"))
	want := map[string]string{
		"dependencies curl 8.18.0":                      "src=" + base + " skip=",
		"overrides openssl 3.3.0":                       "src= skip=version-string",
		"overrides arcticdb-sparrow 2.4.0":              "src= skip=overlay port",
		"overrides arrow 21.0.0#2":                      "src=" + base + " skip=",
		"overrides aws-sdk-cpp 1.11.820":                "src= skip=overlay port",
		"overrides rapidcheck 2023-12-14":               "src=" + base + " skip=",
		"builtin-baseline microsoft/vcpkg HEAD@cea592f": "src=https://github.com/microsoft/vcpkg skip=",
	}
	if len(got) != 41 {
		t.Errorf("%d deps, want 41:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, l := range got {
		_, rest, _ := strings.Cut(l, " ")
		key, tail, _ := strings.Cut(rest, " src=")
		if w, ok := want[key]; ok {
			if "src="+tail != w {
				t.Errorf("%s: got %q, want %q", key, "src="+tail, w)
			}
			delete(want, key)
		}
	}
	for k := range want {
		t.Errorf("missing %s", k)
	}
}

func TestExtractCesiumNative(t *testing.T) {
	base := builtinRaw + "56bb2411609227288b70117ead2c47585ba07713"
	got := extract(t, manager.Env{},
		"vcpkg-configuration.json", fixture(t, "cesium-native/vcpkg-configuration.json"),
		"vcpkg.json", `{"name":"x","dependencies":[{"name":"fmt","version>=":"11.0.2#1"}, "spdlog"]}`)
	assertDeps(t, got,
		"vcpkg-configuration.json:4 default-registry microsoft/vcpkg HEAD@56bb241 src=https://github.com/microsoft/vcpkg skip=",
		"vcpkg.json:1 dependencies fmt 11.0.2#1 src="+base+" skip=",
	)
	// The real manifest has no constraints: only the configuration's baseline is a dep.
	if got := extract(t, manager.Env{}, "vcpkg.json", fixture(t, "cesium-native/vcpkg.json")); len(got) != 0 {
		t.Errorf("cesium-native vcpkg.json: %v", got)
	}
	assertDeps(t, extract(t, manager.Env{}, "vcpkg-configuration.json", fixture(t, "hpcc/vcpkg-configuration.json")),
		"vcpkg-configuration.json:5 default-registry microsoft/vcpkg HEAD@9e593bb src=https://github.com/microsoft/vcpkg skip=")
}

func TestRegistries(t *testing.T) {
	const c1 = "1111111111111111111111111111111111111111"
	const c2 = "2222222222222222222222222222222222222222"
	const c3 = "3333333333333333333333333333333333333333"
	manifest := `{
  "builtin-baseline": "` + c1 + `",
  "dependencies": [
    {"name": "fmt", "version>=": "10.0.0"},
    {"name": "beicode", "version>=": "1.0.0"},
    {"name": "bei-x", "version>=": "1.0.0"},
    {"name": "local", "version>=": "1.0.0"},
    {"name": "nover"},
    {"version>=": "1"},
    {"name": "num", "version>=": 1},
    {"name": "esc", "version>=": "1.\u0030"}
  ],
  "overrides": [{"name": "zlib", "version": "1.3", "port-version": 1}, {"name": "date", "version-date": "2024-01-15"}],
  "vcpkg-configuration": {
    "default-registry": {"kind": "builtin", "baseline": "` + c2 + `"},
    "registries": [
      {"kind": "git", "repository": "https://github.com/northwindtraders/vcpkg-registry", "baseline": "` + c3 + `", "packages": ["bei*"]},
      {"kind": "git", "repository": "https://git.example.com/a/reg.git", "reference": "release", "baseline": "` + c3 + `", "packages": ["beicode"]},
      {"kind": "filesystem", "path": "../reg", "packages": ["local"]},
      {"kind": "git", "repository": "git@github.com:a/b", "baseline": "short", "packages": ["x"]},
      {"kind": "git", "repository": "https://github.com/a/b", "reference": "v1.2.3", "baseline": "` + c3 + `", "packages": ["y"]},
      {"kind": "git", "repository": "file:///reg", "baseline": "` + c3 + `", "packages": ["z"]},
      {"kind": "artifact", "location": "https://example.com/a.zip", "name": "a"}
    ]
  }
}`
	assertDeps(t, extract(t, manager.Env{}, "a/vcpkg.json", manifest),
		"a/vcpkg.json:2 builtin-baseline microsoft/vcpkg HEAD@1111111 src=https://github.com/microsoft/vcpkg skip=",
		"a/vcpkg.json:15 default-registry microsoft/vcpkg HEAD@2222222 src=https://github.com/microsoft/vcpkg skip=",
		"a/vcpkg.json:17 registry northwindtraders/vcpkg-registry HEAD@3333333 src=https://github.com/northwindtraders/vcpkg-registry skip=",
		"a/vcpkg.json:18 registry a/reg release@3333333 src=https://git.example.com/a/reg skip=",
		"a/vcpkg.json:20 registry a/b HEAD@short src=https://github.com/a/b skip=baseline is not a commit",
		"a/vcpkg.json:21 registry a/b v1.2.3@3333333 src=https://github.com/a/b skip=reference is a tag",
		"a/vcpkg.json:22 registry file:///reg HEAD@3333333 src= skip=unrecognized repository",
		"a/vcpkg.json:4 dependencies fmt 10.0.0 src="+builtinRaw+c2+" skip=",
		"a/vcpkg.json:5 dependencies beicode 1.0.0 src=https://git.example.com/a/reg/raw/"+c3+" skip=",
		"a/vcpkg.json:6 dependencies bei-x 1.0.0 src=https://raw.githubusercontent.com/northwindtraders/vcpkg-registry/"+c3+" skip=",
		"a/vcpkg.json:7 dependencies local 1.0.0 src= skip=unsupported registry kind filesystem",
		"a/vcpkg.json:9 dependencies  1 src= skip=no name",
		"a/vcpkg.json:10 dependencies num  src= skip=version is not a string",
		"a/vcpkg.json:11 dependencies esc 1.0 src= skip=cannot locate value",
		"a/vcpkg.json:13 overrides zlib 1.3 src= skip=port-version field",
		"a/vcpkg.json:13 overrides date 2024-01-15 src="+builtinRaw+c2+" skip=",
	)

	// default-registry null leaves unmatched ports without a registry; a builtin default
	// without baseline uses builtin-baseline; no baseline at all is a skip.
	assertDeps(t, extract(t, manager.Env{}, "vcpkg.json", `{"dependencies":[{"name":"fmt","version>=":"1"}],"vcpkg-configuration":{"default-registry":null}}`),
		"vcpkg.json:1 dependencies fmt 1 src= skip=no registry for the port")
	assertDeps(t, extract(t, manager.Env{}, "vcpkg.json", `{"builtin-baseline":"`+c1+`","dependencies":[{"name":"fmt","version>=":"1"}],"vcpkg-configuration":{"default-registry":{"kind":"builtin"}}}`),
		"vcpkg.json:1 builtin-baseline microsoft/vcpkg HEAD@1111111 src=https://github.com/microsoft/vcpkg skip=",
		"vcpkg.json:1 dependencies fmt 1 src="+builtinRaw+c1+" skip=")
	assertDeps(t, extract(t, manager.Env{}, "vcpkg.json", `{"dependencies":[{"name":"fmt","version>=":"1"}]}`),
		"vcpkg.json:1 dependencies fmt 1 src= skip=no builtin-baseline")
	assertDeps(t, extract(t, manager.Env{}, "vcpkg.json", `{"dependencies":[{"name":"fmt","version>=":"1"}],"vcpkg-configuration":{"default-registry":{"kind":"git","repository":"https://github.com/a/b"}}}`),
		"vcpkg.json:1 dependencies fmt 1 src= skip=git registry without baseline")
	assertDeps(t, extract(t, manager.Env{}, "vcpkg.json", `{"dependencies":[{"name":"fmt","version>=":"1"}],"vcpkg-configuration":{"default-registry":{"kind":"git","repository":"git@github.com:a/b","baseline":"`+c1+`"}}}`),
		"vcpkg.json:1 default-registry a/b HEAD@1111111 src=https://github.com/a/b skip=",
		"vcpkg.json:1 dependencies fmt 1 src= skip=unrecognized registry repository")
	// The overlay is the port's own directory.
	env := manager.Env{Exists: func(p string) bool { return p == "ports/fmt/portfile.cmake" }}
	assertDeps(t, extract(t, env, "vcpkg.json", `{"builtin-baseline":"`+c1+`","dependencies":[{"name":"fmt","version>=":"1"}],"vcpkg-configuration":{"overlay-ports":["ports/fmt", "/abs", ""]}}`),
		"vcpkg.json:1 builtin-baseline microsoft/vcpkg HEAD@1111111 src=https://github.com/microsoft/vcpkg skip=",
		"vcpkg.json:1 dependencies fmt 1 src= skip=overlay port")
}

// TestBaselineKeys: a port carries the BaselineKey of the updatable baseline its versions are
// read at (the engine moves it with that baseline), none when the baseline cannot move.
func TestBaselineKeys(t *testing.T) {
	const c1 = "1111111111111111111111111111111111111111"
	const c2 = "2222222222222222222222222222222222222222"
	cfg := `{"registries": [
  {"kind": "git", "repository": "https://github.com/a/reg", "baseline": "` + c2 + `", "packages": ["beicode"]},
  {"kind": "git", "repository": "https://github.com/a/tagged", "reference": "v1.2.3", "baseline": "` + c2 + `", "packages": ["pinned"]}
]}`
	manifest := `{"builtin-baseline": "` + c1 + `", "dependencies": [
  {"name": "fmt", "version>=": "10.0.0"},
  {"name": "beicode", "version>=": "1.0.0"},
  {"name": "pinned", "version>=": "1.0.0"}
]}`
	m := New()
	cdeps, err := m.Extract("vcpkg-configuration.json", []byte(cfg), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	mdeps, err := m.Extract("vcpkg.json", []byte(manifest), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m.(manager.Linker).Link([][]dep.Dep{cdeps, mdeps})
	want := map[string]string{"fmt": BaselineKey(mdeps[0]), "beicode": BaselineKey(cdeps[0]), "pinned": ""}
	if mdeps[0].DepType != DepTypeBuiltinBaseline || cdeps[0].Name != "a/reg" || cdeps[1].Skip != "reference is a tag" {
		t.Fatalf("baselines %+v %+v", mdeps[0], cdeps)
	}
	for _, d := range mdeps[1:] {
		if d.Baseline != want[d.Name] || d.Skip != "" {
			t.Errorf("%s: baseline %q, want %q (skip %q)", d.Name, d.Baseline, want[d.Name], d.Skip)
		}
	}
	if BaselineKey(mdeps[0]) == BaselineKey(cdeps[0]) {
		t.Error("baseline keys of different files collide")
	}
}

func TestExtractMalformed(t *testing.T) {
	m := New()
	if _, err := m.Extract("vcpkg.json", []byte(`{"dependencies": [`), manager.Env{}); err == nil {
		t.Error("truncated JSON: no error")
	}
	for _, src := range []string{`[]`, `{"builtin-baseline": 1, "dependencies": {}, "overrides": 1, "vcpkg-configuration": []}`} {
		deps, err := m.Extract("vcpkg.json", []byte(src), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("%s: %v %v", src, deps, err)
		}
	}
	for p, want := range map[string]bool{"vcpkg.json": true, "a/vcpkg-configuration.json": true, "avcpkg.json": false, "vcpkg.json5": false} {
		if m.Match(p) != want {
			t.Errorf("Match(%s) = %v", p, !want)
		}
	}
	if m.Name() != Name {
		t.Error(m.Name())
	}
}

func TestEdits(t *testing.T) {
	const c1 = "1111111111111111111111111111111111111111"
	const c2 = "2222222222222222222222222222222222222222"
	src := `{"builtin-baseline": "` + c1 + `", "dependencies": [{"name": "fmt", "version>=": "10.0.0"}]}`
	m := New()
	deps, err := m.Extract("vcpkg.json", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m.(manager.Linker).Link([][]dep.Dep{deps})
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[0], NewVersion: headVersion, NewDigest: c2},
		{Dep: deps[0], NewVersion: headVersion, NewDigest: c1},
		{Dep: deps[1], NewVersion: "11.1.4#1"},
		{Dep: deps[1], NewVersion: "10.0.0"},
	} {
		e, err := m.Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"builtin-baseline": "` + c2 + `", "dependencies": [{"name": "fmt", "version>=": "11.1.4#1"}]}`; string(out) != want {
		t.Errorf("got %s", out)
	}
}

// TestBOM covers a manifest written with a UTF-8 byte order mark: it yields the deps of the
// same manifest without one, with spans into the original bytes.
func TestBOM(t *testing.T) {
	const c1 = "1111111111111111111111111111111111111111"
	const c2 = "2222222222222222222222222222222222222222"
	src := "\ufeff" + `{"builtin-baseline": "` + c1 + `", "dependencies": [{"name": "fmt", "version>=": "10.0.0"}]}`
	m := New()
	deps, err := m.Extract("vcpkg.json", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 {
		t.Fatalf("deps = %+v", deps)
	}
	m.(manager.Linker).Link([][]dep.Dep{deps})
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[0], NewVersion: headVersion, NewDigest: c2},
		{Dep: deps[1], NewVersion: "11.1.4#1"},
	} {
		e, err := m.Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\ufeff" + `{"builtin-baseline": "` + c2 + `", "dependencies": [{"name": "fmt", "version>=": "11.1.4#1"}]}`; string(out) != want {
		t.Errorf("got %s", out)
	}
}

func TestVersioning(t *testing.T) {
	s, ok := versioning.Get(Versioning)
	if !ok {
		t.Fatal("invalid versioning")
	}
	for _, v := range []string{"1", "1.2", "1.2.3", "1.2.3.4", "1.2.3#2", "2024-01-15", "2024-01-15#1"} {
		if !s.Valid(v) {
			t.Errorf("%s invalid", v)
		}
	}
	for _, v := range []string{"vs-2022", "1.2.3-rc1", "1#"} {
		if s.Valid(v) {
			t.Errorf("%s valid", v)
		}
	}
	for _, c := range [][2]string{{"1.2.3", "1.2.3#1"}, {"1.2.3#9", "1.2.4"}, {"1.2.3.9", "1.2.4"}, {"1.2.3.4", "1.2.3.4#1"}, {"2024-01-15", "2024-02-01"}} {
		if s.Compare(c[0], c[1]) >= 0 {
			t.Errorf("%s >= %s", c[0], c[1])
		}
	}
}
