package versioning

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// apkVersion is an Alpine package version (Renovate's `apk` versioning, apk-tools'
// version.c): `N[.N]...[a-z][_suffix[N]]...[~hash][-rN]`, e.g. `3.2.1-r0`, `2.39.0_rc1-r0`,
// `6.5_p20250503-r0`, `1.1.1w-r1`. Suffixes are alpha, beta, pre and rc (below the plain
// version) and cvs, svn, git, hg and p (above it).
type apkVersion struct {
	nums     []uint64
	letter   byte // 0 = none
	suffixes []apkSuffix
	rev      uint64
	hasRev   bool
}

// apkSuffix is one `_<name>[N]` suffix: rank orders the names, negative for pre-release ones.
type apkSuffix struct {
	rank int
	num  uint64
}

// apkSuffixRanks orders the suffix names as apk-tools does; the plain version ranks 0.
var apkSuffixRanks = map[string]int{"alpha": -4, "beta": -3, "pre": -2, "rc": -1, "cvs": 1, "svn": 2, "git": 3, "hg": 4, "p": 5}

// parseApk parses s; numbers that do not fit uint64 make it invalid.
func parseApk(s string) (apkVersion, bool) {
	var v apkVersion
	rest := s
	if i := strings.LastIndex(rest, "-r"); i >= 0 {
		n, ok := apkNum(rest[i+2:])
		if !ok {
			return apkVersion{}, false
		}
		v.rev, v.hasRev, rest = n, true, rest[:i]
	}
	if i := strings.IndexByte(rest, '~'); i >= 0 {
		if !apkHex(rest[i+1:]) {
			return apkVersion{}, false
		}
		rest = rest[:i]
	}
	head, sufs, _ := strings.Cut(rest, "_")
	if head != "" && 'a' <= head[len(head)-1] && head[len(head)-1] <= 'z' {
		v.letter, head = head[len(head)-1], head[:len(head)-1]
	}
	for p := range strings.SplitSeq(head, ".") {
		n, ok := apkNum(p)
		if !ok {
			return apkVersion{}, false
		}
		v.nums = append(v.nums, n)
	}
	if sufs != "" || strings.HasSuffix(rest, "_") {
		for suf := range strings.SplitSeq(sufs, "_") {
			end := len(suf)
			for end > 0 && '0' <= suf[end-1] && suf[end-1] <= '9' {
				end--
			}
			rank, ok := apkSuffixRanks[suf[:end]]
			if !ok {
				return apkVersion{}, false
			}
			sx := apkSuffix{rank: rank}
			if end < len(suf) {
				if sx.num, ok = apkNum(suf[end:]); !ok {
					return apkVersion{}, false
				}
			}
			v.suffixes = append(v.suffixes, sx)
		}
	}
	return v, true
}

// apkNum parses a non-empty decimal number.
func apkNum(s string) (uint64, bool) {
	if s == "" || s[0] == '+' {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// apkHex reports whether s is a non-empty lower-case hex commit hash.
func apkHex(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// compareApk orders by the numbers (a shorter list first when it is a prefix), the letter,
// the suffixes (a missing suffix ranks between pre-release and post-release ones), then the
// revision; the commit hash does not count.
func compareApk(a, b apkVersion) int {
	if c := slices.Compare(a.nums, b.nums); c != 0 {
		return c
	}
	if c := cmp.Compare(a.letter, b.letter); c != 0 {
		return c
	}
	for i := range max(len(a.suffixes), len(b.suffixes)) {
		var x, y apkSuffix
		if i < len(a.suffixes) {
			x = a.suffixes[i]
		}
		if i < len(b.suffixes) {
			y = b.suffixes[i]
		}
		if c := cmp.Or(cmp.Compare(x.rank, y.rank), cmp.Compare(x.num, y.num)); c != 0 {
			return c
		}
	}
	return cmp.Compare(a.rev, b.rev)
}

// prerelease reports whether v carries an alpha, beta, pre or rc suffix.
func (v apkVersion) prerelease() bool {
	return slices.ContainsFunc(v.suffixes, func(s apkSuffix) bool { return s.rank < 0 })
}

// apkCompat admits a pre-release only from a pre-release, and keeps the revision style: a
// version written with `-rN` moves to versions with one, one without to versions without.
func apkCompat(cur, next apkVersion) string {
	switch {
	case next.prerelease() && !cur.prerelease():
		return "prerelease"
	case cur.hasRev != next.hasRev:
		return "suffix"
	}
	return ""
}

// num is the i-th number of v, 0 when absent.
func (v apkVersion) num(i int) uint64 {
	if i < len(v.nums) {
		return v.nums[i]
	}
	return 0
}

// apkKind classifies by the first two numbers; everything after them (further numbers,
// letter, suffixes, revision) is a patch update.
func apkKind(cur, next apkVersion) dep.UpdateType {
	switch {
	case cur.num(0) != next.num(0):
		return dep.Major
	case cur.num(1) != next.num(1):
		return dep.Minor
	}
	return dep.Patch
}

func (v apkVersion) semver() *msemver.Version {
	pre := ""
	if v.prerelease() {
		pre = "pre"
	}
	return msemver.New(v.num(0), v.num(1), v.num(2), pre, "")
}

func (v apkVersion) major() string { return strconv.FormatUint(v.num(0), 10) }
