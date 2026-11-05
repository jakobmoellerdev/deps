package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The expectations below were checked against Gem::Version and Gem::Requirement (Ruby 4.0).

func TestParseRuby(t *testing.T) {
	for s, ok := range map[string]bool{
		"1": true, "1.2.3": true, "2.241.2": true, "0.7.20261007": true, "1.2.3.4.5": true,
		"1.0.0.rc1": true, "1.0.0rc1": true, "1.a": true, "1.0.0-beta.2": true, "1.0-a--b": true, "1.0.a-b": true,
		"": false, "1a": false, "1.": false, ".1": false, "1..2": false, "1.0-": false, "1.0-.a": false,
		"1.0-a.": false, "1.0+b": false, "v1": false, "1 0": false, "99999999999999999999": false,
		"1.2.3.4.5.6.7.8.9.10.11": false,
	} {
		if _, got := parseRuby(s); got != ok {
			t.Errorf("parseRuby(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareRuby(t *testing.T) {
	s := get(t, "ruby")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0.0.rc1", "1.0.0", -1}, {"1.0.0.rc1", "1.0.0.rc2", -1}, {"1.0.0.beta", "1.0.0.rc", -1},
		{"1.0.0-beta", "1.0.0.pre.beta", 0}, {"1.2", "1.2.0", 0}, {"1.10", "1.9", 1}, {"1.0.a", "1.0", -1},
		{"1.0.0.1", "1.0.0", 1}, {"1.0.0.pre", "1.0.0.rc", -1}, {"2.0.0.a10", "2.0.0.a9", 1},
		{"1.0.0rc1", "1.0.0.rc1", 0}, {"1.rc1", "1.0.0.rc1", 0}, {"1.0-a--b", "1.0.pre.a.pre.pre.b", 0},
		{"1.0.0.RC1", "1.0.0.rc1", -1}, {"0", "0.0.0", 0}, {"1.0.0.0.1", "1.0.0.1", -1}, {"7.0.8.4", "7.0.8", 1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestRubyBest(t *testing.T) {
	names := []string{"2.240.1", "2.241.0", "2.241.1", "2.241.2", "2.242.0.rc1", "3.0.0.beta", "7.0.8", "7.0.8.4", "7.1.0", "8.0.1"}
	runBest(t, "ruby", []bestCase{
		{"2.241.1", "8.0.1", names, nil},
		{"2.241.1", "2.241.2", names, func(v string) bool { return v < "2.242" }},
		{"7.0.8", "7.0.8.4", names, func(v string) bool { return v < "7.1" }}, // a fourth segment is a patch
		{"2.242.0.rc1", "8.0.1", names, nil},                                  // from a prerelease
		{"3.0.0.alpha", "3.0.0.beta", names, func(v string) bool { return v < "7" }},
		{"8.0.1", "", names, nil},
		{"7.1", "8.0.1", names, nil}, // 7.1.0 equals 7.1
		{"bad", "", names, nil},
		{"1.0", "", []string{"junk", "v1.1"}, nil},
	})
}

func TestRubyKindAndSemver(t *testing.T) {
	s := get(t, "ruby")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"2.241.1", "2.241.2", dep.Patch}, {"2.241.1", "2.242.0", dep.Minor}, {"2.241.1", "3.0.0", dep.Major},
		{"7.0.8", "7.0.8.4", dep.Patch}, {"7.1", "7.2.0", dep.Minor}, {"7", "7.0.1", dep.Patch},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	if s.Compatible("1.0.0", "1.1.0.rc1") || !s.Compatible("1.0.0.rc1", "1.1.0.rc1") || s.Compatible("1.0.0", "1.0") {
		t.Error("Compatible")
	}
	for v, want := range map[string]string{"1.2": "1.2.0", "2.241.2.1": "2.241.2", "1.0.0.rc1": "1.0.0-rc1", "1.0.0-beta.2": "1.0.0-beta.2"} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
}

func TestRubyMatchRange(t *testing.T) {
	m, ok := get(t, "ruby").(RangeMatcher)
	if !ok {
		t.Fatal("ruby is not a RangeMatcher")
	}
	for _, tt := range []struct {
		rng     string
		in, out []string
	}{
		{"~> 7.1", []string{"7.1", "7.1.0", "7.9.9"}, []string{"8.0.0", "7.0.9", "7.5.0.rc1"}},
		{"~> 7.1.2", []string{"7.1.2", "7.1.9", "7.1.9.1"}, []string{"7.2.0", "7.1.1"}},
		{"~> 1", []string{"1.0", "1.9"}, []string{"2.0", "0.9"}},
		{"~> 0", []string{"0.5"}, []string{"1.0"}},
		{"~>7.1, >= 7.1.2", []string{"7.1.2", "7.4.0"}, []string{"7.1.1", "8.0.0"}},
		{"2.241.2", []string{"2.241.2", "2.241.2.0"}, []string{"2.241.3", "2.241.2.1"}},
		{"= 1.2", []string{"1.2.0"}, []string{"1.2.1"}},
		{"!= 1.2, >= 1.0", []string{"1.2.1", "1.0"}, []string{"1.2", "0.9"}},
		{">= 0", []string{"0.0.1", "13.4.2"}, []string{"1.0.0.rc1", "x"}},
		{"> 1.0, < 2, <= 1.5", []string{"1.5", "1.0.1"}, []string{"1.0", "1.5.1"}},
		{"~> 1.0.rc1", []string{"1.0.0", "1.0.rc2", "1.9"}, []string{"2.0", "0.9"}}, // a prerelease requirement admits prereleases
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
	for _, bad := range []string{"", "latest", ">= 1.0,", "~> x", "=> 1.0", ">= v1"} {
		if _, err := m.MatchRange(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestParseRubyRequirement(t *testing.T) {
	for in, want := range map[string]RubyClause{
		"~> 7.1": {"~>", "7.1", 3}, "2.241.2": {"", "2.241.2", 0}, " >=1.2 ": {">=", "1.2", 3}, "=  1.0.0.rc1": {"=", "1.0.0.rc1", 3},
	} {
		if got, err := ParseRubyRequirement(in); err != nil || got != want {
			t.Errorf("ParseRubyRequirement(%q) = %+v, %v, want %+v", in, got, err, want)
		}
	}
}

func TestFitRuby(t *testing.T) {
	for _, tt := range []struct{ cur, next, want string }{
		{"7.1", "8.0.1", "8.0"}, {"1.2.0", "2.0", "2.0.0"}, {"2", "3.4.1", "3"}, {"2.241.2", "2.242.0.1", "2.242.0"},
		{"7.1", "8.0.0.rc1", "8.0.0.rc1"}, {"x", "1.0.0", "1.0.0"}, {"1.0", "y", "y"}, {"1.0.rc1", "1.1.0", "1.1.0"},
	} {
		if got := FitRuby(tt.cur, tt.next); got != tt.want {
			t.Errorf("FitRuby(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
}
