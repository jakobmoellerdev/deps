package versioning

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"

	msemver "github.com/Masterminds/semver/v3"
)

// semverRangeCases are hand-written edge cases of the hand-parsed grammar and of the
// Masterminds fallback, besides the real-world ranges in testdata/semver-ranges.txt.
var semverRangeCases = []string{
	"", " ", "*", "x", "X", "v1", "V1.2.3", "1", "1.2", "1.2.3", "v1.2.3", "=1.2.3", "= 1.2.3",
	"1.x", "1.X", "1.*", "1.2.x", "x.2.3", "1.x.3", "*.*.*", "1.x.x", "^1", "^1.2", "^1.2.3",
	"^0", "^0.0", "^0.0.3", "^0.2", "^0.2.3", "^0.x", "^*", "~1", "~1.2", "~1.2.3", "~0",
	"~0.0.0", "~>1.2", "~> 1.2.3", ">1", ">1.2", ">1.2.3", ">*", ">1.x", "<1", "<1.2", "<1.2.3",
	">=1.2.3", "=>1.2.3", "<=1.2.3", "=<1.2.3", "<=1", "<=1.2", "<=1.x", "<=*", ">= 1.2, < 2",
	">=1.2.3 <2", ">=1.2.3,<2", ">=1.2.3 , <2", ">=1.2.3,", ">=1.2.3, ", ",>=1.2.3",
	">=1.2.3,,<2", ">=1.2.3<2", "1.2.3 1.2.4", "1.x || 2.x", "1.x||2.x", "^1.0.0 ||  ^2.0.0",
	"1.2.3 ||", "|| 1.2.3", "1 | 2", "1.2.3 - 2.3.4", "1 - 2", "1.0 - 2.1", "1.2.3 -beta",
	"1.2.3-beta", "1.2.3-beta.1", "^1.2.3-beta.1", ">=1.2.3-0 <2", "1.2.3-01", "1.2.3-a.01",
	"1.2.3+build", "1.2.3-rc.1+build.5", "1.2.3+a-b", "1.2.3+a+b", "1.2-beta", "1.x-beta",
	"*-beta", "01.2.3", "1.02.3", "1.2.03", "00", "1.2.3.4", "1.", "1..2", "x1", "1x",
	"!=1.2.3", "!=1.x", ">=1.2.3\n<2", ">=1.2.3\t<2", "\t^1.2.3\t", "> =1.2.3", ">= v1.2.3",
	"99999999999999999999", "1.99999999999999999999", "1.2.99999999999999999999",
	"99999999999999999999.x", "^18 || ^20 || >=22", "latest", "next", "1.2.3 || latest",
	"|| - 0", "1.0 ||1.1 - 2.0", "0 - 0- 0|| 0 - 0", // hyphen ranges reaching across the `||`
	"X.0X-0", // a dirty version whose parts mix digits and wildcards
	strings.Repeat("1.x || ", 32) + "1.x", strings.Repeat("1", 600),
}

// semverRangeVersions are the versions the parity test checks every range against.
func semverRangeVersions() []string {
	vs := []string{
		"0.0.0", "0.0.1", "0.0.3", "0.0.4", "0.1.0", "0.2.3", "0.2.9", "0.3.0", "1.0.0-0",
		"1.0.0-alpha", "1.0.0-beta.2", "1.2.3-beta", "1.2.3-beta.1", "1.2.3-beta.2", "1.2.3-0",
		"1.2.3+build", "v1.2.3", "v1.5.0", "1.2", "1", "v2", "2.0.0-rc.1", "2.0.0-canary.44",
		"15.0.0-canary.152", "19.0.0-rc-94e652d5-20240912", "1.2.3-01", "not-a-version", "",
		"99999999999999999999.0.0",
	}
	for major := range 4 {
		for _, minor := range []int{0, 1, 2, 3, 9} {
			for _, patch := range []int{0, 2, 3, 4} {
				vs = append(vs, fmt.Sprintf("%d.%d.%d", major, minor, patch))
			}
		}
	}
	for _, major := range []int{4, 5, 7, 13, 14, 15, 16, 18, 19, 20, 22, 30} {
		vs = append(vs, fmt.Sprintf("%d.0.0", major), fmt.Sprintf("%d.3.1", major))
	}
	return vs
}

func readSemverRanges(t testing.TB) []string {
	t.Helper()
	f, err := os.Open("testdata/semver-ranges.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkSemverRangeParity compares NewSemverRange and semverScheme.MatchRange for rng against
// Masterminds, the oracle: the same error, and per version the same Check, and the same match
// as the engine's Masterminds path (NewConstraint, Scheme.Semver, Check). A prerelease of a
// range naming one is left to TestSemverPrerelease, Masterminds admitting more of them than
// npm (semverGroup.pre).
func checkSemverRangeParity(t testing.TB, rng string, versions []string) {
	t.Helper()
	want, wantErr := msemver.NewConstraint(rng)
	got, err := NewSemverRange(rng)
	if fmt.Sprint(err) != fmt.Sprint(wantErr) {
		t.Fatalf("NewSemverRange(%q) error = %v, want %v", rng, err, wantErr)
	}
	match, mErr := schemes["semver"].(RangeMatcher).MatchRange(rng)
	if fmt.Sprint(mErr) != fmt.Sprint(wantErr) {
		t.Fatalf("MatchRange(%q) error = %v, want %v", rng, mErr, wantErr)
	}
	if wantErr != nil {
		return
	}
	pre := false
	for i := range got.groups {
		pre = pre || len(got.groups[i].pre) > 0
	}
	s := schemes["semver"]
	for _, v := range versions {
		sv := s.Semver(v)
		if pre && sv != nil && sv.Prerelease() != "" {
			continue
		}
		if w := sv != nil && want.Check(sv); match(v) != w {
			t.Errorf("MatchRange(%q)(%q) = %v, want %v", rng, v, !w, w)
		}
		mv, err := msemver.NewVersion(v)
		if err != nil || pre && mv.Prerelease() != "" {
			continue
		}
		if w := want.Check(mv); got.Check(mv) != w {
			t.Errorf("NewSemverRange(%q).Check(%q) = %v, want %v", rng, v, !w, w)
		}
	}
}

// handParsed reports whether every alternative of r is parsed by the package itself.
func handParsed(r *SemverRange) bool {
	for i := range r.groups {
		if r.groups[i].ms != nil {
			return false
		}
	}
	return true
}

// TestSemverRangeParity checks the hand-parsed ranges against Masterminds over real-world
// npm ranges and edge cases, and that the common grammar does not fall back to Masterminds.
func TestSemverRangeParity(t *testing.T) {
	ranges := append(readSemverRanges(t), semverRangeCases...)
	versions := semverRangeVersions()
	for _, rng := range ranges {
		if strings.Contains(rng, "-") {
			versions = append(versions, rng) // the prereleases ranges pin
		}
	}
	fast := 0
	for _, rng := range ranges {
		checkSemverRangeParity(t, rng, versions)
		if r, err := NewSemverRange(rng); err == nil && handParsed(r) {
			fast++
		}
	}
	t.Logf("%d of %d ranges hand-parsed", fast, len(ranges))
	for _, rng := range []string{"^1.2.3", "~1.2", "1.x || 2.x", ">= 1.2, < 2", "15.0.0-canary.152", "*", ">=18"} {
		if r, err := NewSemverRange(rng); err != nil || !handParsed(r) {
			t.Errorf("%q falls back to Masterminds", rng)
		}
	}
}

// TestSemverPrerelease checks the prerelease rule of node-semver (npm): a prerelease
// satisfies an alternative only when one of its comparators names a prerelease of the same
// [major, minor, patch] (semver/classes/range.js testSet, README "Prerelease Tags"). The
// expected values are `semver -r <range> <version>` of node-semver 7.
func TestSemverPrerelease(t *testing.T) {
	for _, c := range []struct {
		rng, v string
		want   bool
	}{
		{">=1.0.0-alpha.1 <2.0.0", "1.5.0-rc.1", false}, // Masterminds admits it
		{">=1.0.0-alpha.1 <2.0.0", "1.0.0-alpha.2", true},
		{">=1.0.0-alpha.1 <2.0.0", "1.0.0-alpha.0", false},
		{">=1.0.0-alpha.1 <2.0.0", "1.5.0", true},
		{"^1.0.0-beta.1", "1.5.0-rc.1", false},
		{"^1.0.0-beta.1", "1.0.0-rc.1", true},
		{"^1.0.0-beta.1", "1.2.0", true},
		{"~1.2.3-beta.1", "1.2.3-beta.2", true},
		{"~1.2.3-beta.1", "1.2.4-beta.1", false},
		{">=1.2.3-0 <2", "1.9.0-beta", false},
		{">=1.0.0", "1.5.0-rc.1", false},
		{"^1.0.0-beta.1 || ^2.0.0-rc.1", "2.0.0-rc.2", true},
		{"^1.0.0-beta.1 || ^2.0.0-rc.1", "2.5.0-rc.2", false},
		// Hyphen ranges, which Masterminds parses for the package.
		{"1.2.3-beta.1 - 2.0.0", "1.2.3-beta.2", true},
		{"1.2.3-beta.1 - 2.0.0", "1.5.0-rc.1", false},
		{"1.2.3 - 2.0.0", "1.5.0-rc.1", false},
	} {
		r, err := NewSemverRange(c.rng)
		if err != nil {
			t.Fatalf("NewSemverRange(%q): %v", c.rng, err)
		}
		v, ok := parseSemver(c.v)
		if !ok {
			t.Fatalf("parseSemver(%q) failed", c.v)
		}
		if got := r.Check(v.semver()); got != c.want {
			t.Errorf("NewSemverRange(%q).Check(%q) = %v, want %v", c.rng, c.v, got, c.want)
		}
		match, err := schemes["semver"].(RangeMatcher).MatchRange(c.rng)
		if err != nil {
			t.Fatalf("MatchRange(%q): %v", c.rng, err)
		}
		if got := match(c.v); got != c.want {
			t.Errorf("MatchRange(%q)(%q) = %v, want %v", c.rng, c.v, got, c.want)
		}
	}
}

// FuzzSemverRange checks NewSemverRange and MatchRange against Masterminds for any range
// and version.
func FuzzSemverRange(f *testing.F) {
	for _, rng := range semverRangeCases {
		f.Add(rng, "1.2.3")
	}
	for _, v := range semverRangeVersions() {
		f.Add("^1.2.3 || >=2.0.0-rc.1 <3", v)
	}
	f.Fuzz(func(t *testing.T, rng, v string) {
		checkSemverRangeParity(t, rng, []string{v, "1.2.3", "0.0.0", "1.2.3-beta.1"})
	})
}

// BenchmarkSemverMatchRange matches registry-sized release lists against common npm ranges,
// as the engine checks each candidate of a dependency against its range.
func BenchmarkSemverMatchRange(b *testing.B) {
	var cands []string
	for _, c := range benchCorpora() {
		if c.scheme == "semver" {
			cands = c.cands
		}
	}
	ranges := []string{"1.2.3", "^1.2.3", "~1.2.3", ">=1.2.3 <2", "1.x || 2.x", "^1.0.0-rc.1"}
	m := schemes["semver"].(RangeMatcher)
	b.ReportAllocs()
	for b.Loop() {
		for _, rng := range ranges {
			match, err := m.MatchRange(rng)
			if err != nil {
				b.Fatal(err)
			}
			for _, v := range cands {
				match(v)
			}
		}
	}
}
