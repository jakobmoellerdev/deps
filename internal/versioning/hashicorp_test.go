package versioning

import (
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestParseHashicorp(t *testing.T) {
	for s, ok := range map[string]bool{
		"5": true, "5.0": true, "v1.9.5": true, "1.10.0-beta1": true, "1.0.0-rc.1": true,
		"1.2-beta": false, "1.2.3.4": false, "1.2.3+meta": false, "1.2.3-": false, "": false, "v": false, "x1": false,
		"99999999999999999999": false,
	} {
		if _, got := parseHashicorp(s); got != ok {
			t.Errorf("parseHashicorp(%q) = %v", s, got)
		}
	}
}

func TestHashicorpBest(t *testing.T) {
	// Registry versions (no "v") mixed with GitHub release tags ("v").
	names := []string{"5.0.0", "5.30.0", "5.31.0", "5.31.2", "6.0.0-beta1", "6.0.0", "v6.2.1", "6.3.0-rc1"}
	runBest(t, "hashicorp", []bestCase{
		{"5.0", "6.2", names, nil},                                              // ~> 5.0 keeps two parts
		{"5.0", "5.31", names, func(v string) bool { return v < "6" }},          // allow < 6
		{"5.0.1", "6.2.1", names, nil},                                          // full precision, "v" dropped
		{"v5.0.0", "v6.2.1", names, nil},                                        // "v" kept
		{"5", "6", names, nil},                                                  // one part
		{"6.2", "", names, nil},                                                 // 6.2.1 cut to 6.2 is not newer; 6.3.0-rc1 is a prerelease
		{"6.0.0-beta1", "6.3.0-rc1", names, nil},                                // from a prerelease
		{"6.3.0-rc1", "", []string{"6.3.0-rc0", "6.2.9"}, nil},                  // older only
		{"bad", "", names, nil},                                                 // invalid current
		{"1.0", "", []string{"junk", "1.0.5"}, nil},                             // invalid candidates
		{"1.0.0", "1.1.0", []string{"1.1.0", "1.1"}, nil},                       // fewer parts than cur rank below
		{"1.0", "1.1", []string{"1.1.0-beta", "1.1.0", "1.1.1", "v1.1.3"}, nil}, // duplicates once
	})
}

func TestHashicorpRank(t *testing.T) {
	s := get(t, "hashicorp")
	rejected := map[string][]string{}
	r := s.Rank("5.0", []string{"5.0.3", "5.1.0", "5.1.4", "6.0.0", "7.0.0-alpha", "x"}, nil, func(v, why string) { rejected[why] = append(rejected[why], v) })
	var got []string
	for i := range r.Len() {
		c := r.At(i)
		got = append(got, c.Version+"/"+string(c.Kind)+"/"+c.Major)
	}
	if want := []string{"6.0/major/6", "5.1/minor/"}; !slices.Equal(got, want) {
		t.Errorf("Rank = %v, want %v", got, want)
	}
	if !slices.Equal(rejected["not newer"], []string{"5.0"}) || !slices.Equal(rejected["prerelease"], []string{"7.0.0-alpha"}) || !slices.Equal(rejected["invalid"], []string{"x"}) {
		t.Errorf("rejected = %v", rejected)
	}
}

func TestHashicorpCompatibleAndKind(t *testing.T) {
	s := get(t, "hashicorp")
	for _, tc := range []struct {
		cur, next string
		want      bool
	}{
		{"5.0", "5.1.0", true}, {"5.0", "5.0.9", false}, {"5.0", "6.0.0-rc1", false}, {"1.2.3", "v1.2.4", true}, {"x", "1.0.0", false},
	} {
		if got := s.Compatible(tc.cur, tc.next); got != tc.want {
			t.Errorf("Compatible(%s, %s) = %v", tc.cur, tc.next, got)
		}
	}
	for _, tc := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"5.0", "6.1", dep.Major}, {"5.0", "5.1", dep.Minor}, {"5.1.0", "5.1.2", dep.Patch}, {"5", "5.0.1", dep.Patch},
	} {
		if got := s.Kind(tc.cur, tc.next); got != tc.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tc.cur, tc.next, got, tc.want)
		}
	}
	if c := s.Compare("1.2", "1.2.0"); c >= 0 {
		t.Errorf("Compare(1.2, 1.2.0) = %d, want < 0", c)
	}
	if c := s.Compare("1.2.0-beta", "1.2.0"); c >= 0 {
		t.Errorf("Compare(1.2.0-beta, 1.2.0) = %d, want < 0", c)
	}
	if v := s.Semver("v1.9"); v == nil || v.String() != "1.9.0" {
		t.Errorf("Semver(v1.9) = %v", v)
	}
}

func TestHashicorpMatchRange(t *testing.T) {
	m, ok := get(t, "hashicorp").(RangeMatcher)
	if !ok {
		t.Fatal("hashicorp is not a RangeMatcher")
	}
	for _, tt := range []struct {
		rng string
		in  []string
		out []string
	}{
		{"~> 5.0", []string{"5.0.0", "5.46.2", "v5.9"}, []string{"4.9.9", "6.0.0", "5.1.0-beta1"}},
		{"~> 5.31.0", []string{"5.31.0", "5.31.9"}, []string{"5.32.0", "5.30.9"}},
		{"~> 1", []string{"1.0.0", "7.2.0"}, []string{"0.9.0"}},
		{">= 4.0, < 6.0", []string{"4.0.0", "5.99.0"}, []string{"6.0.0", "3.9.9"}},
		{"> 1.2, <= 1.4, != 1.3.0", []string{"1.2.1", "1.4.0", "1.3.1"}, []string{"1.2.0", "1.3.0", "1.4.1"}},
		{"= 1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{"1.2", []string{"1.2.0"}, []string{"1.2.1"}},
		{"1.0.0-rc1", []string{"1.0.0-rc1"}, []string{"1.0.0"}},
		{"< 2", []string{"1.9.9", "x"}[:1], []string{"2.0.0", "x"}},
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
	for _, bad := range []string{"", "latest", ">= 1.0,", "~> x"} {
		if _, err := m.MatchRange(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestParseHashicorpConstraint(t *testing.T) {
	cs, err := ParseHashicorpConstraint(" >= 1.2 ,<2.0,~>1.5.0")
	want := []HashicorpClause{{">=", "1.2", 4}, {"<", "2.0", 10}, {"~>", "1.5.0", 16}}
	if err != nil || !slices.Equal(cs, want) {
		t.Errorf("clauses = %+v, %v", cs, err)
	}
}

func TestFitHashicorp(t *testing.T) {
	for _, tt := range []struct{ cur, next, want string }{
		{"5.0", "6.2.1", "6.2"}, {"v1.5.7", "1.9.5", "v1.9.5"}, {"5", "6.1.0", "6"},
		{"5.0", "6.0.0-rc1", "6.0.0-rc1"}, {"x", "1.0.0", "1.0.0"}, {"1.0.0", "y", "y"},
	} {
		if got := FitHashicorp(tt.cur, tt.next); got != tt.want {
			t.Errorf("FitHashicorp(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
}
