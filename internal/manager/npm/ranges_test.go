package npm

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// TestNewRange checks the range rewrites per rangeStrategy ("" = the default rewrite, which
// update-lockfile, replace, bump and in-range-only share); want "" = not rewritten.
func TestNewRange(t *testing.T) {
	for _, c := range []struct{ rng, v, strategy, want string }{
		// Simple ranges: the version token moves, as before ranges were rewritten whole.
		{"^1.2.3", "2.0.0", "", "^2.0.0"},
		{"^1.2.3", "1.4.0", "", "^1.4.0"},
		{"~1.2.3", "1.3.0", "", "~1.3.0"},
		{">=1.2.3", "2.0.0", "", ">=2.0.0"},
		{"=1.2.3", "1.2.4", "", "=1.2.4"},
		{"^1.0.0-beta.1", "1.0.0", "", "^1.0.0"},
		{"^1.2.3", "1.2.3", "", ""},
		// x-ranges, partial versions and prefixes keep their precision and form.
		{"1.x", "2.3.4", "", "2.x"},
		{"1.2.x", "2.3.4", "", "2.3.x"},
		{"1.x", "1.5.0", "", ""}, // nothing to raise
		{"^1.2", "2.0.1", "", "^2.0"},
		{"v1.2.3", "1.3.0", "", "v1.3.0"},
		{">= 1.0.0", "2.0.0", "", ">= 2.0.0"},
		// Upper bounds move past the new version at their precision; `>` takes it as `>=`.
		{"<2.0.0", "3.1.0", "", "<4.0.0"},
		{"<0.5", "0.7.1", "", "<0.8"},
		{"<=2.0.0", "3.1.0", "", "<=3.1.0"},
		{">=1.2.3 <2.0.0", "3.1.0", "", ">=3.1.0 <4.0.0"},
		{">=1.2.3 <2", "1.5.0", "", ">=1.5.0 <2"},
		{">1.2.3 <2", "2.1.0", "", ">=2.1.0 <3"},
		// Hyphen ranges: a new version above moves the high end, an admitted one the low end.
		{"1.0.0 - 2.0.0", "3.1.0", "", "1.0.0 - 3.1.0"},
		{"1 - 2", "3.1.0", "", "1 - 3"},
		{"1 - 2", "2.9.0", "", "2 - 2"},
		// `||`: replace keeps the replaced last alternative, bump raises the admitting one.
		{"^1.0.0 || ^2.0.0", "3.1.0", "", "^3.1.0"},
		{"^1.0.0 || ^2.0.0", "2.5.0", "", "^1.0.0 || ^2.5.0"},
		// widen
		{"^1.2.3", "2.0.0", "widen", "^1.2.3 || ^2.0.0"},
		{"^1.0.0 ||  ^2.0.0", "3.1.0", "widen", "^1.0.0 ||  ^2.0.0 ||  ^3.1.0"},
		{">=1.2.3 <2.0.0", "3.1.0", "widen", ">=1.2.3 <4.0.0"},
		{"1.0.0 - 2.0.0", "3.1.0", "widen", "1.0.0 - 3.1.0"},
		{"1.x", "2.3.4", "widen", "1.x || 2.x"},
		{"1.2.3", "1.2.4", "widen", "1.2.4"}, // an exact version is replaced
		{"^1.2.3", "1.4.0", "widen", "^1.4.0"},
		// pin
		{"^1.2.3", "1.4.0", "pin", "1.4.0"},
		{"^1.0.0 || ^2.0.0", "2.5.0", "pin", "2.5.0"},
		{"1.4.0", "1.4.0", "pin", ""},
		// The new version must be admitted.
		{"^1.2.3", "not-a-version", "", ""},
	} {
		got, ok := npmManager{}.NewRange(dep.Dep{Range: c.rng}, c.v, c.strategy)
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("NewRange(%q, %s, %q) = %q, want %q", c.rng, c.v, c.strategy, got, c.want)
		}
	}
}

func TestParseRange(t *testing.T) {
	for rng, anchor := range map[string]string{
		"^1.2.3":                "1.2.3",
		"^1.0.0 || ^2.0.0":      "2.0.0",
		">=1.2.3 <2":            "1.2.3",
		"<2":                    "2.0.0",
		"1.x":                   "1.0.0",
		"1.0.0 - 2.0.0":         "1.0.0",
		"~>1.2.3-rc.1+b":        "1.2.3-rc.1+b",
		"*":                     "",
		"x || *":                "",
		"":                      "",
		"1.2.3 -2.0.0":          "",
		"1.2.3 - 2.0.0 - 3":     "",
		"1.2.3-":                "",
		"1.2.3 || ":             "",
		"latest":                "",
		"^1.2.3 < 2":            "1.2.3",
		">=1.2.3 <2.0.0 || 3.x": "3.0.0",
	} {
		r, ok := parseRange(rng)
		got := ""
		if ok {
			got = r.anchor()
		}
		if got != anchor || ok != (anchor != "") {
			t.Errorf("parseRange(%q) = %q, %v; want anchor %q", rng, got, ok, anchor)
		}
	}
}

func TestEditsRange(t *testing.T) {
	src := `{"dependencies": {"a": "^1.0.0 || ^2.0.0", "b": "1.x"}}`
	deps, err := New().Extract("package.json", []byte(src), manager.Env{})
	if err != nil || len(deps) != 2 {
		t.Fatalf("deps = %+v, %v", deps, err)
	}
	for _, c := range []struct {
		u    dep.Update
		want string
	}{
		{dep.Update{Dep: deps[0], NewVersion: "3.0.0", RangeStrategy: "widen"}, `"^1.0.0 || ^2.0.0 || ^3.0.0"`},
		{dep.Update{Dep: deps[0], NewVersion: "2.1.0", RangeStrategy: "pin"}, `"2.1.0"`},
		{dep.Update{Dep: deps[1], NewVersion: "2.0.1"}, `"2.x"`},
	} {
		edits, err := New().Edits(c.u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil || !strings.Contains(string(out), c.want) {
			t.Errorf("%+v: %s, %v", c.u, out, err)
		}
	}
	if _, err := New().Edits(dep.Update{Dep: dep.Dep{Range: "*"}, NewVersion: "1.0.0"}); err == nil {
		t.Error("unparsable range accepted")
	}
}
