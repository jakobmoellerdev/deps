package versioning

import (
	"maps"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// pep440Cases maps inputs to their normalized form ("" = invalid), checked against python's
// packaging.version.Version except for surrounding whitespace, which packaging strips.
var pep440Cases = map[string]string{
	"1.0":                             "1.0",
	"1":                               "1",
	"01.02":                           "1.2",
	"1.0.0.0.0.0.0.0.0.0":             "1.0.0.0.0.0.0.0.0.0",
	"v1.0":                            "1.0",
	"V1.0":                            "1.0",
	"0!1.0":                           "1.0",
	"v1!2.0":                          "1!2.0",
	"1.0-1":                           "1.0.post1",
	"1.0.0-Alpha.1":                   "1.0.0a1",
	"1.0RC1":                          "1.0rc1",
	"1.0c1":                           "1.0rc1",
	"1.0pre2":                         "1.0rc2",
	"1.0preview":                      "1.0rc0",
	"1.0.beta":                        "1.0b0",
	"1.0b":                            "1.0b0",
	"1.0a.":                           "1.0a0",
	"1.0a-":                           "1.0a0",
	"1.0a1-1":                         "1.0a1.post1",
	"1.0a.post1":                      "1.0a0.post1",
	"1.0.0.dev":                       "1.0.0.dev0",
	"1.0-dev":                         "1.0.dev0",
	"1.0dev-":                         "1.0.dev0",
	"1.0.DEV_3":                       "1.0.dev3",
	"1.0-r4":                          "1.0.post4",
	"1.0.r":                           "1.0.post0",
	"1.0rev":                          "1.0.post0",
	"1.0-r-":                          "1.0.post0",
	"1.0.post":                        "1.0.post0",
	"1.0.post.":                       "1.0.post0",
	"1.0_post_3":                      "1.0.post3",
	"1.0-post1":                       "1.0.post1",
	"1.0.post.dev1":                   "1.0.post0.dev1",
	"1.0a1.post2.dev3":                "1.0a1.post2.dev3",
	"1!1.0rc01.post02.dev03+Abc_05-X": "1!1.0rc1.post2.dev3+abc.5.x",
	"1.0+ubuntu-1":                    "1.0+ubuntu.1",
	"1.0+ABC-1_x":                     "1.0+abc.1.x",
	"1.0+abc.05":                      "1.0+abc.5",
	"1.0+00":                          "1.0+0",
	"1.0+5a.0b":                       "1.0+5a.0b",
	"1.0-":                            "",
	"1.0.dev1.post1":                  "",
	"1!":                              "",
	"!1":                              "",
	"1!1!1":                           "",
	"1.0+":                            "",
	"1.0+a..b":                        "",
	"1.0+-a":                          "",
	"1.0+a-":                          "",
	"1.0+a+b":                         "",
	"1.0+a/b":                         "",
	" 1.0":                            "",
	"1.0 ":                            "",
	"":                                "",
	"v":                               "",
	"vv1":                             "",
	"1.*":                             "",
	"abc":                             "",
	"1.":                              "",
	"1..0":                            "",
	"1.0.-1":                          "",
	"1.0_1":                           "",
	"1.0-1-1":                         "",
	"1.0a1b1":                         "",
	"1.0.dev1.dev2":                   "",
	"1.0post1post2":                   "",
	"1.0.0.0.0.0.0.0.0.0.0":           "", // more than maxPEP440Parts
	"99999999999999999999":            "",
	"99999999999999999999!1":          "",
	"1.99999999999999999999":          "",
	"1.0a99999999999999999999":        "",
	"1.0-99999999999999999999":        "",
	"1.0.post99999999999999999999":    "",
	"1.0.dev99999999999999999999":     "",
}

func TestNormalizePEP440(t *testing.T) {
	s := get(t, "pep440")
	for in, want := range pep440Cases {
		got, ok := NormalizePEP440(in)
		if got != want || ok != (want != "") {
			t.Errorf("NormalizePEP440(%q) = %q, %v, want %q", in, got, ok, want)
		}
		if s.Valid(in) != (want != "") {
			t.Errorf("Valid(%q) = %v", in, want == "")
		}
		if want == "" && s.Semver(in) != nil {
			t.Errorf("Semver(%q) non-nil", in)
		}
		if want != "" {
			if again, _ := NormalizePEP440(want); again != want {
				t.Errorf("NormalizePEP440(%q) = %q, not idempotent", want, again)
			}
			if s.Compare(in, want) != 0 {
				t.Errorf("Compare(%q, %q) != 0", in, want)
			}
		}
	}
}

func TestParsePEP440Allocs(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { _, _ = parsePEP440("1!1.2.3rc1.post2.dev3+ubuntu.1") }); n != 0 {
		t.Errorf("parsePEP440 allocates %v times", n)
	}
}

func TestComparePEP440(t *testing.T) {
	s := get(t, "pep440")
	// PEP 440's ordering example, then epochs, then local segment rules.
	for _, order := range [][]string{
		{
			"1.0.dev456", "1.0a1", "1.0a2.dev456", "1.0a12.dev456", "1.0a12", "1.0b1.dev456", "1.0b2", "1.0b2.post345.dev456",
			"1.0b2.post345", "1.0rc1.dev456", "1.0rc1", "1.0", "1.0+abc.5", "1.0+abc.7", "1.0+5", "1.0.post456.dev34",
			"1.0.post456", "1.0.15", "1.1.dev1", "2.0", "1!0.1", "1!1.0.dev0", "1!1.0", "2!0",
		},
		{"1.0+abc", "1.0+abc.5", "1.0+ABC.5.a", "1.0+abd", "1.0+0", "1.0+5.a", "1.0+5.0", "1.0+10"},
		{"1.0.dev1", "1.0a1.dev1", "1.0a1", "1.0rc1.post1", "1.0", "1.0.post1.dev1", "1.0.post1"},
	} {
		for i, a := range order {
			for j, b := range order {
				if got, want := s.Compare(a, b), cmpInt(i, j); got != want {
					t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
				}
			}
		}
	}
	for _, eq := range [][2]string{
		{"1.0", "1.0.0"}, {"1.0", "v1.0"}, {"1", "1.0.0.0"}, {"0!1.0", "1.0"}, {"1.0a", "1.0alpha0"},
		{"1.0-1", "1.0.post1"}, {"1.0+abc.05", "1.0+ABC.5"}, {"1.0+a-b", "1.0+a_b"},
	} {
		if s.Compare(eq[0], eq[1]) != 0 {
			t.Errorf("Compare(%s, %s) != 0", eq[0], eq[1])
		}
	}
	if s.Compare("1.0", "1.0+0") >= 0 {
		t.Error("1.0 >= 1.0+0")
	}
	if s.Compare("x", "1.0") == 0 {
		t.Error("Compare of invalid = 0")
	}
}

func cmpInt(i, j int) int {
	switch {
	case i < j:
		return -1
	case i > j:
		return 1
	}
	return 0
}

func TestPEP440Kind(t *testing.T) {
	s := get(t, "pep440")
	for p, want := range map[[2]string]dep.UpdateType{
		{"1.0", "1!0.1"}:       dep.Major,
		{"1.0", "2.0"}:         dep.Major,
		{"0.9", "1"}:           dep.Major,
		{"1", "1.1"}:           dep.Minor,
		{"4.2.11", "4.3"}:      dep.Minor,
		{"4.2.11", "4.2.12"}:   dep.Patch,
		{"4.2", "4.2.post1"}:   dep.Patch,
		{"2.0rc1", "2.0"}:      dep.Patch,
		{"1.2.3.4", "1.2.3.5"}: dep.Patch,
		{"x", "1.0"}:           dep.Major,
	} {
		if got := s.Kind(p[0], p[1]); got != want {
			t.Errorf("Kind(%s, %s) = %s, want %s", p[0], p[1], got, want)
		}
	}
}

func TestPEP440Best(t *testing.T) {
	django := []string{"3.2.25", "4.1", "4.2", "4.2.11", "4.2.16", "5.0", "5.0.post1", "5.1", "5.1.4", "5.2a1", "5.2.dev20240101", "5.2+local", "bad"}
	runBest(t, "pep440", []bestCase{
		{"4.2.11", "5.1.4", django, nil},
		{"4.2.11", "4.2.16", django, func(v string) bool { return v < "5" }},
		{"4.2", "5.1", django, nil},                     // 5.1.4 exceeds the precision
		{"4.2", "5.0", []string{"5.0.0", "4.3.1"}, nil}, // 5.0.0 is written at cur's precision
		{"4.2", "5.0.post1", []string{"5.0.0.post1"}, nil},
		{"4.2", "", []string{"1!5.0.0rc1"}, nil}, // only a prerelease cur accepts it
		{"4.2rc1", "1!5.0rc1", []string{"1!5.0.0rc1"}, nil},
		{"2", "3", []string{"3.0.0", "3.1"}, nil},
		{"4.2.11", "5.2", []string{"5.2"}, nil}, // fewer parts than cur stay as published
		{"2.0rc1", "2.0", []string{"2.0rc2", "2.0", "2.0.dev1"}, nil},
		{"2.0rc1", "2.0rc2", []string{"2.0rc2", "2.0.dev1"}, nil},
		{"2.0.dev1", "2.0a1", []string{"2.0a1", "2.0.dev0"}, nil},
		{"1.9", "1.9.post2", []string{"2.0rc1", "2.0.dev1", "1.9.post2", "1.9.post1"}, nil},
		{"1.9", "V1.10", []string{"V1.10", "1.10+local"}, nil},
		{"1.0+ubuntu.1", "1.0+ubuntu.2", []string{"1.0+ubuntu.2"}, nil},
		{"1.0", "1!0.1", []string{"1!0.1", "0.9"}, nil},
		{"1.0", "", []string{"1.0.0", "0.9", "1.0+x"}, nil},
		{"x", "", django, nil},
	})
}

func TestPEP440RejectReasons(t *testing.T) {
	got := map[string][]string{}
	accept := func(v string, _ dep.UpdateType) (bool, string) { return v != "4.4", "nope" }
	cands := []string{"bad", "4.3rc1", "4.3.dev1", "4.3+local", "4.2.1", "4.1", "4.2.0", "4.4", "4.3.0", "4.3.post1"}
	best, ok := get(t, "pep440").Best("4.2", cands, accept, func(v, reason string) { got[reason] = append(got[reason], v) })
	want := map[string][]string{
		"invalid": {"bad"}, "prerelease": {"4.3rc1", "4.3.dev1"}, "suffix": {"4.3+local"}, "precision": {"4.2.1"},
		"not newer": {"4.1", "4.2.0"}, "policy: nope": {"4.4"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) || best != "4.3.post1" || !ok {
		t.Errorf("Best = %q, %v, rejects = %v, want %v", best, ok, got, want)
	}
}

func TestPEP440Rank(t *testing.T) {
	s := get(t, "pep440")
	for _, tc := range []struct {
		cur   string
		cands []string
		want  []Candidate
	}{
		{"4.2", []string{"4.3.0", "5.0.0.post1", "1!1.0", "4.2.post1", "5.1.2"}, []Candidate{
			{"1!1.0", dep.Major, "1"}, {"5.0.post1", dep.Major, "5"}, {"4.3", dep.Minor, ""}, {"4.2.post1", dep.Patch, ""},
		}},
		{"4.2.11", []string{"5.2", "v4.3.0", "4.2.12"}, []Candidate{
			{"5.2", dep.Major, "5"}, {"v4.3.0", dep.Minor, ""}, {"4.2.12", dep.Patch, ""},
		}},
		{"x", []string{"1.0"}, nil},
	} {
		r := s.Rank(tc.cur, tc.cands, nil, nil)
		var got []Candidate
		for i := range r.Len() {
			got = append(got, r.At(i))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Rank(%s) = %v, want %v", tc.cur, got, tc.want)
		}
	}
}

func TestPEP440Compatible(t *testing.T) {
	s := get(t, "pep440")
	for p, want := range map[[2]string]bool{
		{"1.0", "1.1"}:        true,
		{"1.0", "2.0.post1"}:  true,
		{"1.0", "1.0.post1"}:  true,
		{"1.0", "1!0.1"}:      true,
		{"1.0", "1.0.0"}:      false,
		{"1.0", "1.1a1"}:      false,
		{"1.0", "1.1.dev1"}:   false,
		{"1.0", "1.1+x"}:      false,
		{"1.0", "1.1.1"}:      false,
		{"1.0", "0.9"}:        false,
		{"1.0a1", "1.0a2"}:    true,
		{"1.0.dev1", "1.0a1"}: true,
		{"x", "1.0"}:          false,
		{"1.0", "x"}:          false,
	} {
		if got := s.Compatible(p[0], p[1]); got != want {
			t.Errorf("Compatible(%s, %s) = %v, want %v", p[0], p[1], got, want)
		}
	}
}

func TestPEP440Semver(t *testing.T) {
	s := get(t, "pep440")
	for in, want := range map[string]string{
		"4.2":                 "4.2.0",
		"1!4.2.11.5":          "4.2.11",
		"1.0a1":               "1.0.0-a.1",
		"1.0b2":               "1.0.0-b.2",
		"1.0rc1.dev2":         "1.0.0-rc.1.dev.2",
		"1.0.dev3":            "1.0.0-dev.3",
		"1.0.post1+ubuntu.1":  "1.0.0",
		"1.0rc1.post1.dev2+x": "1.0.0-rc.1.dev.2",
	} {
		if sv := s.Semver(in); sv == nil || sv.String() != want {
			t.Errorf("Semver(%s) = %v, want %s", in, sv, want)
		}
	}
}
