package elm

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func render(ds []dep.Dep) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%d %s %s %s %s", d.Line, d.DepType, d.Datasource, d.Name, d.Version)
		if d.Range != "" {
			fmt.Fprintf(&b, " range=%q constraint=%q", d.Range, d.Constraint)
		}
		if d.Skip != "" {
			fmt.Fprintf(&b, " skip=%q", d.Skip)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func extract(t *testing.T, file string) ([]byte, []dep.Dep) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Skip == "" {
			want := d.Version
			if d.Range != "" {
				want = d.Range
			}
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != want {
				t.Errorf("%s: RefSpan %q, want %q", d.Name, got, want)
			}
		}
	}
	return src, ds
}

// TestExtractApplication reads rtfeldman/elm-spa-example's elm.json.
func TestExtractApplication(t *testing.T) {
	_, ds := extract(t, "testdata/elm-spa-example/elm.json")
	want := `6 elm-version github-releases elm/compiler 0.19.1
9 direct elm-package NoRedInk/elm-json-decode-pipeline 1.0.0
10 direct elm-package elm/browser 1.0.0
11 direct elm-package elm/core 1.0.0
12 direct elm-package elm/html 1.0.0
13 direct elm-package elm/http 1.0.0
14 direct elm-package elm/json 1.0.0
15 direct elm-package elm/time 1.0.0
16 direct elm-package elm/url 1.0.0
17 direct elm-package elm-explorations/markdown 1.0.0
18 direct elm-package rtfeldman/elm-iso8601-date-strings 1.1.0
21 indirect elm-package elm/parser 1.0.0
22 indirect elm-package elm/virtual-dom 1.0.0
27 test-direct elm-package elm-explorations/test 1.0.0
30 test-indirect elm-package elm/random 1.0.0
`
	if got := render(ds); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
	if ds[0].Source != "https://github.com/elm/compiler" {
		t.Errorf("elm-version source = %q", ds[0].Source)
	}
}

// TestExtractPackage reads rtfeldman/elm-css's elm.json.
func TestExtractPackage(t *testing.T) {
	_, ds := extract(t, "testdata/elm-css/elm.json")
	want := `28 elm-version github-releases elm/compiler 0.19.0 range="0.19.0 <= v < 0.20.0" constraint=">=0.19.0 <0.20.0"
30 dependencies elm-package elm/core 1.0.0 range="1.0.0 <= v < 2.0.0" constraint=">=1.0.0 <2.0.0"
31 dependencies elm-package elm/json 1.0.0 range="1.0.0 <= v < 2.0.0" constraint=">=1.0.0 <2.0.0"
32 dependencies elm-package elm/virtual-dom 1.0.0 range="1.0.0 <= v < 2.0.0" constraint=">=1.0.0 <2.0.0"
33 dependencies elm-package robinheghan/murmur3 1.0.0 range="1.0.0 <= v < 2.0.0" constraint=">=1.0.0 <2.0.0"
34 dependencies elm-package rtfeldman/elm-hex 1.0.0 range="1.0.0 <= v < 2.0.0" constraint=">=1.0.0 <2.0.0"
37 test-dependencies elm-package elm-explorations/test 1.2.2 range="1.2.2 <= v < 2.0.0" constraint=">=1.2.2 <2.0.0"
`
	if got := render(ds); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
}

func TestExtractSkipsAndOddShapes(t *testing.T) {
	src := `{
  "type": "application",
  "name": "ignored",
  "dependencies": {
    "direct": {"elm/core": "1.0", "elm": "1.0.0", "elm/json": 1, "elm/html": "1.0.\u0030", "elm/url": "1.0.0"},
    "indirect": [],
    "other": {"elm/time": "1.0.0"},
    "elm/random": "1.0.0"
  },
  "test-dependencies": [],
  "elm-version": 19
}`
	ds, err := New().Extract("elm.json", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := `5 direct elm-package elm/core 1.0 skip="unsupported version"
5 direct elm-package elm 1.0.0 skip="invalid package name"
5 direct elm-package elm/html 1.0.0 skip="unsupported version"
5 direct elm-package elm/url 1.0.0
`
	if got := render(ds); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}

	pkg := `{"dependencies": {"a/b": "1.0.0 <= v < 2.0.0", "a/c": "^1.0.0", "a/d": "1.0 <= v < 2.0", "a/e": "1.0.0 < v <= 1.5.0"}, "type": "package"}`
	ds, err = New().Extract("pkg/elm.json", []byte(pkg), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want = `1 dependencies elm-package a/b 1.0.0 range="1.0.0 <= v < 2.0.0" constraint=">=1.0.0 <2.0.0"
1 dependencies elm-package a/c ^1.0.0 skip="unsupported constraint"
1 dependencies elm-package a/d 1.0 <= v < 2.0 skip="unsupported constraint"
1 dependencies elm-package a/e 1.0.0 range="1.0.0 < v <= 1.5.0" constraint=">1.0.0 <=1.5.0"
`
	if got := render(ds); got != want {
		t.Errorf("package deps =\n%s\nwant\n%s", got, want)
	}

	for _, src := range []string{`{"dependencies": {"a/b": "1.0.0"}}`, `{"type": "library", "dependencies": {"a/b": "1.0.0"}}`, `{"type": ["application"]}`} {
		if ds, err := New().Extract("elm.json", []byte(src), manager.Env{}); err != nil || len(ds) != 0 {
			t.Errorf("%s: deps %v, %v", src, ds, err)
		}
	}
}

func TestExtractErrors(t *testing.T) {
	for src, want := range map[string]string{
		`[]`:                            "top level is not an object",
		``:                              "unexpected EOF",
		`{"type": "application"`:        "unexpected EOF",
		`{"dependencies": {"direct": {`: "unexpected EOF",
		`{"dependencies": {"a/b": }}`:   "invalid character",
		`{"type": "package",,}`:         "invalid character",
		`{"dependencies": {"direct" 1}`: "expecting ':'",
	} {
		if _, err := New().Extract("elm.json", []byte(src), manager.Env{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if m.Name() != Name {
		t.Error(m.Name())
	}
	for p, want := range map[string]bool{"elm.json": true, "frontend/elm.json": true, "elm.json.bak": false, "myelm.json": false, "elm-stuff/elm.json": true} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
}

func TestEdits(t *testing.T) {
	src, ds := extract(t, "testdata/elm-css/elm.json")
	test := ds[len(ds)-1]
	for strategy, want := range map[string]string{
		"":                `"elm-explorations/test": "2.2.0 <= v < 3.0.0"`,
		"update-lockfile": `"elm-explorations/test": "2.2.0 <= v < 3.0.0"`,
		"replace":         `"elm-explorations/test": "2.2.0 <= v < 3.0.0"`,
		"widen":           `"elm-explorations/test": "1.2.2 <= v < 3.0.0"`,
		"pin":             `"elm-explorations/test": "2.2.0 <= v <= 2.2.0"`,
	} {
		edits, err := New().Edits(dep.Update{Dep: test, NewVersion: "2.2.0", RangeStrategy: strategy})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), want) || len(out) != len(src)+len(want)-len(`"elm-explorations/test": "1.2.2 <= v < 2.0.0"`) {
			t.Errorf("%q: elm.json =\n%s\nwant %s", strategy, out, want)
		}
	}

	src, ds = extract(t, "testdata/elm-spa-example/elm.json")
	edits, err := New().Edits(dep.Update{Dep: ds[2], NewVersion: "1.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(string(src), `"elm/browser": "1.0.0"`, `"elm/browser": "1.0.2"`, 1); string(out) != want {
		t.Errorf("elm.json =\n%s", out)
	}

	if _, err := New().Edits(dep.Update{Dep: dep.Dep{Range: "^1.0.0"}, NewVersion: "2.0.0"}); err == nil {
		t.Error("unsupported constraint edited")
	}
}

func TestNewRange(t *testing.T) {
	r := New().(manager.RangeRewriter)
	for _, tt := range []struct {
		rng, v, strategy, want string
	}{
		{"1.0.0 <= v < 2.0.0", "2.1.0", "widen", "1.0.0 <= v < 3.0.0"},
		{"1.0.0 <= v < 2.0.0", "2.1.0", "replace", "2.1.0 <= v < 3.0.0"},
		{"1.0.0 <= v < 2.0.0", "1.4.0", "bump", "1.4.0 <= v < 2.0.0"},
		{"1.0.0 <= v < 2.0.0", "1.4.0", "widen", ""}, // admitted: nothing to widen
		{"1.0.0 <= v < 2.0.0", "1.0.0", "bump", ""},  // unchanged
		{"1.0.0 <= v < 2.0.0", "3.0.0", "pin", "3.0.0 <= v <= 3.0.0"},
		{"1.2.0 <= v < 1.5.0", "1.7.2", "widen", "1.2.0 <= v < 1.8.0"},
		{"1.2.3 <= v < 1.2.4", "1.2.9", "widen", "1.2.3 <= v < 1.2.10"},
		{"1.0.0 <= v <= 1.5.0", "1.7.2", "widen", "1.0.0 <= v <= 1.7.2"},
		{"1.0.0 < v < 2.0.0", "2.0.0", "widen", "1.0.0 < v < 3.0.0"},
		{"1.0.0 < v < 2.0.0", "2.0.0", "", "2.0.0 <= v < 3.0.0"},
		{"1.0.0<=v<2.0.0", "2.0.0", "widen", "1.0.0 <= v < 3.0.0"},
		{"1.0.0 <= v < 2.0.0", "2.0", "widen", ""}, // not a version
		{"^1.0.0", "2.0.0", "widen", ""},
	} {
		got, ok := r.NewRange(dep.Dep{Range: tt.rng}, tt.v, tt.strategy)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("NewRange(%q, %s, %q) = %q, %v, want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
}

func TestAdmits(t *testing.T) {
	c, _ := parseConstraint("1.0.0 < v <= 2.0.0")
	for v, want := range map[string]bool{"1.0.0": false, "1.0.1": true, "2.0.0": true, "2.0.1": false, "x": false} {
		if c.admits(v) != want {
			t.Errorf("admits(%s) = %v", v, !want)
		}
	}
}
