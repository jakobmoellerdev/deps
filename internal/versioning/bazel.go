package versioning

import (
	"cmp"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// bazelVersion is a Bazel module version (Renovate's `bazel-module` versioning, Bazel's
// bzlmod Version.java): `RELEASE[-PRERELEASE][+BUILD]`, RELEASE dot-separated identifiers of
// [0-9A-Za-z] (`1.2.3`, `1.2.3.bcr.1`, `20250814.1`), PRERELEASE and BUILD dot-separated
// identifiers that may also hold "-" (`2025-11-05.bcr.1` is release 2025 with prerelease
// `11-05.bcr.1`, as Bazel reads it). The identifiers slice the original string.
type bazelVersion struct {
	release, pre []string
	orig         string
}

// parseBazel parses s; the empty version (Bazel's "overridden" marker) is not one.
func parseBazel(s string) (bazelVersion, bool) {
	v := bazelVersion{orig: s}
	rest, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && !bazelIdents(build, true) {
		return bazelVersion{}, false
	}
	rel, pre, hasPre := strings.Cut(rest, "-")
	if !bazelIdents(rel, false) || hasPre && !bazelIdents(pre, true) {
		return bazelVersion{}, false
	}
	v.release = strings.Split(rel, ".")
	if hasPre {
		v.pre = strings.Split(pre, ".")
	}
	return v, true
}

// bazelIdents reports whether s is one or more non-empty dot-separated identifiers of
// [0-9A-Za-z] (and "-" when hyphen).
func bazelIdents(s string, hyphen bool) bool {
	if s == "" {
		return false
	}
	prev := byte('.')
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '.':
			if prev == '.' {
				return false
			}
		case '0' <= c && c <= '9', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', hyphen && c == '-':
		default:
			return false
		}
		prev = c
	}
	return prev != '.'
}

// bazelDigits reports whether the identifier s consists only of digits.
func bazelDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// compareBazelIdent orders two identifiers as Bazel does: digit-only identifiers before the
// others, digit-only ones numerically (then as strings, so `01` and `1` differ), the others
// as strings.
func compareBazelIdent(a, b string) int {
	da, db := bazelDigits(a), bazelDigits(b)
	switch {
	case da && !db:
		return -1
	case !da && db:
		return 1
	case da:
		ta, tb := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(ta) != len(tb) {
			return cmp.Compare(len(ta), len(tb))
		}
		if c := strings.Compare(ta, tb); c != 0 {
			return c
		}
	}
	return strings.Compare(a, b)
}

// compareBazelParts orders two identifier lists lexicographically, a proper prefix first.
func compareBazelParts(a, b []string) int {
	for i := range min(len(a), len(b)) {
		if c := compareBazelIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// compareBazel orders by release, then a prerelease below its release, then prereleases by
// their identifiers; build metadata does not count.
func compareBazel(a, b bazelVersion) int {
	if c := compareBazelParts(a.release, b.release); c != 0 {
		return c
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	return compareBazelParts(a.pre, b.pre)
}

// bazelCompat admits a prerelease only from a prerelease (Renovate's isStable filter); any
// other valid version is compatible, as in Renovate.
func bazelCompat(cur, next bazelVersion) string {
	if len(next.pre) > 0 && len(cur.pre) == 0 {
		return "prerelease"
	}
	return ""
}

// num is the numeric value of the i-th release identifier: 0 when absent or not digit-only
// (Renovate's getMajor/getMinor/getPatch); huge values saturate.
func (v bazelVersion) num(i int) uint64 {
	if i >= len(v.release) || !bazelDigits(v.release[i]) {
		return 0
	}
	n, err := strconv.ParseUint(v.release[i], 10, 64)
	if err != nil {
		return ^uint64(0)
	}
	return n
}

// bazelKind classifies by the first three release identifiers; everything after them (`.bcr.1`
// patches of the registry) and prerelease changes are patch updates.
func bazelKind(cur, next bazelVersion) dep.UpdateType {
	switch {
	case cur.num(0) != next.num(0):
		return dep.Major
	case cur.num(1) != next.num(1):
		return dep.Minor
	}
	return dep.Patch
}

func (v bazelVersion) semver() *msemver.Version {
	return msemver.New(v.num(0), v.num(1), v.num(2), strings.Join(v.pre, "."), "")
}

func (v bazelVersion) major() string { return strconv.FormatUint(v.num(0), 10) }
