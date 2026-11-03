package versioning

import (
	"math"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"
	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// goVersion is a Go module version parsed once with the syntax and ordering of
// golang.org/x/mod/semver. It stores end offsets into raw instead of substrings, so it stays
// small (Rank keeps one per candidate) and parsing does not allocate.
type goVersion struct {
	raw string
	// End offsets of MAJOR, .MINOR, .PATCH and -PRE in raw. An absent part ends where the
	// previous one does; absent minor/patch (shorthand v1, v1.2) read as "0". raw[pre:] is
	// the build metadata.
	major, minor, patch, pre uint32
}

func (v goVersion) majorNum() string { return v.raw[1:v.major] }

func (v goVersion) minorNum() string {
	if v.minor == v.major {
		return "0"
	}
	return v.raw[v.major+1 : v.minor]
}

func (v goVersion) patchNum() string {
	if v.patch == v.minor {
		return "0"
	}
	return v.raw[v.minor+1 : v.patch]
}

// prerelease is "-rc.1" or "".
func (v goVersion) prerelease() string { return v.raw[v.patch:v.pre] }

func (v goVersion) incompatible() bool { return v.raw[v.pre:] == "+incompatible" }

// parseGo parses a non-pseudo Go module version.
func parseGo(v string) (goVersion, bool) {
	p, ok := parseGoSemver(v)
	// A pseudo-version has at least two dashes in its prerelease, so the exact (regexp)
	// check runs only for those.
	if !ok || strings.Count(p.prerelease(), "-") >= 2 && module.IsPseudoVersion(v) {
		return goVersion{}, false
	}
	return p, true
}

// parseGoSemver mirrors golang.org/x/mod/semver's parser: "v" MAJOR[.MINOR[.PATCH[-PRE][+BUILD]]],
// shorthand forms without prerelease or build, no leading zeros in numbers.
func parseGoSemver(v string) (goVersion, bool) {
	p := goVersion{raw: v}
	if v == "" || v[0] != 'v' || len(v) > math.MaxUint32 {
		return p, false
	}
	i, ok := goInt(v, 1)
	if !ok {
		return p, false
	}
	end := uint32(i)
	p.major, p.minor, p.patch, p.pre = end, end, end, end
	if i == len(v) {
		return p, true
	}
	if v[i] != '.' {
		return p, false
	}
	if i, ok = goInt(v, i+1); !ok {
		return p, false
	}
	end = uint32(i)
	p.minor, p.patch, p.pre = end, end, end
	if i == len(v) {
		return p, true
	}
	if v[i] != '.' {
		return p, false
	}
	if i, ok = goInt(v, i+1); !ok {
		return p, false
	}
	p.patch, p.pre = uint32(i), uint32(i)
	if i < len(v) && v[i] == '-' {
		if i, ok = goIdents(v, i, true); !ok {
			return p, false
		}
		p.pre = uint32(i)
	}
	if i < len(v) && v[i] == '+' {
		if i, ok = goIdents(v, i, false); !ok {
			return p, false
		}
	}
	return p, i == len(v)
}

// goInt returns the end of the decimal number without leading zeros starting at v[i].
func goInt(v string, i int) (int, bool) {
	start := i
	for i < len(v) && '0' <= v[i] && v[i] <= '9' {
		i++
	}
	return i, i > start && (v[start] != '0' || i == start+1)
}

// goIdents returns the end of the "-" prerelease (pre) or "+" build starting at v[i]:
// non-empty dot-separated identifiers of [0-9A-Za-z-]; numeric prerelease identifiers have no
// leading zeros. A prerelease ends at "+".
func goIdents(v string, i int, pre bool) (int, bool) {
	start := i + 1
	for i = start; i < len(v) && (!pre || v[i] != '+'); i++ {
		c := v[i]
		if c == '.' {
			if start == i || pre && badNum(v[start:i]) {
				return i, false
			}
			start = i + 1
			continue
		}
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-') {
			return i, false
		}
	}
	return i, start != i && !(pre && badNum(v[start:i]))
}

func badNum(v string) bool { return len(v) > 1 && v[0] == '0' && isNum(v) }

func isNum(v string) bool {
	for i := range len(v) {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

// compareGo orders like semver.Compare: numbers numerically, a prerelease before its release,
// build metadata ignored.
func compareGo(a, b goVersion) int {
	if c := compareNum(a.majorNum(), b.majorNum()); c != 0 {
		return c
	}
	if c := compareNum(a.minorNum(), b.minorNum()); c != 0 {
		return c
	}
	if c := compareNum(a.patchNum(), b.patchNum()); c != 0 {
		return c
	}
	return comparePre(a.prerelease(), b.prerelease())
}

// compareNum compares decimal strings without leading zeros.
func compareNum(x, y string) int {
	if len(x) != len(y) {
		if len(x) < len(y) {
			return -1
		}
		return 1
	}
	return strings.Compare(x, y)
}

// comparePre orders prereleases by semver precedence; "" (a release) is highest.
func comparePre(x, y string) int {
	switch {
	case x == y:
		return 0
	case x == "":
		return 1
	case y == "":
		return -1
	}
	for x != "" && y != "" {
		var dx, dy string
		dx, x = nextIdent(x[1:]) // skip "-" or "."
		dy, y = nextIdent(y[1:])
		if dx == dy {
			continue
		}
		nx, ny := isNum(dx), isNum(dy)
		switch {
		case nx && ny:
			return compareNum(dx, dy)
		case nx:
			return -1
		case ny:
			return 1
		}
		return strings.Compare(dx, dy)
	}
	if x == "" {
		return -1
	}
	return 1
}

func nextIdent(x string) (id, rest string) {
	if i := strings.IndexByte(x, '.'); i >= 0 {
		return x[:i], x[i:]
	}
	return x, ""
}

// goCompat rejects prereleases unless cur is one, +incompatible unless cur has it, and other
// major versions except v0 → v1.
func goCompat(cur, next goVersion) string {
	switch cm, nm := cur.majorNum(), next.majorNum(); {
	case next.prerelease() != "" && cur.prerelease() == "":
		return "prerelease"
	case next.incompatible() && !cur.incompatible():
		return "suffix"
	case nm != cm && (cm != "0" || nm != "1"):
		return "major"
	}
	return ""
}

// goKind classifies cur → next.
func goKind(cur, next goVersion) dep.UpdateType {
	switch {
	case cur.majorNum() != next.majorNum():
		return dep.Major
	case cur.minorNum() != next.minorNum():
		return dep.Minor
	}
	return dep.Patch
}

// semver converts the version to a Masterminds version, or nil when a number overflows
// uint64. It equals msemver.NewVersion without the "v" but skips its regexp: a full
// MAJOR.MINOR.PATCH is already strict semver.
func (v goVersion) semver() *msemver.Version {
	if v.patch != v.minor {
		s, err := msemver.StrictNewVersion(v.raw[1:])
		if err != nil {
			return nil
		}
		return s
	}
	var n [2]uint64
	for i, s := range [2]string{v.majorNum(), v.minorNum()} {
		var err error
		if n[i], err = strconv.ParseUint(s, 10, 64); err != nil {
			return nil
		}
	}
	return msemver.New(n[0], n[1], 0, "", "")
}
