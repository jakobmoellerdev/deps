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
// as the engine's Masterminds path (NewConstraint, Scheme.Semver, Check).
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
	s := schemes["semver"]
	for _, v := range versions {
		sv := s.Semver(v)
		if w := sv != nil && want.Check(sv); match(v) != w {
			t.Errorf("MatchRange(%q)(%q) = %v, want %v", rng, v, !w, w)
		}
		mv, err := msemver.NewVersion(v)
		if err != nil {
			continue
		}
		if w := want.Check(mv); got.Check(mv) != w {
			t.Errorf("NewSemverRange(%q).Check(%q) = %v, want %v", rng, v, !w, w)
		}
	}
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
		if r, ok := parseSemverRange(rng); ok && r.ms == nil {
			fast++
		}
	}
	t.Logf("%d of %d ranges hand-parsed", fast, len(ranges))
	for _, rng := range []string{"^1.2.3", "~1.2", "1.x || 2.x", ">= 1.2, < 2", "15.0.0-canary.152", "*", ">=18"} {
		if _, ok := parseSemverRange(rng); !ok {
			t.Errorf("%q falls back to Masterminds", rng)
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
