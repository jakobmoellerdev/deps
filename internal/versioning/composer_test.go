package versioning

import (
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations below follow composer/semver (VersionParser, Comparator) as documented at
// https://getcomposer.org/doc/articles/versions.md.

func TestParseComposer(t *testing.T) {
	for s, ok := range map[string]bool{
		"1": true, "1.2": true, "1.2.3": true, "1.2.3.4": true, "v1.2.3": true, "V2.0": true, "3.0.0-RC1": true,
		"2.0.0-beta.1": true, "1.0.0-alpha2": true, "1.0.0beta": true, "1.0.0.RC.2": true, "1.0.0-p1": true,
		"1.0.0-pl2": true, "1.0.0-patch3": true, "1.0.0-dev": true, "1.0.0-beta1-dev": true, "1.2.3+build.5": true,
		"1.0.0-stable": true,
		"":             false, "v": false, "dev-main": false, "1.x-dev": false, "1.2.3.4.5": false, "1.0.0-foo": false,
		"1.0.0-beta1-x": false, "x": false, "1.": false, "1234567890": false, "1.0 as 2.0": false,
	} {
		if _, got := parseComposer(s); got != ok {
			t.Errorf("parseComposer(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareComposer(t *testing.T) {
	s := get(t, "composer")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0.0-RC1", "1.0.0", -1}, {"1.0.0-beta2", "1.0.0-RC1", -1}, {"1.0.0-alpha3", "1.0.0-beta1", -1},
		{"1.0.0-dev", "1.0.0-alpha1", -1}, {"1.0.0-p1", "1.0.0", 1}, {"1.0.0-pl1", "1.0.0-patch1", 0},
		{"v1.2.3", "1.2.3", 0}, {"1.2", "1.2.0.0", 0}, {"1.10.0", "1.9.9", 1}, {"1.0.0-beta.2", "1.0.0-beta1", 1},
		{"1.0.0-beta1-dev", "1.0.0-beta1", -1}, {"1.0.0-b2", "1.0.0-beta2", 0}, {"1.2.3.4", "1.2.3", 1},
		{"1.0.0-stable", "1.0.0", 0}, {"1.2.3+5", "1.2.3", 0},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestComposerBest(t *testing.T) {
	names := []string{"v3.4.0", "v3.4.1", "v3.5.0-RC1", "v3.5.0", "v4.0.0-beta1", "4.0.0", "dev-main", "4.x-dev"}
	below4 := func(v string) bool { return v != "4.0.0" && v != "v4.0.0-beta1" }
	runBest(t, "composer", []bestCase{
		{"3.4.0", "4.0.0", names, nil},
		{"v3.4.0", "v3.5.0", names, below4},
		{"4.0.0-alpha1", "4.0.0", names, nil},
		{"3.5.0-beta1", "v3.5.0", names, below4},
		{"4.0.0", "", names, nil},
		{"bad", "", names, nil},
	})
}

func TestComposerKindAndSemver(t *testing.T) {
	s := get(t, "composer")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"3.4.0", "3.4.1", dep.Patch}, {"3.4.0", "v3.5.0", dep.Minor}, {"v3.4.0", "4.0.0", dep.Major}, {"1.2.3", "1.2.3.1", dep.Patch},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	if s.Compatible("1.0.0", "1.1.0-RC1") || !s.Compatible("1.0.0-beta1", "1.1.0-RC1") || !s.Compatible("1.0.0", "1.0.0-p1") {
		t.Error("Compatible")
	}
	for v, want := range map[string]string{
		"1.2": "1.2.0", "v2.3.4.5": "2.3.4", "1.0.0-RC2": "1.0.0-rc.2", "1.0.0-beta": "1.0.0-beta.0", "1.0.0-p1": "1.0.0",
		"1.0.0-dev": "1.0.0-dev.0", "1.0.0-alpha1-dev": "1.0.0-alpha.1",
	} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
}

func TestComposerMatchRange(t *testing.T) {
	m, ok := get(t, "composer").(RangeMatcher)
	if !ok {
		t.Fatal("composer is not a RangeMatcher")
	}
	for _, tt := range []struct {
		rng     string
		in, out []string
	}{
		{"^1.2.3", []string{"1.2.3", "v1.9.0", "1.2.3-p1"}, []string{"2.0.0", "1.2.2", "2.0.0-beta1", "1.3.0-RC1"}},
		{"^0.3", []string{"0.3.0", "0.3.9"}, []string{"0.4.0", "0.2.9"}},
		{"^0.0.3", []string{"0.0.3"}, []string{"0.0.4"}},
		{"^0", []string{"0.0.1", "0.9"}, []string{"1.0"}},
		{"^0.0", []string{"0.0.9"}, []string{"0.1.0"}},
		{"^v13.35.0", []string{"13.35.0", "v13.40.1"}, []string{"14.0.0"}},
		{"~1.2", []string{"1.2.0", "1.9.9"}, []string{"2.0.0", "1.1.9"}},
		{"~1.2.3", []string{"1.2.3", "1.2.9"}, []string{"1.3.0"}},
		{"~1", []string{"1.0", "1.9"}, []string{"2.0"}},
		{"~1.2.3.4", []string{"1.2.3.9"}, []string{"1.2.4.0"}},
		{"1.2.*", []string{"1.2.0", "1.2.99"}, []string{"1.3.0", "1.1.0"}},
		{"1.x", []string{"1.0.0", "1.99"}, []string{"2.0.0"}},
		{"*", []string{"0.0.1", "99.0"}, []string{"1.0.0-beta1", "dev-main"}},
		{"1.2.3", []string{"1.2.3", "v1.2.3.0"}, []string{"1.2.4"}},
		{"== 1.2", []string{"1.2.0"}, []string{"1.2.1"}},
		{">=1.2 <2.0", []string{"1.2.0", "1.9.9"}, []string{"2.0.0", "1.1.0", "2.0.0-RC1"}},
		{">= 1.2, < 2.0", []string{"1.2.0"}, []string{"2.0.0"}},
		{">1.0,<=1.5", []string{"1.0.1", "1.5.0"}, []string{"1.0.0", "1.5.1"}},
		{"!=1.3 >=1.0", []string{"1.2", "1.3.1"}, []string{"1.3.0", "0.9"}},
		{"<>1.3", []string{"1.2"}, []string{"1.3"}},
		{"^2.0|^3.0", []string{"2.1.0", "3.9.0"}, []string{"1.9.0", "4.0.0"}},
		{"^1 || ^2 || ^3", []string{"1.0", "3.5"}, []string{"4.0"}},
		{"1.0 - 2.0", []string{"1.0.0", "2.0.9"}, []string{"2.1.0", "0.9"}},
		{"1.0.0 - 2.1.0", []string{"2.1.0"}, []string{"2.1.1"}},
		{"^2.0@beta", []string{"2.0.0-beta1", "2.1.0"}, []string{"3.0.0", "2.0.0-alpha1", "1.9.0"}},
		{"^2.0@dev", []string{"2.0.0-alpha1", "2.0.0-dev"}, []string{"1.9.0"}},
		{"^2.0-beta1", []string{"2.0.0-beta1", "2.0.0-beta2", "2.0.0"}, []string{"2.0.0-alpha1", "3.0.0"}},
		{"^1.0@stable", []string{"1.1.0"}, []string{"1.1.0-RC1"}},
		{">=2.0-dev", []string{"2.0.0-dev", "2.0.0"}, []string{"1.9"}},
	} {
		f, err := m.MatchRange(tt.rng)
		if err != nil {
			t.Fatalf("%s: %v", tt.rng, err)
		}
		for _, v := range tt.in {
			if !f(v) {
				t.Errorf("%q does not admit %s", tt.rng, v)
			}
		}
		for _, v := range tt.out {
			if f(v) {
				t.Errorf("%q admits %s", tt.rng, v)
			}
		}
	}
	for _, bad := range []string{
		"", "dev-main", "1.x-dev", "^1.0 as 1.5", "@dev", "^1.0@foo", "^1.0 ||", "|| ^1.0", "^*", ">1.*",
		"1.0 - 2.*", "1.0 -", "1.0 - 2.0 - 3.0", "^1.0,,", "latest", "dev-main#abc123", "^1.0^2.0",
	} {
		if _, err := m.MatchRange(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestParseComposerConstraint(t *testing.T) {
	c, err := ParseComposerConstraint("^1.2 || >= 2.0, <3.0@beta")
	if err != nil {
		t.Fatal(err)
	}
	want := ComposerConstraint{Sep: " || ", Alts: []ComposerAlt{
		{Start: 0, End: 4, Cmps: []ComposerCmp{{Op: "^", Start: 0, Off: 1, Ver: "1.2", End: 4}}},
		{Start: 8, End: 25, Cmps: []ComposerCmp{
			{Op: ">=", Start: 8, Off: 11, Ver: "2.0", End: 14},
			{Op: "<", Start: 16, Off: 17, Ver: "3.0", Flag: "beta", End: 25},
		}},
	}}
	if !slices.EqualFunc(c.Alts, want.Alts, func(a, b ComposerAlt) bool {
		return a.Start == b.Start && a.End == b.End && a.Hyphen == b.Hyphen && slices.Equal(a.Cmps, b.Cmps)
	}) || c.Sep != want.Sep {
		t.Errorf("got %+v\nwant %+v", c, want)
	}
	h, err := ParseComposerConstraint("1.0 - 2.0|3.0")
	if err != nil || !h.Alts[0].Hyphen || len(h.Alts[0].Cmps) != 2 || h.Alts[0].Cmps[1].Ver != "2.0" || h.Sep != "|" {
		t.Errorf("hyphen: %+v, %v", h, err)
	}
}

func TestComposerVersionParts(t *testing.T) {
	for _, tt := range []struct {
		v      string
		parts  []int
		suffix string
	}{
		{"v1.2.3", []int{1, 2, 3}, ""}, {"2.0-beta1", []int{2, 0}, "-beta1"}, {"1.0.0.RC2", []int{1, 0, 0}, ".RC2"}, {"3", []int{3}, ""},
	} {
		p, s, ok := ComposerVersionParts(tt.v)
		if !ok || !slices.Equal(p, tt.parts) || s != tt.suffix {
			t.Errorf("ComposerVersionParts(%s) = %v, %q, %v", tt.v, p, s, ok)
		}
	}
	if _, _, ok := ComposerVersionParts("dev-main"); ok {
		t.Error("dev-main is a version")
	}
	if !IsComposerPrerelease("1.0.0-RC1") || IsComposerPrerelease("1.0.0-p1") || IsComposerPrerelease("x") {
		t.Error("IsComposerPrerelease")
	}
}
