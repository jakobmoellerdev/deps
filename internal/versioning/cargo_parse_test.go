package versioning

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// cargoSemverVersion is the former cargoVersion, which held every version as a Masterminds
// version; the cargo scheme must classify and order versions as it did.
type cargoSemverVersion struct {
	v *msemver.Version
	n int
}

func parseCargoSemver(s string) (cargoSemverVersion, bool) {
	var parts [3]uint64
	n, i := 0, 0
	for {
		j := i
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		if j == i || n == 3 || j-i > 1 && s[i] == '0' {
			return cargoSemverVersion{}, false
		}
		p, err := strconv.ParseUint(s[i:j], 10, 64)
		if err != nil {
			return cargoSemverVersion{}, false
		}
		parts[n], n, i = p, n+1, j
		if i == len(s) {
			return cargoSemverVersion{v: msemver.New(parts[0], parts[1], parts[2], "", ""), n: n}, true
		}
		if s[i] != '.' {
			break
		}
		i++
	}
	if n != 3 || s[i] != '-' && s[i] != '+' {
		return cargoSemverVersion{}, false
	}
	v, err := msemver.StrictNewVersion(s)
	if err != nil {
		return cargoSemverVersion{}, false
	}
	return cargoSemverVersion{v: v, n: 3}, true
}

func cargoSemverCompat(cur, next cargoSemverVersion) string {
	switch {
	case next.n != cur.n:
		return "precision"
	case next.v.Prerelease() != "" && cur.v.Prerelease() == "":
		return "prerelease"
	}
	return ""
}

func cargoSemverMajor(c cargoSemverVersion) string {
	v := c.v
	switch {
	case v.Major() > 0:
		return strconv.FormatUint(v.Major(), 10)
	case v.Minor() > 0:
		return "0." + strconv.FormatUint(v.Minor(), 10)
	}
	return "0.0." + strconv.FormatUint(v.Patch(), 10)
}

func cargoSemverKind(cur, next cargoSemverVersion) dep.UpdateType {
	if cargoSemverMajor(cur) != cargoSemverMajor(next) {
		return dep.Major
	}
	if cur.v.Major() > 0 && cur.v.Minor() != next.v.Minor() {
		return dep.Minor
	}
	return dep.Patch
}

// checkCargoPair compares parsing, semver, major, compat, order and kind of a and b with the
// former implementation.
func checkCargoPair(t *testing.T, a, b string) {
	t.Helper()
	x, okX := parseCargo(a)
	y, okY := parseCargo(b)
	ox, okOX := parseCargoSemver(a)
	oy, okOY := parseCargoSemver(b)
	if okX != okOX || okY != okOY {
		t.Fatalf("parseCargo(%q, %q) ok = %v %v, want %v %v", a, b, okX, okY, okOX, okOY)
	}
	if !okX || !okY {
		return
	}
	for _, p := range []struct {
		v cargoVersion
		o cargoSemverVersion
	}{{x, ox}, {y, oy}} {
		sv := p.v.semver()
		if sv.String() != p.o.v.String() || sv.Original() != p.o.v.Original() || sv.Compare(p.o.v) != 0 ||
			sv.Prerelease() != p.o.v.Prerelease() || sv.Metadata() != p.o.v.Metadata() {
			t.Fatalf("semver(%q) = %#v, want %#v", p.o.v.Original(), sv, p.o.v)
		}
		if got, want := cargoMajor(p.v), cargoSemverMajor(p.o); got != want {
			t.Fatalf("cargoMajor(%s) = %s, want %s", sv, got, want)
		}
	}
	if got, want := compareCargo(x, y), ox.v.Compare(oy.v); got != want {
		t.Fatalf("compareCargo(%q, %q) = %d, want %d", a, b, got, want)
	}
	if got, want := cargoCompat(x, y), cargoSemverCompat(ox, oy); got != want {
		t.Fatalf("cargoCompat(%q, %q) = %q, want %q", a, b, got, want)
	}
	if got, want := cargoKind(x, y), cargoSemverKind(ox, oy); got != want {
		t.Fatalf("cargoKind(%q, %q) = %s, want %s", a, b, got, want)
	}
}

// cargoParseCorpus is the versions of the Cargo and crate fixtures plus edge cases.
func cargoParseCorpus(t testing.TB) []string {
	corpus := []string{
		"0", "1", "1.2", "1.2.3", "0.0.3", "0.4.1", "01.2.3", "1.2.3.4", "1.2.3-rc.1", "1.2.3-rc.1+b",
		"1.2.3+build.5", "1.2.3-alpha", "1.2.3-alpha.1", "1.2.3-alpha.beta", "1.2.3-beta.11",
		"1.2.3-beta.2", "1.2.3-0", "1.2.3-01", "1.2-rc.1", "v1.2.3", "1.2.3-", "1.2.3+", "1..2",
		"18446744073709551615.0.0", "18446744073709551616.0.0", "1.2.3-rc.1+b.2",
	}
	re := regexp.MustCompile(`"(?:vers|version)"\s*:\s*"([^"]+)"|version = "([^"]+)"|= "([^"]+)"`)
	for _, glob := range []string{"../datasource/crate/testdata/*", "../manager/cargo/testdata/*"} {
		files, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				corpus = append(corpus, m[1]+m[2]+m[3])
			}
		}
	}
	return corpus
}

func TestCargoParity(t *testing.T) {
	corpus := cargoParseCorpus(t)
	if len(corpus) < 200 {
		t.Fatalf("corpus has %d versions, want the fixtures' versions", len(corpus))
	}
	for _, a := range corpus {
		for _, b := range corpus {
			checkCargoPair(t, a, b)
		}
	}
}

func FuzzCargo(f *testing.F) {
	corpus := cargoParseCorpus(f)
	for i, s := range corpus {
		f.Add(s, corpus[(i*7+3)%len(corpus)])
	}
	f.Fuzz(checkCargoPair)
}

// BenchmarkCargoRank ranks a crates.io-sized release list: 3000 releases with prereleases.
func BenchmarkCargoRank(b *testing.B) {
	var cands []string
	for i := range 3000 {
		maj, mnr, pat := i/600, i/30%20, i%30
		cands = append(cands, fmt.Sprintf("%d.%d.%d", maj, mnr, pat))
		if i%5 == 0 {
			cands = append(cands, fmt.Sprintf("%d.%d.%d-beta.%d", maj, mnr, pat+1, i%4))
		}
	}
	s, _ := Get("cargo")
	b.ReportAllocs()
	for b.Loop() {
		r := s.Rank("1.2.3", cands, nil, nil)
		for i := range min(r.Len(), 5) {
			_ = r.At(i)
		}
	}
}
