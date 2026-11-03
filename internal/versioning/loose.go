package versioning

import (
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// maxLooseParts is the most numeric parts a loose version may have (Renovate: 6).
const maxLooseParts = 6

// looseVersion is Renovate's loose versioning: 1–6 numeric dot-separated parts with an
// optional "v" prefix and an optional suffix after "-" or "_", which counts as a prerelease
// (1877.25, v1.2.3.4, 2.0-rc1, 5_beta). The parts live in an array, so parsing allocates
// nothing.
type looseVersion struct {
	parts  [maxLooseParts]int
	n      int    // number of parts
	v      bool   // "v" prefix
	suffix string // prerelease after the separator; "" = release
}

func parseLoose(s string) (looseVersion, bool) {
	var l looseVersion
	if s != "" && s[0] == 'v' {
		l.v, s = true, s[1:]
	}
	for {
		i := 0
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		if i == 0 || l.n == maxLooseParts {
			return looseVersion{}, false
		}
		v, err := strconv.Atoi(s[:i])
		if err != nil {
			return looseVersion{}, false // overflow
		}
		l.parts[l.n], l.n, s = v, l.n+1, s[i:]
		if s == "" {
			return l, true
		}
		switch s[0] {
		case '.':
			s = s[1:]
		case '-', '_':
			if !validSuffix(s[1:]) {
				return looseVersion{}, false
			}
			l.suffix = s[1:]
			return l, true
		default:
			return looseVersion{}, false
		}
	}
}

// validSuffix reports whether s is a non-empty run of [0-9A-Za-z._+-].
func validSuffix(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || strings.ContainsRune("._+-", r))
	})
}

// looseCompat requires the current "v" prefix style and rejects prereleases unless cur is one.
func looseCompat(cur, next looseVersion) string {
	switch {
	case next.v != cur.v:
		return "prefix"
	case next.suffix != "" && cur.suffix == "":
		return "prerelease"
	}
	return ""
}

// compareLoose orders the parts numerically (a missing part counts as 0), then a release
// above its prereleases, prereleases by their dot-separated identifiers, and finally fewer
// parts below more (1.2 < 1.2.0) so the order is total.
func compareLoose(a, b looseVersion) int {
	for i := range maxLooseParts {
		if a.parts[i] != b.parts[i] {
			if a.parts[i] < b.parts[i] {
				return -1
			}
			return 1
		}
	}
	if c := comparePrerelease(a.suffix, b.suffix); c != 0 {
		return c
	}
	return a.n - b.n
}

// looseKind classifies by position: part 0 major, part 1 minor, any later part or the
// suffix patch.
func looseKind(cur, next looseVersion) dep.UpdateType {
	switch {
	case cur.parts[0] != next.parts[0]:
		return dep.Major
	case cur.parts[1] != next.parts[1]:
		return dep.Minor
	}
	return dep.Patch
}

// semver builds MAJOR.MINOR.PATCH[-suffix] from the first three parts.
func (l looseVersion) semver() *msemver.Version {
	return msemver.New(uint64(l.parts[0]), uint64(l.parts[1]), uint64(l.parts[2]), l.suffix, "")
}

func (l looseVersion) major() string { return strconv.Itoa(l.parts[0]) }

// comparePrerelease orders prerelease identifiers without a leading separator by semver
// precedence: "" (a release) is highest, dot-separated identifiers compare numerically when
// both are numbers, numbers sort below words, and a shorter list of equal identifiers is lower.
func comparePrerelease(x, y string) int {
	switch {
	case x == y:
		return 0
	case x == "":
		return 1
	case y == "":
		return -1
	}
	for {
		dx, rx, okx := strings.Cut(x, ".")
		dy, ry, oky := strings.Cut(y, ".")
		if dx != dy {
			nx, ny := isNum(dx), isNum(dy)
			switch {
			case nx && ny:
				if c := compareNum(strings.TrimLeft(dx, "0"), strings.TrimLeft(dy, "0")); c != 0 {
					return c
				}
				return strings.Compare(dx, dy)
			case nx:
				return -1
			case ny:
				return 1
			}
			return strings.Compare(dx, dy)
		}
		switch {
		case !okx && !oky:
			return 0
		case !okx:
			return -1
		case !oky:
			return 1
		}
		x, y = rx, ry
	}
}
