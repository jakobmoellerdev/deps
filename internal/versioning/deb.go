package versioning

import (
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// debVersion is a Debian package version (Renovate's `deb` versioning, deb-version(7)):
// `[epoch:]upstream_version[-debian_revision]`. The epoch is decimal; upstream_version
// starts with a digit and holds [A-Za-z0-9.+~] plus ":" (only after an epoch) and "-" (only
// with a revision, which starts after the last "-"); the revision holds [A-Za-z0-9.+~].
// upstream and revision slice the original string; nums are the first three digit runs after
// the epoch (`1:2.36.1-8+deb11u1` → 2, 36, 1), the release parts update types are read from,
// as in Renovate. Huge numbers saturate.
type debVersion struct {
	epoch              uint64
	upstream, revision string
	nums               [3]uint64
}

// parseDeb parses s as dpkg's parseversion does, with Renovate's rejection of an empty
// revision (`1.0-`).
func parseDeb(s string) (debVersion, bool) {
	var v debVersion
	rest := s
	e, r, hasEpoch := strings.Cut(s, ":")
	if hasEpoch {
		if e == "" {
			return v, false
		}
		for i := range len(e) {
			if e[i] < '0' || e[i] > '9' {
				return v, false
			}
			v.epoch = satMulAdd(v.epoch, e[i])
		}
		rest = r
	}
	v.upstream = rest
	if i := strings.LastIndexByte(rest, '-'); i >= 0 {
		v.upstream, v.revision = rest[:i], rest[i+1:]
		if v.revision == "" {
			return v, false
		}
	}
	if v.upstream == "" || v.upstream[0] < '0' || v.upstream[0] > '9' {
		return v, false
	}
	for i := range len(v.upstream) {
		c := v.upstream[i]
		if !debChar(c) && (c != ':' || !hasEpoch) && (c != '-' || v.revision == "") {
			return v, false
		}
	}
	for i := range len(v.revision) {
		if !debChar(v.revision[i]) {
			return v, false
		}
	}
	n := 0
	for i := 0; i < len(rest) && n < len(v.nums); {
		if rest[i] < '0' || rest[i] > '9' {
			i++
			continue
		}
		for ; i < len(rest) && rest[i] >= '0' && rest[i] <= '9'; i++ {
			v.nums[n] = satMulAdd(v.nums[n], rest[i])
		}
		n++
	}
	return v, true
}

// satMulAdd is n*10 + the digit c, saturating at the uint64 maximum.
func satMulAdd(n uint64, c byte) uint64 {
	const maxU = ^uint64(0)
	d := uint64(c - '0')
	if n > (maxU-d)/10 {
		return maxU
	}
	return n*10 + d
}

// debChar reports whether c may appear anywhere in an upstream version or a revision.
func debChar(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '.' || c == '+' || c == '~'
}

// compareDeb orders by epoch, then upstream_version, then debian_revision, each part as
// dpkg's verrevcmp compares it.
func compareDeb(a, b debVersion) int {
	if c := cmpUint(a.epoch, b.epoch); c != 0 {
		return c
	}
	if c := debVerRevCmp(a.upstream, b.upstream); c != 0 {
		return c
	}
	return debVerRevCmp(a.revision, b.revision)
}

// debOrder is dpkg's order: a digit (and the end of the string) 0, a letter itself, "~"
// below everything (even the end), other characters after all letters.
func debOrder(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	switch c := s[i]; {
	case '0' <= c && c <= '9':
		return 0
	case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
		return int(c)
	case c == '~':
		return -1
	default:
		return int(c) + 256
	}
}

func debDigit(s string, i int) bool { return i < len(s) && '0' <= s[i] && s[i] <= '9' }

// debVerRevCmp is dpkg's verrevcmp: alternating non-digit runs, compared character by
// character with debOrder, and digit runs, compared numerically (any length).
func debVerRevCmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for i < len(a) && !debDigit(a, i) || j < len(b) && !debDigit(b, j) {
			ac, bc := debOrder(a, i), debOrder(b, j)
			if ac != bc {
				if ac < bc {
					return -1
				}
				return 1
			}
			i++
			j++
		}
		for debDigit(a, i) && a[i] == '0' {
			i++
		}
		for debDigit(b, j) && b[j] == '0' {
			j++
		}
		firstDiff := 0
		for debDigit(a, i) && debDigit(b, j) {
			if firstDiff == 0 {
				firstDiff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if debDigit(a, i) {
			return 1
		}
		if debDigit(b, j) {
			return -1
		}
		if firstDiff != 0 {
			if firstDiff < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// debCompat accepts every valid version: Debian versions have no prerelease marker a suite
// would mix in (a `~rc` upload is what the suite ships), as in Renovate.
func debCompat(_, _ debVersion) string { return "" }

// debKind is major on an epoch or first-number change, minor on a second-number change, else
// patch (a new revision or backport is a patch).
func debKind(cur, next debVersion) dep.UpdateType {
	switch {
	case cur.epoch != next.epoch || cur.nums[0] != next.nums[0]:
		return dep.Major
	case cur.nums[1] != next.nums[1]:
		return dep.Minor
	}
	return dep.Patch
}

func (v debVersion) semver() *msemver.Version {
	return msemver.New(v.nums[0], v.nums[1], v.nums[2], "", "")
}

func (v debVersion) major() string { return strconv.FormatUint(v.nums[0], 10) }
