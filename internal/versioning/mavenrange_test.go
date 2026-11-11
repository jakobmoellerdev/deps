package versioning

import "testing"

func TestParseMavenRange(t *testing.T) {
	for _, tc := range []struct {
		in    string
		ok    bool
		admit []string
		deny  []string
	}{
		{in: "[1.0,2.0)", ok: true, admit: []string{"1.0", "1.5", "1.9.9"}, deny: []string{"0.9", "2.0", "2.0.1"}},
		{in: "(,1.0]", ok: true, admit: []string{"0.1", "1.0"}, deny: []string{"1.0.1"}},
		{in: "[1.5,)", ok: true, admit: []string{"1.5", "99"}, deny: []string{"1.4"}},
		{in: "(1.0,2.0]", ok: true, admit: []string{"1.0.1", "2.0"}, deny: []string{"1.0", "2.1"}},
		{in: "[1.0]", ok: true, admit: []string{"1.0", "1"}, deny: []string{"1.1"}},
		{in: "(,1.0],[1.2,)", ok: true, admit: []string{"0.5", "1.2", "3"}, deny: []string{"1.1"}},
		{in: "[ 1.0 , 2.0 )", ok: true, admit: []string{"1.0"}, deny: []string{"2.0"}},
		{in: "[1.0"},
		{in: "(1.0)"},
		{in: "[,]"},
		{in: "[2.0,1.0)"},
		{in: "[1.0,1.0)"},
		{in: "[1.0,2.0) x"},
		{in: "1.0"},
		{in: "[1 0,2)"},
	} {
		r, err := ParseMavenRange(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err = %v", tc.in, err)
			continue
		}
		for _, v := range tc.admit {
			if !r.Admits(v) {
				t.Errorf("%q does not admit %s", tc.in, v)
			}
		}
		for _, v := range tc.deny {
			if r.Admits(v) {
				t.Errorf("%q admits %s", tc.in, v)
			}
		}
	}
	if r, _ := ParseMavenRange("[1,2)"); r.Admits("not a version!") {
		t.Error("admits an invalid version")
	}
}

// TestMavenRangeExtend follows Renovate's maven versioning getNewValue tests.
func TestMavenRangeExtend(t *testing.T) {
	for _, tc := range []struct{ in, v, want string }{
		{"[1.0]", "1.1", "[1.1]"},
		{"[1,2)", "4.3.2", "[4,5)"},
		{"[1,2-alpha)", "4.3.2", "[4,5-alpha)"},
		{"[1.0,2.0)", "2.5.1", "[1.0,2.6)"},
		{"[1.2.3,1.2.4)", "1.2.5", "[1.2.5,1.2.6)"},
		{"[1.0.0,1.2.23]", "1.2.24", "[1.0.0,1.2.24]"},
		{"[1.0.0,1.2.final]", "1.3.0", "[1.0.0,1.3.0]"},
		{"(,1.0]", "2.0.0", "(,2.0]"},
		{"(,1.0)", "1.0", "(,1.1)"},
		{"(,1.0],[1.2,1.5)", "1.7", "(,1.0],[1.2,1.8)"},
		{"[1.0,2.0)", "3.0-jre", "[1.0,3.1)"},
		{"[1.0,2.0)", "1.5", ""},    // admitted
		{"[1.0,)", "2.0", ""},       // admitted
		{"[2.0,)", "1.0", "[1.0,)"}, // below the floor: the floor moves
		{"[1.0,2.0)", "!", ""},
	} {
		r, err := ParseMavenRange(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		got, ok := r.Extend(tc.in, tc.v)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("Extend(%q, %s) = %q, %v, want %q", tc.in, tc.v, got, ok, tc.want)
		}
	}
}

// BenchmarkMavenInRange checks a registry-sized release list against the ranges of a
// dependency, as the maven, ant, clojure and sbt managers do for every candidate release
// (their InRange parses the range of the dep and asks it).
func BenchmarkMavenInRange(b *testing.B) {
	var cands []string
	for _, c := range benchCorpora() {
		if c.scheme == "maven" {
			cands = c.cands
		}
	}
	ranges := []string{"[1.0,2.0)", "(,1.0]", "[1.5,)", "[1.0]", "(,1.0],[1.2,)"}
	b.ReportAllocs()
	for b.Loop() {
		for _, rng := range ranges {
			for _, v := range cands {
				if r, err := ParseMavenRange(rng); err == nil {
					r.Admits(v)
				}
			}
		}
	}
}
