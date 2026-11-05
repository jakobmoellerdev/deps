package versioning

import (
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations below follow NuGet.Versioning (NuGetVersion, VersionComparer, VersionRange,
// FloatRange) as documented at https://learn.microsoft.com/nuget/concepts/package-versioning.

func TestParseNuGet(t *testing.T) {
	for s, ok := range map[string]bool{
		"1": true, "1.2": true, "13.0.3": true, "4.0.0.0": true, "1.01.2": true, "8.0.100-rc.2.23502.2": true,
		"1.0.0-beta01": true, "1.0.0-alpha-1": true, "1.0.0+sha.5114f85": true, "1.0.0-rc.1+build.5": true,
		"": false, "v1.0": false, "1.2.3.4.5": false, "1.": false, ".1": false, "1..2": false, "1.0-": false,
		"1.0-a..b": false, "1.0+": false, "1.0-a_b": false, "1.x": false, "99999999999999999999": false, "1 0": false,
	} {
		if _, got := parseNuGet(s); got != ok {
			t.Errorf("parseNuGet(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareNuGet(t *testing.T) {
	s := get(t, "nuget")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0.0.0", 0}, {"1.01", "1.1", 0}, {"1.0.0.1", "1.0.0", 1}, {"1.10.0", "1.9.0", 1},
		{"1.0.0-beta", "1.0.0", -1}, {"1.0.0-Beta", "1.0.0-beta", 0}, {"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1}, {"1.0.0-beta.2", "1.0.0-beta.11", -1}, {"1.0.0-rc.1", "1.0.0-beta.11", 1},
		{"1.0.0+a", "1.0.0+b", 0}, {"8.0.100-rc.2.23502.2", "8.0.100", -1}, {"1.0.0-beta01", "1.0.0-beta02", -1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestNuGetBest(t *testing.T) {
	names := []string{"12.0.3", "13.0.1", "13.0.2", "13.0.3", "13.0.4-beta1", "8.0.100", "8.0.101", "8.0.404", "9.0.100-rc.2.24474.11", "9.0.100"}
	runBest(t, "nuget", []bestCase{
		{"12.0.3", "13.0.3", names, func(v string) bool { return v < "8" }},
		{"12.0.3", "13.0.2", names, func(v string) bool { return v < "13.0.3" }},
		{"8.0.100", "8.0.404", names, func(v string) bool { return v < "9" && v >= "8" }},
		{"8.0.100", "9.0.100", names, func(v string) bool { return v >= "8" }},
		{"9.0.100-rc.1.24452.12", "9.0.100", names, func(v string) bool { return v >= "9" }},
		{"13.0.4-alpha", "13.0.4-beta1", names, func(v string) bool { return v < "8" }},
		{"bad", "", names, nil},
	})
}

func TestNuGetKindAndSemver(t *testing.T) {
	s := get(t, "nuget")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"13.0.1", "13.0.3", dep.Patch}, {"13.0.1", "13.1.0", dep.Minor}, {"12.0.3", "13.0.1", dep.Major},
		{"4.0.0.0", "4.0.0.1", dep.Patch}, {"8.0.100", "8.0.404", dep.Patch}, {"1.0.0-rc.1", "1.0.0", dep.Patch},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	if s.Compatible("1.0.0", "1.1.0-rc1") || !s.Compatible("1.0.0-rc1", "1.1.0-rc1") || s.Compatible("1.0.0", "1.0") {
		t.Error("Compatible")
	}
	for v, want := range map[string]string{"1.2": "1.2.0", "4.0.0.1": "4.0.0", "1.0.0-rc.1+b": "1.0.0-rc.1"} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
	if r := s.Rank("1.0.0", []string{"2.0.0"}, nil, nil); r.Len() != 1 || r.At(0).Major != "2" {
		t.Error("Rank: major of 2.0.0 is not 2")
	}
}

func TestNuGetMatchRange(t *testing.T) {
	m, ok := get(t, "nuget").(RangeMatcher)
	if !ok {
		t.Fatal("nuget is not a RangeMatcher")
	}
	for _, tt := range []struct {
		rng     string
		in, out []string
	}{
		{"1.0", []string{"1.0", "1.0.0.0", "7.0.0"}, []string{"0.9", "2.0.0-beta"}},
		{"[1.0]", []string{"1.0.0"}, []string{"1.0.1"}},
		{"[1.0,2.0)", []string{"1.0", "1.9.9"}, []string{"2.0", "0.9", "1.5.0-rc1"}},
		{"( 1.0 , 2.0 ]", []string{"1.0.1", "2.0"}, []string{"1.0", "2.0.1"}},
		{"(,2.0)", []string{"0.1", "1.9"}, []string{"2.0"}},
		{"[1.0,)", []string{"1.0", "99"}, []string{"0.9"}},
		{"[1.0.0-beta,2.0)", []string{"1.0.0-rc", "1.5"}, []string{"1.0.0-alpha", "2.0"}},
		{"1.*", []string{"1.0", "1.9.3"}, []string{"2.0", "0.9", "1.2.0-rc1"}},
		{"1.2.*", []string{"1.2", "1.2.7.1"}, []string{"1.3.0", "1.1.0"}},
		{"*", []string{"0.0.1", "13.0.3"}, []string{"1.0.0-rc1", "x"}},
		{"*-*", []string{"1.0.0-rc1", "1.0.0"}, nil},
		{"1.*-*", []string{"1.0.0-rc1", "1.4"}, []string{"2.0.0-rc1"}},
		{"1.0.0-*", []string{"1.0.0-rc1", "1.0.0"}, []string{"1.0.1", "1.0.1-rc1"}},
		{"1.0.0-rc*", []string{"1.0.0-rc1", "1.0.0-RC.2"}, []string{"1.0.0-beta", "1.0.0"}},
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
	for _, bad := range []string{"", " ", "latest", "[1.0", "1.0)", "(1.0)", "[1.0)", "[]", "[,]", "[2.0,1.0]", "(1.0,1.0]",
		"[1.0,2.0,3.0]", "[x,2.0]", "[1.*,2.0)", "1.*.3", "1.2.3.4.*", "1.*-rc*", "1.0-r*c", "1.0-*-*", "1.0.0-a..*", "x.*"} {
		if _, err := m.MatchRange(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestParseNuGetRange(t *testing.T) {
	r, err := ParseNuGetRange(" [ 1.0 , 2.0 ) ")
	if err != nil || r.Min != "1.0" || r.Max != "2.0" || !r.MinIncl || r.MaxIncl || r.Bare || r.Exact() ||
		r.MinSpan != (dep.Span{Start: 3, End: 6}) || r.MaxSpan != (dep.Span{Start: 9, End: 12}) {
		t.Errorf("interval = %+v, %v", r, err)
	}
	if r, err := ParseNuGetRange("[13.0.3]"); err != nil || !r.Exact() || r.MinSpan != (dep.Span{Start: 1, End: 7}) {
		t.Errorf("exact = %+v, %v", r, err)
	}
	if r, err := ParseNuGetRange("13.0.3"); err != nil || !r.Bare || r.Exact() || r.Max != "" {
		t.Errorf("bare = %+v, %v", r, err)
	}
	if r, err := ParseNuGetRange("1.2.*"); err != nil || r.Float != "1.2.*" || !slices.Equal(r.Prefix, []uint64{1, 2}) || !r.Star || r.FloatPre {
		t.Errorf("float = %+v, %v", r, err)
	}
	if r, err := ParseNuGetRange("1.0.0-rc.*"); err != nil || r.Star || r.PrePrefix != "rc." || !r.FloatPre {
		t.Errorf("float pre = %+v, %v", r, err)
	}
	if parts, pre, ok := NuGetParts("1.2-rc"); !ok || !slices.Equal(parts, []uint64{1, 2}) || pre != "rc" {
		t.Errorf("NuGetParts = %v %q %v", parts, pre, ok)
	}
	if _, _, ok := NuGetParts("x"); ok {
		t.Error("NuGetParts(x) ok")
	}
}
