package julia

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// The testdata projects are real-world files, verbatim:
//   - dataframes/Project.toml: JuliaData/DataFrames.jl (main, fetched 2026-11-05)
//   - json/Project.toml: JuliaIO/JSON.jl (master), with [weakdeps] and [extensions]
//   - packageanalyzer/: ericphanson/PackageAnalyzerJuliaCon2023 (main), an application with
//     its committed format-2 Manifest.toml (Julia 1.9.2)
//   - revise/: timholy/Revise.jl (master), a Julia 1.12 workspace (docs, test); its
//     Manifest.toml was resolved by Pkg.resolve() of Julia 1.12.7 (Revise commits none)
//   - cuda/: JuliaGPU/CUDA.jl (6dbfaa6) Project.toml, lib/cublas and lib/cusolver, nested
//     workspaces with [sources] paths

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		f := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// render lists deps as `line name depType range version [skip]`.
func render(deps []dep.Dep) string {
	var b strings.Builder
	for _, d := range deps {
		fmt.Fprintf(&b, "%d %s %s %q", d.Line, d.Name, d.DepType, d.Range)
		if d.Version != "" {
			fmt.Fprintf(&b, " %s", d.Version)
		}
		if d.Skip != "" {
			fmt.Fprintf(&b, " [%s]", d.Skip)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func extract(t *testing.T, file string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, read(t, filepath.Join("testdata", file)), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Manager != Name || d.Versioning != Versioning || d.Datasource != Datasource || d.File != file {
			t.Errorf("%s: %+v", d.Name, d)
		}
	}
	return deps
}

func TestExtractDataFrames(t *testing.T) {
	deps := extract(t, "dataframes/Project.toml")
	want := `30 CategoricalArrays extras "0.10.0, 1" 1
31 Combinatorics extras "1.0.2" 1.0.2
32 Compat deps "4.2" 4.2
33 DataAPI deps "1.16.0" 1.16.0
34 DataStructures deps "0.18, 0.19" 0.19
35 DataValues extras "0.4.13" 0.4.13
36 InlineStrings deps "1.3.0, 2" 2
37 InvertedIndices deps "1.3" 1.3
38 IteratorInterfaceExtensions deps "0.1.1, 1" 1
39 Missings deps "0.4.2, 1" 1
40 OffsetArrays extras "1.12.10" 1.12.10
41 PooledArrays deps "1.4.2" 1.4.2
42 PrecompileTools deps "1" 1
43 PrettyTables deps "2.1, 3.0.5" 3.0.5
44 Reexport deps "1" 1
45 SentinelArrays deps "1.2" 1.2
46 ShiftedArrays extras "1, 2" 2
47 SortingAlgorithms deps "0.3, 1" 1
48 StableRNGs extras "1" 1
49 Statistics deps "1" 1
50 TableTraits deps "0.4, 1" 1
51 Tables deps "1.9.0" 1.9.0
52 Unitful extras "1" 1
53 julia julia "1.10" 1.10 [julia compat is the project's supported Julia versions]
`
	if got := render(deps); got != want {
		t.Errorf("deps:\n%s\nwant:\n%s", got, want)
	}
	for _, d := range deps {
		if d.Name == "Compat" && (d.Source != "34da2185-b29b-5c13-b0c7-acf172513d20" || string(read(t, "testdata/dataframes/Project.toml")[d.RefSpan.Start:d.RefSpan.End]) != "4.2") {
			t.Errorf("Compat = %+v", d)
		}
	}
}

func TestExtractWeakdepsAndStdlibs(t *testing.T) {
	got := render(extract(t, "json/Project.toml"))
	want := `23 Arrow extras "2.8.0" 2.8.0
24 ArrowTypes weakdeps "2.2" 2.2
25 Parsers deps "2.8.8, 3" 3
26 PrecompileTools deps "1" 1
27 StructUtils deps "2.10" 2.10
28 julia julia "1.10" 1.10 [julia compat is the project's supported Julia versions]
`
	if got != want {
		t.Errorf("deps:\n%s\nwant:\n%s", got, want)
	}
}

func TestExtractNoCompat(t *testing.T) {
	deps := extract(t, "packageanalyzer/Project.toml")
	if got := render(deps); got != "2 Pluto deps \">= 0\"\n" || !deps[0].RangeFixed {
		t.Errorf("deps = %s", got)
	}
}

func TestExtractSources(t *testing.T) {
	got := render(extract(t, "cuda/Project.toml"))
	for _, line := range []string{
		`37 CUDACore deps "=6.5.1" 6.5.1 [path source]`,
		`39 Reexport deps "1.1" 1.1`,
		`40 cuBLAS deps "=6.5.1" 6.5.1 [path source]`,
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("missing %s in\n%s", line, got)
		}
	}
	src := "[deps]\nA = \"0f8b85d8-7281-11e9-16c2-39a750bddbf1\"\nB = \"1f8b85d8-7281-11e9-16c2-39a750bddbf1\"\nC = \"2f8b85d8-7281-11e9-16c2-39a750bddbf1\"\n" +
		"LinearAlgebra = \"37e2e46d-f89d-539d-b4ee-838fcccc9c8e\"\n" +
		"[sources]\nA = {url = \"https://github.com/o/A.jl\", rev = \"main\"}\nB = \"x\"\n" +
		"[compat]\nA = \"1\"\nB = 1\nC = \"1.\\u0030\"\nD = \"1\"\nLinearAlgebra = \"1\"\nE = \"> 1\"\n"
	deps, err := New().Extract("Project.toml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := `10 A deps "1" 1 [url source]
11 B deps "" [compat entry is not a string]
12 C deps "1.0" 1.0 [compat entry with escapes]
13 D  "1" 1 [not in [deps], [weakdeps] or [extras]]
14 LinearAlgebra deps "1" 1 [julia standard library]
15 E  "> 1" > 1 [not in [deps], [weakdeps] or [extras]]
`
	if got := render(deps); got != want {
		t.Errorf("deps:\n%s\nwant:\n%s", got, want)
	}
	deps, _ = New().Extract("Project.toml", []byte("[deps]\nE = \"3f8b85d8-7281-11e9-16c2-39a750bddbf1\"\nU = \"4f8b85d8-7281-11e9-16c2-39a750bddbf1\"\n[compat]\nE = \"> 1\"\nU = \"< 2\"\n"), manager.Env{})
	if got := render(deps); got != "5 E deps \"> 1\" > 1 [unsupported compat entry]\n6 U deps \"< 2\"\n" {
		t.Errorf("deps = %s", got)
	}
	if _, err := New().Extract("Project.toml", []byte("[deps"), manager.Env{}); err == nil {
		t.Error("broken TOML")
	}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"Project.toml": true, "a/JuliaProject.toml": true, "docs/Project.toml": true,
		"aProject.toml": false, "a/xJuliaProject.toml": false, "Manifest.toml": false, "Project.toml.bak": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%s) = %v", p, got)
		}
	}
	if m.Name() != Name {
		t.Error("Name")
	}
}

func TestRewrite(t *testing.T) {
	for _, tt := range []struct {
		spec, v, strategy, want string
	}{
		// out of range: replace (default) keeps the top term's operator and precision
		{"0.21, 1", "2.3.4", "", "2"},
		{"4.2", "5.1.0", "replace", "5.1"},
		{"~1.2", "1.5.0", "", "~1.5"},
		{"^1.2.3", "2.0.1", "", "^2.0.1"},
		{"=0.3.1", "0.4.2", "", "=0.4.2"},
		{"=1.2", "1.4.5", "", "=1.4.5"},
		{"1.2 - 1.5", "2.1.0", "", "2.1 - 2.1"},
		{"< 2", "2.3.0", "", "< 3"},
		{"v1.2", "2.0.0", "", "v2.0"},
		{"0.25", "0.26.0+0", "", "0.26"},
		// widen appends the caret prefix (CompatHelper), or raises an upper bound in place
		{"0.21, 1", "2.3.4", "widen", "0.21, 1, 2"},
		{"0.21", "0.22.1", "widen", "0.21, 0.22"},
		{"0.0.3", "0.0.5", "widen", "0.0.3, 0.0.5"},
		{"0.25,0.26", "1.0.0", "widen", "0.25,0.26,1"},
		{"^1.2", "2.0.0", "widen", "^1.2, ^2"},
		{"~1.2", "1.5.0", "widen", "~1.2, ~1.5"},
		{"=1.2.3", "1.3.0", "widen", "=1.2.3, =1.3.0"},
		{"< 2", "2.3.0", "widen", "< 3"},
		{"1.2 - 1.5", "2.1.0", "widen", "1.2 - 2.1"},
		{"0.5, < 2", "2.0.1", "widen", "0.5, < 3"},
		// admitted: bump drops the terms below and raises the floor
		{"0.21, 1", "1.4.5", "bump", "1"},
		{"1.2", "1.4.5", "bump", "1.4"},
		{"1.2 - 2", "1.4.5", "bump", "1.4 - 2"},
		{"~1.0, ^1.1.1", "1.3.0", "bump", "^1.3.0"},
		{"1", "1.4.5", "bump", ""}, // nothing to raise at precision 1
		{">= 1.2", "3.0.0", "bump", ">= 3.0"},
		{"< 2", "1.5.0", "bump", ""},  // no floor
		{"0.21, 1", "1.4.5", "", "1"}, // the engine only asks for bumps of admitted versions
		// pin
		{"0.21, 1", "1.4.5+1", "pin", "=1.4.5"},
		{"=1.4.5", "1.4.5", "pin", ""},
		// invalid input
		{"> 1", "2.0.0", "", ""},
		{"1", "junk", "", ""},
	} {
		got, ok := rewrite(tt.spec, tt.v, tt.strategy)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("rewrite(%q, %s, %q) = %q, %v, want %q", tt.spec, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
}

func TestEdits(t *testing.T) {
	src := "[deps]\nJSON = \"682c06a0-de6a-54ab-a142-c8b1cf79cde6\"\n\n[compat]\nJSON = \"0.21, 1\" # keep\n"
	deps, err := New().Extract("Project.toml", []byte(src), manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatal(deps, err)
	}
	d := deps[0]
	if d.Source != "682c06a0-de6a-54ab-a142-c8b1cf79cde6" || d.Version != "1" {
		t.Fatalf("dep = %+v", d)
	}
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "2.0.1", RangeStrategy: dep.RangeWiden})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if want := strings.Replace(src, `"0.21, 1"`, `"0.21, 1, 2"`, 1); err != nil || string(out) != want {
		t.Errorf("patched = %q, %v", out, err)
	}
	if _, err := New().Edits(dep.Update{Dep: d, NewVersion: "junk"}); err == nil {
		t.Error("an invalid version cannot be written")
	}
	fixed := dep.Dep{Name: "Pluto", Range: anyVersion, RangeFixed: true}
	if _, err := New().Edits(dep.Update{Dep: fixed, NewVersion: "1.0.0"}); err == nil {
		t.Error("a package without a compat entry has nothing to rewrite")
	}
	if _, ok := New().(manager.RangeRewriter).NewRange(fixed, "1.0.0", ""); ok {
		t.Error("NewRange of a fixed range")
	}
}
