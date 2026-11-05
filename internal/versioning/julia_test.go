package versioning

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// juliaVersions are the versions TestJuliaMatchRange checks every entry against.
var juliaVersions = strings.Fields("0.0.0 0.0.3 0.0.4 0.1.0 0.2.2 0.2.3 0.2.9 0.3.0 0.21.0 0.21.9 0.22.0 0.7.0 1.0.0 1.1.0 1.1.9 1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0 2.0.0 2.9.9 3.0.0")

// TestJuliaMatchRange checks compat entries against the admitted versions Pkg computes
// (`VersionNumber(v) in Pkg.Versions.semver_spec(entry)`, Julia 1.12.7).
func TestJuliaMatchRange(t *testing.T) {
	m, ok := get(t, "julia").(RangeMatcher)
	if !ok {
		t.Fatal("julia is not a RangeMatcher")
	}
	for rng, want := range map[string]string{
		"1.2.3":        "1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		"^1.2.3":       "1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		"0.2.3":        "0.2.3 0.2.9",
		"0.0.3":        "0.0.3",
		"0":            "0.0.0 0.0.3 0.0.4 0.1.0 0.2.2 0.2.3 0.2.9 0.3.0 0.21.0 0.21.9 0.22.0 0.7.0",
		"0.0":          "0.0.0 0.0.3 0.0.4",
		"1":            "1.0.0 1.1.0 1.1.9 1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		"0.21, 1":      "0.21.0 0.21.9 1.0.0 1.1.0 1.1.9 1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		"~1.2.3":       "1.2.3 1.2.9",
		"~1.2":         "1.2.0 1.2.3 1.2.9",
		"~1":           "1.0.0 1.1.0 1.1.9 1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		"~0.0.3":       "0.0.3 0.0.4",
		"=1.2":         "1.2.0", // not 1.2.x
		"=1":           "1.0.0",
		"=1.2.3":       "1.2.3",
		">= 1.2":       "1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0 2.0.0 2.9.9 3.0.0",
		"≥1.2":         "1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0 2.0.0 2.9.9 3.0.0",
		"< 1.2":        "0.0.0 0.0.3 0.0.4 0.1.0 0.2.2 0.2.3 0.2.9 0.3.0 0.21.0 0.21.9 0.22.0 0.7.0 1.0.0 1.1.0 1.1.9",
		"<1":           "0.0.0 0.0.3 0.0.4 0.1.0 0.2.2 0.2.3 0.2.9 0.3.0 0.21.0 0.21.9 0.22.0 0.7.0",
		"<0.2.3":       "0.0.0 0.0.3 0.0.4 0.1.0 0.2.2",
		"1.2 - 2":      "1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0 2.0.0 2.9.9",
		"0.7 - 1.3":    "0.21.0 0.21.9 0.22.0 0.7.0 1.0.0 1.1.0 1.1.9 1.2.0 1.2.3 1.2.9 1.3.0 1.3.5",
		"v1.2":         "1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		"~1.0, ^1.1.1": "1.0.0 1.1.9 1.2.0 1.2.3 1.2.9 1.3.0 1.3.5 1.4.0 1.99.0",
		">=0":          strings.Join(juliaVersions, " "),
	} {
		match, err := m.MatchRange(rng)
		if err != nil {
			t.Errorf("MatchRange(%q): %v", rng, err)
			continue
		}
		var got []string
		for _, v := range juliaVersions {
			if match(v) {
				got = append(got, v)
			}
		}
		if strings.Join(got, " ") != want {
			t.Errorf("MatchRange(%q) admits %v, want %s", rng, got, want)
		}
	}
	match, _ := m.MatchRange("1.2")
	if !match("1.2.3+1") || !match("1.3.0-rc1") || match("junk") || match("2.0.0-DEV") {
		t.Error("prereleases and builds count as their release")
	}
	// Pkg rejects all of these (semver_spec throws).
	for _, rng := range []string{"> 1", "<= 2", "1.x", "*", "", "0.0.0", "^ 1", "1.2.3.4", "1-2", "1,", "=0.0.0", "1.2 - x", "x - 1", "v", "99999999999999999999"} {
		if _, err := m.MatchRange(rng); err == nil {
			t.Errorf("MatchRange(%q) = nil error", rng)
		}
	}
}

func TestParseJuliaCompat(t *testing.T) {
	terms, err := ParseJuliaCompat(" ~0.5 ,1.2 - v2.0, >= 3")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		op, lo, hi string
		text       string
	}{{"~", "0.5", "", "~0.5"}, {"-", "1.2", "2.0", "1.2 - v2.0"}, {">=", "3", "", ">= 3"}}
	if len(terms) != len(want) {
		t.Fatalf("terms = %+v", terms)
	}
	src := " ~0.5 ,1.2 - v2.0, >= 3"
	for i, w := range want {
		tt := terms[i]
		if tt.Op != w.op || tt.Lo.String() != w.lo || w.hi != "" && tt.Hi.String() != w.hi || src[tt.Start:tt.End] != w.text {
			t.Errorf("term %d = %+v (%q), want %+v", i, tt, src[tt.Start:tt.End], w)
		}
		if src[tt.Lo.Off:tt.Lo.End] != w.lo {
			t.Errorf("term %d Lo bytes = %q", i, src[tt.Lo.Off:tt.Lo.End])
		}
	}
	if terms[1].Hi.Off != strings.Index(src, "2.0") || !terms[0].Floor() || (JuliaTerm{Op: "<"}).Floor() {
		t.Error("bound offsets / Floor")
	}
	if hi, ok := terms[0].Upper(); !ok || hi != [3]uint64{0, 6, 0} {
		t.Errorf("Upper(~0.5) = %v, %v", hi, ok)
	}
	if _, ok := terms[2].Upper(); ok {
		t.Error("Upper(>= 3) is bounded")
	}
	if !terms[0].Below([3]uint64{0, 6, 0}) || terms[0].Below([3]uint64{0, 5, 9}) || terms[2].Below([3]uint64{9, 0, 0}) {
		t.Error("Below")
	}
	if p, ok := JuliaParts("1.2.11+1"); !ok || p != [3]uint64{1, 2, 11} {
		t.Errorf("JuliaParts = %v, %v", p, ok)
	}
	if _, ok := JuliaParts("v1"); ok {
		t.Error("JuliaParts(v1)")
	}
}

func TestJuliaCompareAndKind(t *testing.T) {
	s := get(t, "julia")
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.2.11+1", "1.2.11+0", 1}, {"1.2.11+0", "1.2.11", 1}, {"1.2.11+10", "1.2.11+9", 1}, {"1.2.11+a", "1.2.11+1", 1},
		{"1.2.11+1.1", "1.2.11+1", 1}, {"1.2.11+a", "1.2.11+b", -1}, {"1.2.11+1", "1.2.11+1", 0},
		{"1.2.12", "1.2.11+5", 1}, {"1.0.0-rc1", "1.0.0", -1}, {"1", "1.0.0", 0}, {"0.21", "0.21.0", 0},
	} {
		if got := s.Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := s.Compare(tt.b, tt.a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
	for _, tt := range []struct {
		cur, next string
		want      dep.UpdateType
	}{
		{"0.21", "0.22.0", dep.Major}, {"0.21", "0.21.4", dep.Patch}, {"1", "1.4.0", dep.Minor}, {"1.2.3", "2.0.0", dep.Major},
		{"1.2.11+0", "1.2.11+1", dep.Patch}, {"0.0.3", "0.0.4", dep.Major},
	} {
		if got := s.Kind(tt.cur, tt.next); got != tt.want {
			t.Errorf("Kind(%s, %s) = %s, want %s", tt.cur, tt.next, got, tt.want)
		}
	}
	runBest(t, "julia", []bestCase{
		{"1", "2.1.0", []string{"1.0.5", "1.4.0", "2.0.0", "2.1.0", "3.0.0-rc1"}, nil},
		{"0.21", "0.21.9", []string{"0.21.1", "0.21.9", "0.22.0"}, func(v string) bool { return v < "0.22" }},
		{"3.0.0-rc1", "3.0.0-rc2", []string{"3.0.0-rc2", "2.0.0"}, nil},
		{"1.2.11+0", "1.2.11+2", []string{"1.2.11+1", "1.2.11+2", "1.2.11"}, nil},
	})
	if r := get(t, "julia").Rank("1", []string{"2.0.0", "3.1.0"}, nil, nil); r.Len() != 2 || r.At(0).Major != "3" {
		t.Errorf("Rank major bucket = %+v", r.At(0))
	}
}
