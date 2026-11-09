package versioning

import (
	"cmp"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// rpmVersion is an RPM package version `[<epoch>:]<version>[-<release>]` (Renovate's `rpm`
// versioning, RPM's EVR): the version and the release are compared with rpmvercmp, after the
// epoch. Version and release slice the original string.
type rpmVersion struct {
	epoch            uint64
	version, release string // release "" = none
	pre              bool   // a "~" in version or release marks a prerelease (`1.0~rc1`)
}

// parseRPM parses s. The epoch is decimal; the release is what follows the last "-"; version
// and release are non-empty runs of `[0-9A-Za-z._+~^]`, and the version starts with a digit
// so words such as `latest` are not versions.
func parseRPM(s string) (rpmVersion, bool) {
	var v rpmVersion
	rest := s
	if e, r, ok := strings.Cut(s, ":"); ok {
		n, err := strconv.ParseUint(e, 10, 64)
		if err != nil || !allDigits(e) {
			return rpmVersion{}, false
		}
		v.epoch, rest = n, r
	}
	v.version = rest
	if i := strings.LastIndexByte(rest, '-'); i >= 0 {
		v.version, v.release = rest[:i], rest[i+1:]
		if v.release == "" || !rpmChars(v.release) {
			return rpmVersion{}, false
		}
	}
	if v.version == "" || v.version[0] < '0' || v.version[0] > '9' || !rpmChars(v.version) {
		return rpmVersion{}, false
	}
	v.pre = strings.IndexByte(v.version, '~') >= 0 || strings.IndexByte(v.release, '~') >= 0
	return v, true
}

// rpmChars reports whether s only has the characters RPM allows in a version or release.
func rpmChars(s string) bool {
	for i := range len(s) {
		if c := s[i]; !rpmAlnum(c) && c != '.' && c != '_' && c != '+' && c != '~' && c != '^' {
			return false
		}
	}
	return true
}

func rpmAlnum(c byte) bool { return rpmDigit(c) || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func rpmDigit(c byte) bool { return '0' <= c && c <= '9' }

// rpmvercmp compares two version or release strings as RPM's rpmvercmp does: runs of digits
// and runs of letters are compared pairwise, separators skipped; digits compare numerically
// and are newer than letters; "~" sorts before anything, even the end of the string
// (`1.0~rc1` < `1.0`); "^" sorts after the end of the string but before anything else
// (`1.0` < `1.0^git1` < `1.0.1`); with equal runs, the string with more runs is newer.
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for i < len(a) && !rpmAlnum(a[i]) && a[i] != '~' && a[i] != '^' {
			i++
		}
		for j < len(b) && !rpmAlnum(b[j]) && b[j] != '~' && b[j] != '^' {
			j++
		}
		ta, tb := i < len(a) && a[i] == '~', j < len(b) && b[j] == '~'
		if ta || tb {
			if !ta {
				return 1
			}
			if !tb {
				return -1
			}
			i, j = i+1, j+1
			continue
		}
		ca, cb := i < len(a) && a[i] == '^', j < len(b) && b[j] == '^'
		if ca || cb {
			switch {
			case i >= len(a):
				return -1
			case j >= len(b):
				return 1
			case !ca:
				return 1
			case !cb:
				return -1
			}
			i, j = i+1, j+1
			continue
		}
		if i >= len(a) || j >= len(b) {
			break
		}
		si, sj := i, j
		num := rpmDigit(a[i])
		run := func(s string, k int) int {
			for k < len(s) && (num && rpmDigit(s[k]) || !num && rpmAlnum(s[k]) && !rpmDigit(s[k])) {
				k++
			}
			return k
		}
		i, j = run(a, i), run(b, j)
		x, y := a[si:i], b[sj:j]
		if y == "" { // runs of different types: digits are newer
			if num {
				return 1
			}
			return -1
		}
		if num {
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				return cmp.Compare(len(x), len(y))
			}
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i >= len(a):
		return -1
	}
	return 1
}

// compareRPM orders by epoch, then version, then release (rpmvercmp); a version without a
// release sorts below the same version with one.
func compareRPM(a, b rpmVersion) int {
	if c := cmp.Compare(a.epoch, b.epoch); c != 0 {
		return c
	}
	if c := rpmvercmp(a.version, b.version); c != 0 {
		return c
	}
	return rpmvercmp(a.release, b.release)
}

// rpmCompat admits a prerelease only from a prerelease; any other valid version is
// compatible, as in Renovate.
func rpmCompat(cur, next rpmVersion) string {
	if next.pre && !cur.pre {
		return "prerelease"
	}
	return ""
}

// rpmScheme is RPM's EVR. Best and Rank take a cur without an epoch (files usually write
// `<name>-<ver>-<rel>`, as dnf takes it, while the rpm datasource lists `<epoch>:` versions)
// to have the epoch of the candidate with its version and release, the highest when
// several do: `1.5.10-34.fc41` listed as `1.5.10-34.fc41` is epoch 0, so an epoch bump that
// lowered the version (`1:0.8.0-1.fc43`) is newer, as dnf orders it. Without such a
// candidate the epochs of cur and the candidates are ignored (Renovate's order). Candidates
// are written in cur's form: without the epoch when cur has none, with it (`0:` for none)
// when cur has one.
type rpmScheme struct{ impl[rpmVersion] }

func (s rpmScheme) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	in, c := s.against(cur, cands)
	best, ok := in.Best(c, cands, accept, reject)
	if !ok {
		return "", false
	}
	return rpmStyle(cur, best), true
}

func (s rpmScheme) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) Ranking {
	in, c := s.against(cur, cands)
	r := in.rank(c, cands, accept, reject)
	r.style = func(v string) string { return rpmStyle(cur, v) }
	return r
}

func (s rpmScheme) Compatible(cur, next string) bool {
	in, c := s.against(cur, []string{next})
	return in.Compatible(c, next)
}

// against returns the scheme and the current version cands are ranked against: cur and
// the full scheme when cur has an epoch; else the candidate with cur's version and release
// and the highest epoch; else cur and the scheme ignoring epochs.
func (s rpmScheme) against(cur string, cands []string) (impl[rpmVersion], string) {
	if strings.IndexByte(cur, ':') >= 0 {
		return s.impl, cur
	}
	match, epoch, found := "", uint64(0), false
	for _, v := range cands {
		if v != cur && (len(v) <= len(cur) || v[len(v)-len(cur)-1] != ':' || !strings.HasSuffix(v, cur)) {
			continue
		}
		if n, ok := parseRPM(v); ok && (!found || n.epoch > epoch) {
			match, epoch, found = v, n.epoch, true
		}
	}
	if found {
		return s.impl, match
	}
	in := s.impl
	in.parse = parseRPMIgnoringEpoch
	return in, cur
}

// parseRPMIgnoringEpoch is parseRPM with the epoch dropped.
func parseRPMIgnoringEpoch(s string) (rpmVersion, bool) {
	v, ok := parseRPM(s)
	v.epoch = 0
	return v, ok
}

// rpmStyle writes the candidate v in cur's form: without its epoch when cur has none, with
// `0:` when cur has an epoch and v none.
func rpmStyle(cur, v string) string {
	_, rest, epoch := strings.Cut(v, ":")
	switch curEpoch := strings.IndexByte(cur, ':') >= 0; {
	case epoch && !curEpoch:
		return rest
	case !epoch && curEpoch:
		return "0:" + v
	}
	return v
}

// num is the leading number of the i-th part of the version, parts separated by ".", "_" or
// "+" and ending at a "~" or "^" (`1.2.3a~rc1` → 1, 2, 3): 0 when absent or not starting with
// a digit; huge values saturate.
func (v rpmVersion) num(i int) uint64 {
	s := v.version
	if k := strings.IndexAny(s, "~^"); k >= 0 {
		s = s[:k]
	}
	for part := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == '.' || r == '_' || r == '+' }) {
		if i > 0 {
			i--
			continue
		}
		e := 0
		for e < len(part) && rpmDigit(part[e]) {
			e++
		}
		if e == 0 {
			return 0
		}
		n, err := strconv.ParseUint(part[:e], 10, 64)
		if err != nil {
			return ^uint64(0)
		}
		return n
	}
	return 0
}

// rpmKind classifies by epoch and the first two numbers of the version: an epoch or first
// number change is major, a second number change minor, anything else (later numbers, the
// release) patch.
func rpmKind(cur, next rpmVersion) dep.UpdateType {
	switch {
	case cur.epoch != next.epoch || cur.num(0) != next.num(0):
		return dep.Major
	case cur.num(1) != next.num(1):
		return dep.Minor
	}
	return dep.Patch
}

func (v rpmVersion) semver() *msemver.Version {
	pre := ""
	if v.pre {
		pre = "pre"
	}
	return msemver.New(v.num(0), v.num(1), v.num(2), pre, "")
}

func (v rpmVersion) major() string {
	m := strconv.FormatUint(v.num(0), 10)
	if v.epoch != 0 {
		return strconv.FormatUint(v.epoch, 10) + ":" + m
	}
	return m
}
