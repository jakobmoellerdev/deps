package versioning

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The orderings and spec matches below were checked against conda's VersionOrder and
// VersionSpec (conda/models/version.py, conda 26.x).

func TestParseConda(t *testing.T) {
	for s, ok := range map[string]bool{
		"1": true, "1.2.3": true, "1.1.1w": true, "2024a": true, "9e": true, "1.0.0rc1": true, "1!2.0": true,
		"1.0+abc.7": true, "1.0_1": true, "1.0-1": true, "1.0.dev1": true, "0.4.1.RC": true, "20230101.1": true,
		"": false, "a1": false, "latest": false, "1..2": false, "1.": false, ".1": false, "1.0_": false, "1!": false,
		"x!1.0": false, "1!2!3": false, "1.0+": false, "1.0+a+b": false, "1.0-1_2": false, "1.*": false, "1 0": false,
		"1234567890123456789": false, "1.0+a!b": false,
	} {
		if _, got := parseConda(s); got != ok {
			t.Errorf("parseConda(%q) = %v, want %v", s, got, ok)
		}
	}
}

func TestCompareConda(t *testing.T) {
	s := get(t, "conda")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.1.1w", "1.1.1", -1}, {"1.1.1w", "1.1.1v", 1}, {"2024a", "2023c", 1}, {"9e", "9", -1},
		{"1.0.0rc1", "1.0.0", -1}, {"1.0.dev1", "1.0a1", 1}, {"1.0.post1", "1.0.1", -1}, {"1.2", "1.2.0", 0},
		{"1!1.0", "2.0", 1}, {"1.0+abc", "1.0", -1}, {"1.0_1", "1.0.1", 0}, {"1.0-1", "1.0.1", 0},
		{"2.5.4", "2.10", -1}, {"1.0a", "1.0", -1}, {"1.0.1a", "1.0.1", -1}, {"0.4.1.rc", "0.4.1.RC", 0},
		{"1.1dev1", "1.1.dev1", -1}, {"1.0.0", "1.0.0.0", 0}, {"1.0.post1", "1.0.post2", -1},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestCondaBest(t *testing.T) {
	names := []string{"1.19.2", "1.19.5", "1.26.0", "1.26.4", "2.0.0", "2.3.0", "2.3.4", "2.4.0rc1", "3.0.0dev0"}
	runBest(t, "conda", []bestCase{
		{"1.19.2", "2.3.4", names, nil},
		{"1.26", "2.3", names, nil}, // cut to the current precision; 2.3.4 has a non-zero third segment
		{"1.19.2", "1.19.5", names, func(v string) bool { return v < "1.2" }},
		{"2.4.0rc0", "3.0.0dev0", names, nil}, // from a prerelease
		{"2.3.4", "", names, nil},
		{"bad", "", names, nil},
		{"1.1.1v", "1.1.1w", []string{"1.1.1u", "1.1.1w"}, nil},
		{"1.1.1w", "1.1.1", []string{"1.1.1"}, nil}, // conda orders a word below a number: 1.1.1w < 1.1.1
		{"9d", "9e", []string{"9c", "9e"}, nil},
		{"2024a", "2025b", []string{"2023c", "2025a", "2025b"}, nil},
		{"1.0", "", []string{"junk", "v1.1", "1.1+local", "1.0.0.dev0"}, nil},
	})
	r := get(t, "conda").Rank("3.11", []string{"3.11.9", "3.12.0", "3.13.0", "3.13.1", "3.14.0"}, nil, nil)
	var got []string
	for i := range r.Len() {
		got = append(got, r.At(i).Version)
	}
	if want := []string{"3.14", "3.13", "3.12"}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Rank = %v, want %v", got, want)
	}
	if c := get(t, "conda").Rank("1.26", []string{"2.0.0"}, nil, nil).At(0); c.Major != "2" || c.Kind != dep.Major {
		t.Errorf("Rank major = %+v", c)
	}
}

func TestCondaKindAndSemver(t *testing.T) {
	s := get(t, "conda")
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"1.19.2", "1.19.5", dep.Patch}, {"1.19.2", "1.26.0", dep.Minor}, {"1.26", "2.0", dep.Major},
		{"3", "3.1", dep.Minor}, {"1.1.1v", "1.1.1w", dep.Patch}, {"1!1.0", "1!1.1", dep.Minor}, {"1.0", "1!1.0", dep.Major},
		{"2024a", "2024b", dep.Major}, {"x", "1.0", dep.Major},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	if s.Compatible("1.0.0", "1.1.0rc1") || !s.Compatible("1.0.0rc1", "1.1.0rc1") || s.Compatible("1.0", "1.1+cuda") || !s.Compatible("9d", "9e") {
		t.Error("Compatible")
	}
	for v, want := range map[string]string{"1.2": "1.2.0", "1.26.4.1": "1.26.4", "1.0.0rc1": "1.0.0-pre", "1.1.1w": "1.1.1", "1!2.0": "2.0.0", "2.0b1": "2.0.0-pre"} {
		if got := s.Semver(v); got == nil || got.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", v, got, want)
		}
	}
	if got := s.Semver("nope"); got != nil {
		t.Errorf("Semver(nope) = %v", got)
	}
	if c := (condaVersion{}).cut(1); c != "" {
		t.Errorf("cut = %q", c)
	}
	if p, _ := parseConda("1!2.0.0+cuda"); p.cut(2) != "1!2.0+cuda" {
		t.Errorf("cut = %q", p.cut(2))
	}
}

func TestCondaMatchRange(t *testing.T) {
	m, ok := get(t, "conda").(RangeMatcher)
	if !ok {
		t.Fatal("conda is not a RangeMatcher")
	}
	for _, tt := range []struct {
		rng     string
		in, out []string
	}{
		{"1.2.*", []string{"1.2.5", "1.2"}, []string{"1.20", "1.3"}},
		{"1.2*", []string{"1.2a", "1.2.1"}, []string{"1.20"}},
		{"=1.2", []string{"1.2.3", "1.2"}, []string{"1.3.0"}},
		{"=1.2.0", []string{"1.2", "1.2.0.1"}, []string{"1.2.1"}},
		{">=1.2,<2", []string{"1.9.9", "1.2"}, []string{"2.0", "1.1"}},
		{"~=1.4.5", []string{"1.4.9", "1.4.5"}, []string{"1.5.0", "1.4.4"}},
		{"~=2", []string{"2.0", "3.1"}, []string{"1.9"}},
		{"!=1.2.*", []string{"1.3"}, []string{"1.2.3"}},
		{"!=1.2", []string{"1.2.1"}, []string{"1.2.0"}},
		{"==1.2", []string{"1.2.0"}, []string{"1.2.1"}},
		{"1.2", []string{"1.2"}, []string{"1.2.1"}},
		{"1.1.1*", []string{"1.1.1w"}, []string{"1.1.2"}},
		{"1.1.1w", []string{"1.1.1W"}, []string{"1.1.1v"}},
		{"1.0.0rc*", []string{"1.0.0rc1"}, []string{"1.0.0"}},
		{">1.0, <=1.5", []string{"1.5", "1.0.1"}, []string{"1.0", "1.5.1", "junk"}},
		{"1!1.*", []string{"1!1.3"}, []string{"1.3"}},
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
	for _, bad := range []string{"", "*", "latest", ">=1.0,", ">=1.2.*", "1.2|1.3", "=>1.0"} {
		if _, err := m.MatchRange(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestParseCondaSpec(t *testing.T) {
	got, err := ParseCondaSpec(">=1.2, <2")
	want := []CondaClause{{Op: ">=", Version: "1.2", Start: 0, VerStart: 2, End: 5}, {Op: "<", Version: "2", Start: 7, VerStart: 8, End: 9}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ParseCondaSpec = %+v, %v", got, err)
	}
	for spec, fuzzy := range map[string]bool{"=1.2": true, "1.2.*": true, "==1.2.*": true, "1.2": false, "==1.2": false, "!=1.2.*": false, ">=1": false} {
		c, err := ParseCondaSpec(spec)
		if err != nil || c[0].Fuzzy() != fuzzy {
			t.Errorf("%q: Fuzzy = %v, %v", spec, c, err)
		}
	}
}
