package versioning

import (
	"math/rand/v2"
	"strings"
	"testing"

	msemver "github.com/Masterminds/semver/v3"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// goCorpus returns structured Go versions (shorthand, prereleases, builds, pseudo-versions,
// leading zeros) plus random strings over the version alphabet.
func goCorpus() []string {
	var out []string
	pres := []string{"", "-rc.1", "-rc.2", "-rc.10", "-alpha", "-alpha.1", "-alpha.beta", "-beta.11", "-0", "-1", "-1a", "-x-y", "-01", "-rc..1", "-",
		"-0.20240101000000-abcdefabcdef", "-rc.1.0.20240101000000-abcdefabcdef", "-20240101000000-abcdefabcdef"}
	builds := []string{"", "+incompatible", "+build.1", "+", "+a..b", "+x.incompatible"}
	for _, core := range []string{"v0.0.0", "v0.1.0", "v1.0.0", "v1.2.3", "v1.10.0", "v1.9.10", "v2.0.0", "v10.0.1", "v99999999999999999999.0.0",
		"v1", "v1.2", "v01.2.3", "v1.02.3", "v1.2.03", "1.2.3", "v", "v1.", "v1.2.3.4"} {
		for _, p := range pres {
			for _, b := range builds {
				out = append(out, core+p+b)
			}
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	const alphabet = "v0123456789.-+ab"
	for range 20000 {
		b := make([]byte, 1+r.IntN(14))
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		if r.IntN(2) == 0 {
			b[0] = 'v'
		}
		out = append(out, string(b))
	}
	return out
}

// TestGoVersionMatchesXMod checks the parsed Go scheme against golang.org/x/mod: validity,
// Compare, Kind, compatibility and the Masterminds conversion.
func TestGoVersionMatchesXMod(t *testing.T) {
	corpus := goCorpus()
	var valid []goVersion
	for _, v := range corpus {
		p, ok := parseGoSemver(v)
		if ok != semver.IsValid(v) {
			t.Fatalf("parse(%q) ok = %v, semver.IsValid = %v", v, ok, !ok)
		}
		_, okGo := parseGo(v)
		if want := ok && !module.IsPseudoVersion(v); okGo != want {
			t.Fatalf("parseGo(%q) ok = %v, want %v", v, okGo, want)
		}
		if !ok {
			continue
		}
		if p.prerelease() != semver.Prerelease(v) || p.incompatible() != strings.HasSuffix(v, "+incompatible") || "v"+p.majorNum() != semver.Major(v) {
			t.Fatalf("parse(%q) = %+v", v, p)
		}
		var want *msemver.Version
		if w, err := msemver.NewVersion(strings.TrimPrefix(v, "v")); err == nil {
			want = w
		}
		if got := p.semver(); (got == nil) != (want == nil) || got != nil && (got.String() != want.String() || got.Compare(want) != 0) {
			t.Fatalf("semver(%q) = %v, want %v", v, got, want)
		}
		valid = append(valid, p)
	}
	if len(valid) < 500 {
		t.Fatalf("only %d valid versions in corpus", len(valid))
	}
	for _, a := range valid {
		for _, b := range valid {
			if got, want := compareGo(a, b), semver.Compare(a.raw, b.raw); got != want {
				t.Fatalf("compareGo(%q, %q) = %d, semver.Compare = %d", a.raw, b.raw, got, want)
			}
			if got, want := goKind(a, b), refGoKind(a.raw, b.raw); got != want {
				t.Fatalf("goKind(%q, %q) = %s, want %s", a.raw, b.raw, got, want)
			}
			if got, want := goCompat(a, b), refGoCompat(a.raw, b.raw); got != want {
				t.Fatalf("goCompat(%q, %q) = %q, want %q", a.raw, b.raw, got, want)
			}
		}
	}
}

// refGoKind and refGoCompat are the string-based rules the parsed scheme replaces.
func refGoKind(cur, next string) dep.UpdateType {
	if semver.Major(cur) != semver.Major(next) {
		return dep.Major
	}
	if semver.MajorMinor(cur) != semver.MajorMinor(next) {
		return dep.Minor
	}
	return dep.Patch
}

func refGoCompat(cur, next string) string {
	switch {
	case semver.Prerelease(next) != "" && semver.Prerelease(cur) == "":
		return "prerelease"
	case strings.HasSuffix(next, "+incompatible") && !strings.HasSuffix(cur, "+incompatible"):
		return "suffix"
	}
	if m, c := semver.Major(next), semver.Major(cur); m != c && (c != "v0" || m != "v1") {
		return "major"
	}
	return ""
}
