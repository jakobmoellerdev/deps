package versioning

import (
	"cmp"
	"math"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// maxPEP440Parts is the most release parts a PEP 440 version may have; longer releases are
// invalid so the parts fit an array.
const maxPEP440Parts = 10

// PEP 440 prerelease kinds in ascending order; pepNoPre sorts after every prerelease.
const (
	pepDevOnly = iota // dev release without pre or post: before every prerelease
	pepAlpha
	pepBeta
	pepRC
	pepNoPre
)

// pep440Version is a parsed PEP 440 version as python's packaging.version.Version reads it
// ([v][N!]N(.N)*[{a|b|rc}N][.postN][.devN][+local], case-insensitive, with the spelling
// variants the spec allows). Parsing allocates nothing: the release lives in an array and
// local is a substring of the input.
type pep440Version struct {
	epoch   int
	release [maxPEP440Parts]int
	n       int // number of release parts
	pre     int // pepAlpha, pepBeta, pepRC or pepNoPre; pepDevOnly only via preKey
	preN    int
	hasPost bool
	post    int
	hasDev  bool
	dev     int
	local   string // as written, without "+"; "" = none
}

var (
	pepPreLabels = []string{"alpha", "a", "beta", "b", "preview", "pre", "c", "rc"}
	pepPreKinds  = []int{pepAlpha, pepAlpha, pepBeta, pepBeta, pepRC, pepRC, pepRC, pepRC}
	pepPostLabel = []string{"post", "rev", "r"}
	pepPreNames  = [...]string{pepAlpha: "a", pepBeta: "b", pepRC: "rc"}
	pepDevLabel  = []string{"dev"}
)

func parsePEP440(s string) (pep440Version, bool) {
	v := pep440Version{pre: pepNoPre}
	i := 0
	if s != "" && (s[0] == 'v' || s[0] == 'V') {
		i++
	}
	n, i, ok := pepNum(s, i)
	if !ok {
		return pep440Version{}, false
	}
	if i < len(s) && s[i] == '!' {
		v.epoch = n
		if n, i, ok = pepNum(s, i+1); !ok {
			return pep440Version{}, false
		}
	}
	v.release[0], v.n = n, 1
	for i+1 < len(s) && s[i] == '.' && isDigit(s[i+1]) {
		if v.n == maxPEP440Parts {
			return pep440Version{}, false
		}
		if n, i, ok = pepNum(s, i+1); !ok {
			return pep440Version{}, false
		}
		v.release[v.n], v.n = n, v.n+1
	}
	if k, j := pepLabel(s, pepSep(s, i), pepPreLabels); k >= 0 {
		v.pre = pepPreKinds[k]
		if v.preN, i, ok = pepOptNum(s, j); !ok {
			return pep440Version{}, false
		}
	}
	if i+1 < len(s) && s[i] == '-' && isDigit(s[i+1]) {
		v.hasPost = true
		if v.post, i, ok = pepNum(s, i+1); !ok {
			return pep440Version{}, false
		}
	} else if k, j := pepLabel(s, pepSep(s, i), pepPostLabel); k >= 0 {
		v.hasPost = true
		if v.post, i, ok = pepOptNum(s, j); !ok {
			return pep440Version{}, false
		}
	}
	if k, j := pepLabel(s, pepSep(s, i), pepDevLabel); k >= 0 {
		v.hasDev = true
		if v.dev, i, ok = pepOptNum(s, j); !ok {
			return pep440Version{}, false
		}
	}
	if i < len(s) && s[i] == '+' {
		if !validLocal(s[i+1:]) {
			return pep440Version{}, false
		}
		v.local, i = s[i+1:], len(s)
	}
	return v, i == len(s)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isAlnum(c byte) bool { return isDigit(c) || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isPEPSep(c byte) bool { return c == '-' || c == '_' || c == '.' }

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// pepNum reads a non-empty decimal number at i; ok is false without digits or on overflow.
func pepNum(s string, i int) (n, j int, ok bool) {
	for j = i; j < len(s) && isDigit(s[j]); j++ {
		d := int(s[j] - '0')
		if n > (math.MaxInt-d)/10 {
			return 0, j, false
		}
		n = n*10 + d
	}
	return n, j, j > i
}

// pepOptNum reads the optional "[-_.]?N?" after a label; a missing number is 0.
func pepOptNum(s string, i int) (n, j int, ok bool) {
	i = pepSep(s, i)
	if i < len(s) && isDigit(s[i]) {
		return pepNum(s, i)
	}
	return 0, i, true
}

// pepSep skips one optional separator at i.
func pepSep(s string, i int) int {
	if i < len(s) && isPEPSep(s[i]) {
		return i + 1
	}
	return i
}

// pepLabel matches the first of labels at i case-insensitively and returns its index and the
// end, or -1.
func pepLabel(s string, i int, labels []string) (k, j int) {
	for k, l := range labels {
		if len(s)-i >= len(l) && foldEqual(s[i:i+len(l)], l) {
			return k, i + len(l)
		}
	}
	return -1, i
}

// foldEqual compares s to the lower-case ASCII word w ignoring ASCII case.
func foldEqual(s, w string) bool {
	for i := range len(w) {
		if lowerASCII(s[i]) != w[i] {
			return false
		}
	}
	return true
}

// validLocal reports whether s is [a-z0-9]+([-_.][a-z0-9]+)* ignoring case.
func validLocal(s string) bool {
	if s == "" {
		return false
	}
	prevSep := true
	for i := range len(s) {
		switch c := s[i]; {
		case isPEPSep(c):
			if prevSep {
				return false
			}
			prevSep = true
		case isAlnum(c):
			prevSep = false
		default:
			return false
		}
	}
	return !prevSep
}

// NormalizePEP440 returns the canonical PEP 440 form of v ("1.0-1" → "1.0.post1",
// "V1.0.0-Alpha.1" → "1.0.0a1", "1.0+ubuntu-1" → "1.0+ubuntu.1"), or false if v is not a
// PEP 440 version.
func NormalizePEP440(v string) (string, bool) {
	p, ok := parsePEP440(v)
	if !ok {
		return "", false
	}
	return p.format(p.n), true
}

// format writes v in normalized form with its release cut to parts parts.
func (v pep440Version) format(parts int) string {
	b := make([]byte, 0, 32)
	if v.epoch != 0 {
		b = append(strconv.AppendInt(b, int64(v.epoch), 10), '!')
	}
	for i := range parts {
		if i > 0 {
			b = append(b, '.')
		}
		b = strconv.AppendInt(b, int64(v.release[i]), 10)
	}
	if v.pre != pepNoPre {
		b = strconv.AppendInt(append(b, pepPreNames[v.pre]...), int64(v.preN), 10)
	}
	if v.hasPost {
		b = strconv.AppendInt(append(b, ".post"...), int64(v.post), 10)
	}
	if v.hasDev {
		b = strconv.AppendInt(append(b, ".dev"...), int64(v.dev), 10)
	}
	if v.local != "" {
		b = append(b, '+')
		for seg, rest, more := cutLocal(v.local); ; seg, rest, more = cutLocal(rest) {
			if isNum(seg) {
				if seg = strings.TrimLeft(seg, "0"); seg == "" {
					seg = "0"
				}
			}
			for i := range len(seg) {
				b = append(b, lowerASCII(seg[i]))
			}
			if !more {
				break
			}
			b = append(b, '.')
		}
	}
	return string(b)
}

// cutLocal splits a local version at its first separator.
func cutLocal(s string) (seg, rest string, more bool) {
	for i := range len(s) {
		if isPEPSep(s[i]) {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// preKey is the prerelease sort key: a dev release without pre or post sorts before every
// prerelease of its release, no prerelease after all of them.
func (v pep440Version) preKey() int {
	if v.pre == pepNoPre && !v.hasPost && v.hasDev {
		return pepDevOnly
	}
	return v.pre
}

// pep440Compat rejects prereleases (pre or dev) unless cur is one, local versions unless cur
// has one, and candidates with non-zero release parts beyond cur's precision.
func pep440Compat(cur, next pep440Version) string {
	switch {
	case (next.pre != pepNoPre || next.hasDev) && cur.pre == pepNoPre && !cur.hasDev:
		return "prerelease"
	case next.local != "" && cur.local == "":
		return "suffix"
	}
	for i := cur.n; i < next.n; i++ {
		if next.release[i] != 0 {
			return "precision"
		}
	}
	return ""
}

// comparePEP440 orders like packaging's Version: epoch, release (trailing zeros ignored),
// prerelease, post release (none lowest), dev release (none highest), local version (none
// lowest).
func comparePEP440(a, b pep440Version) int {
	if c := cmp.Compare(a.epoch, b.epoch); c != 0 {
		return c
	}
	for i := range maxPEP440Parts {
		if c := cmp.Compare(a.release[i], b.release[i]); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(a.preKey(), b.preKey()); c != 0 {
		return c
	}
	if c := cmp.Compare(a.preN, b.preN); c != 0 {
		return c
	}
	if c := compareBool(a.hasPost, b.hasPost); c != 0 {
		return c
	}
	if c := cmp.Compare(a.post, b.post); c != 0 {
		return c
	}
	if c := compareBool(!a.hasDev, !b.hasDev); c != 0 {
		return c
	}
	if c := cmp.Compare(a.dev, b.dev); c != 0 {
		return c
	}
	return compareLocal(a.local, b.local)
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// compareLocal compares local versions segment by segment: numbers numerically and above
// words, words case-insensitively, a shorter list of equal segments lower ("" lowest).
func compareLocal(x, y string) int {
	if x == "" || y == "" {
		return cmp.Compare(len(x), len(y))
	}
	for {
		sx, rx, mx := cutLocal(x)
		sy, ry, my := cutLocal(y)
		nx, ny := isNum(sx), isNum(sy)
		var c int
		switch {
		case nx && ny:
			c = compareNum(strings.TrimLeft(sx, "0"), strings.TrimLeft(sy, "0"))
		case nx:
			c = 1
		case ny:
			c = -1
		default:
			c = compareFold(sx, sy)
		}
		if c != 0 {
			return c
		}
		if !mx || !my {
			return compareBool(mx, my)
		}
		x, y = rx, ry
	}
}

// compareFold compares ASCII strings ignoring case.
func compareFold(x, y string) int {
	for i := range min(len(x), len(y)) {
		if c := cmp.Compare(lowerASCII(x[i]), lowerASCII(y[i])); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(x), len(y))
}

// pep440Kind: a new epoch or release[0] is major, release[1] minor, anything else patch.
func pep440Kind(cur, next pep440Version) dep.UpdateType {
	switch {
	case cur.epoch != next.epoch, cur.release[0] != next.release[0]:
		return dep.Major
	case cur.release[1] != next.release[1]:
		return dep.Minor
	}
	return dep.Patch
}

// semver builds MAJOR.MINOR.PATCH from the release with the prerelease and dev release as
// semver prerelease ("a.1", "rc.1.dev.2", "dev.3"); epoch, post release and local are dropped.
func (v pep440Version) semver() *msemver.Version {
	var b []byte
	if v.pre != pepNoPre {
		b = strconv.AppendInt(append(append(b, pepPreNames[v.pre]...), '.'), int64(v.preN), 10)
	}
	if v.hasDev {
		if b != nil {
			b = append(b, '.')
		}
		b = strconv.AppendInt(append(b, "dev."...), int64(v.dev), 10)
	}
	return msemver.New(uint64(v.release[0]), uint64(v.release[1]), uint64(v.release[2]), string(b), "")
}

func (v pep440Version) major() string { return strconv.Itoa(v.release[0]) }

// pep440Scheme is PEP 440 (Python packages). Best and Rank keep cur's release precision: a
// candidate with more release parts than cur (only zeros, others fail "precision") is
// written normalized with its release cut to cur's length, so `~=4.2` with candidate 5.0.0
// yields 5.0 and 5.0.0.post1 yields 5.0.post1; every other candidate is returned as published.
type pep440Scheme struct{ impl[pep440Version] }

func (s pep440Scheme) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	best, ok := s.impl.Best(cur, cands, accept, reject)
	if !ok {
		return "", false
	}
	c, _ := parsePEP440(cur)
	return pep440Style(c, best), true
}

func (s pep440Scheme) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) Ranking {
	r := s.rank(cur, cands, accept, reject)
	r.style = func(v string) string { return pep440Style(r.cur, v) }
	return r
}

// pep440Style cuts a valid candidate v with more release parts than cur to cur's precision.
func pep440Style(cur pep440Version, v string) string {
	p, _ := parsePEP440(v)
	if p.n <= cur.n {
		return v
	}
	return p.format(cur.n)
}
