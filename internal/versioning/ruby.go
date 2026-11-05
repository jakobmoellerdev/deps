package versioning

import (
	"fmt"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// rubyMaxParts bounds the numeric release segments of a Ruby version, so parsing allocates
// nothing.
const rubyMaxParts = 10

// rubyVersion is a RubyGems version (Gem::Version): dot-separated segments of digits and
// letters, optionally followed by `-<prerelease>`. The numeric segments before the first
// letter are the release; from the first letter (or `-`) on, the version is a prerelease
// (`1.0.0.rc1`, `2.0.0.pre.beta`, `1.0.0-beta` = `1.0.0.pre.beta`).
type rubyVersion struct {
	parts [rubyMaxParts]int
	n     int    // numeric release segments
	pre   string // the version from the first letter or "-" on; "" = release
}

// parseRuby parses s as Gem::Version's ANCHORED_VERSION_PATTERN does (without surrounding
// blanks): `[0-9]+(\.[0-9a-zA-Z]+)*(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?`. Numeric segments
// must fit an int.
func parseRuby(s string) (rubyVersion, bool) {
	var v rubyVersion
	main, tail, dash := strings.Cut(s, "-")
	i := 0
	for seg := 0; ; seg++ {
		start := i
		for i < len(main) && main[i] != '.' {
			j := i
			switch c := main[i]; {
			case isDigit(c):
				for i < len(main) && isDigit(main[i]) {
					i++
				}
				n, err := strconv.Atoi(main[j:i])
				if err != nil {
					return rubyVersion{}, false // overflow
				}
				if v.pre == "" {
					if v.n == rubyMaxParts {
						return rubyVersion{}, false
					}
					v.parts[v.n], v.n = n, v.n+1
				}
			case isLetter(c) && seg > 0: // the first segment is digits only
				if v.pre == "" {
					v.pre = s[i:]
				}
				for i < len(main) && isLetter(main[i]) {
					i++
				}
			default:
				return rubyVersion{}, false
			}
		}
		if i == start {
			return rubyVersion{}, false // empty segment
		}
		if i == len(main) {
			break
		}
		i++ // the dot
	}
	if !dash {
		return v, true
	}
	if !validRubyTail(tail) {
		return rubyVersion{}, false
	}
	if v.pre == "" {
		v.pre = s[len(main):]
	}
	return v, true
}

// validRubyTail reports whether the prerelease after a version's first "-" is
// `[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*` with digit runs that fit an int.
func validRubyTail(t string) bool {
	seg, digits := 0, 0 // length of the current segment and digit run
	for i := range len(t) {
		c := t[i]
		switch {
		case c == '.':
			if seg == 0 {
				return false
			}
			seg, digits = 0, 0
			continue
		case isDigit(c):
			digits++
			if digits > 18 {
				return false
			}
		case isLetter(c) || c == '-':
			digits = 0
		default:
			return false
		}
		seg++
	}
	return seg > 0
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// rubySegs iterates the canonical segments of a Ruby version as Gem::Version#<=> compares
// them: the release without trailing zeros, then the prerelease segments, where each "-"
// reads as a "pre" segment (Gem::Version rewrites "-" to ".pre.").
type rubySegs struct {
	v   *rubyVersion
	rel int // release segments to yield (trailing zeros dropped)
	i   int // next release segment
	j   int // next byte of v.pre
}

func newRubySegs(v *rubyVersion) rubySegs {
	rel := v.n
	for rel > 0 && v.parts[rel-1] == 0 {
		rel--
	}
	return rubySegs{v: v, rel: rel}
}

// next returns the next segment: a number (str == "") or a string; ok is false at the end.
func (it *rubySegs) next() (num int, str string, ok bool) {
	if it.i < it.rel {
		it.i++
		return it.v.parts[it.i-1], "", true
	}
	p := it.v.pre
	for it.j < len(p) && p[it.j] == '.' {
		it.j++
	}
	if it.j >= len(p) {
		return 0, "", false
	}
	start := it.j
	switch c := p[it.j]; {
	case c == '-':
		it.j++
		return 0, "pre", true
	case isDigit(c):
		for it.j < len(p) && isDigit(p[it.j]) {
			it.j++
		}
		n, _ := strconv.Atoi(p[start:it.j]) // parseRuby checked the range
		return n, "", true
	default:
		for it.j < len(p) && isLetter(p[it.j]) {
			it.j++
		}
		return 0, p[start:it.j], true
	}
}

// compareRuby is Gem::Version#<=>: canonical segments compare pairwise, a missing one counting
// as 0; numbers compare numerically, strings bytewise, and a string (a prerelease) sorts below
// a number. Versions differing only in trailing zeros are equal (`1.2` = `1.2.0`).
func compareRuby(a, b rubyVersion) int {
	x, y := newRubySegs(&a), newRubySegs(&b)
	for {
		xn, xs, xok := x.next()
		yn, ys, yok := y.next()
		switch {
		case !xok && !yok:
			return 0
		case xok && yok && xs == "" && ys == "" || !xok && ys == "" || !yok && xs == "":
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xs != "" && ys != "":
			if c := strings.Compare(xs, ys); c != 0 {
				return c
			}
		case xs != "": // string vs number (or a missing 0)
			return -1
		default:
			return 1
		}
	}
}

// rubyCompat rejects prereleases unless cur is one.
func rubyCompat(cur, next rubyVersion) string {
	if next.pre != "" && cur.pre == "" {
		return "prerelease"
	}
	return ""
}

func rubyKind(cur, next rubyVersion) dep.UpdateType {
	switch {
	case cur.parts[0] != next.parts[0]:
		return dep.Major
	case cur.parts[1] != next.parts[1]:
		return dep.Minor
	}
	return dep.Patch
}

// semver maps the first three release segments and the prerelease (without separators at its
// ends, "-" read as ".") to a Masterminds version.
func (v rubyVersion) semver() *msemver.Version {
	pre := strings.Trim(strings.ReplaceAll(v.pre, "-", "."), ".")
	return msemver.New(uint64(v.parts[0]), uint64(v.parts[1]), uint64(v.parts[2]), pre, "")
}

func (v rubyVersion) major() string { return strconv.Itoa(v.parts[0]) }

// release is the version without its prerelease (Gem::Version#release).
func (v rubyVersion) release() rubyVersion {
	v.pre = ""
	return v
}

// bump is Gem::Version#bump: the release without its last segment (unless it is the only one),
// with the new last segment incremented; `~> v` admits versions below it.
func (v rubyVersion) bump() rubyVersion {
	b := v.release()
	if b.n > 1 {
		b.n--
	}
	b.parts[b.n-1]++
	for i := b.n; i < len(b.parts); i++ {
		b.parts[i] = 0
	}
	return b
}

// rubyScheme is the versioning of RubyGems (Renovate's `ruby`), with Gem::Requirement ranges
// (MatchRange).
type rubyScheme struct{ impl[rubyVersion] }

// RubyClause is one `<op> <version>` requirement of a gem (`~> 7.1`, `>= 1.2`, `2.0.1`); Off
// is the byte offset of Version in the requirement.
type RubyClause struct {
	Op, Version string
	Off         int
}

// ParseRubyRequirement parses one Gem::Requirement string: an optional operator (`=`, `!=`,
// `>`, `<`, `>=`, `<=`, `~>`; none means `=`), blanks, and a Ruby version.
func ParseRubyRequirement(r string) (RubyClause, error) {
	i := skipBlanks(r, 0)
	op := ""
	for _, o := range []string{"~>", ">=", "<=", "!=", "=", ">", "<"} {
		if strings.HasPrefix(r[i:], o) {
			op = o
			break
		}
	}
	i = skipBlanks(r, i+len(op))
	v := strings.TrimRight(r[i:], " \t")
	if _, ok := parseRuby(v); !ok {
		return RubyClause{}, fmt.Errorf("invalid requirement %q", r)
	}
	return RubyClause{Op: op, Version: v, Off: i}, nil
}

// MatchRange implements RangeMatcher with Gem::Requirement semantics for rng, the
// comma-separated requirements of a gem (`~> 7.1, >= 7.1.2`): all must hold; `~> 1.2` is
// `>= 1.2, < 2` and `~> 1.2.3` is `>= 1.2.3, < 1.3`. As Bundler resolves, a prerelease only
// satisfies requirements of which one names a prerelease.
func (rubyScheme) MatchRange(rng string) (func(v string) bool, error) {
	var clauses []RubyClause
	var parsed []rubyVersion
	pre := false
	for part := range strings.SplitSeq(rng, ",") {
		c, err := ParseRubyRequirement(part)
		if err != nil {
			return nil, err
		}
		p, _ := parseRuby(c.Version)
		pre = pre || p.pre != ""
		clauses, parsed = append(clauses, c), append(parsed, p)
	}
	return func(s string) bool {
		v, ok := parseRuby(s)
		if !ok || v.pre != "" && !pre {
			return false
		}
		for i, c := range clauses {
			if !rubyAdmits(c.Op, parsed[i], v) {
				return false
			}
		}
		return true
	}, nil
}

// rubyAdmits reports whether v satisfies `op r` (Gem::Requirement::OPS).
func rubyAdmits(op string, r, v rubyVersion) bool {
	c := compareRuby(v, r)
	switch op {
	case "", "=":
		return c == 0
	case "!=":
		return c != 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	}
	return c >= 0 && compareRuby(v.release(), r.bump()) < 0 // ~>
}

// FitRuby writes next with as many release segments as cur has, for the version of a `~>`
// (or lower-bound) requirement, whose precision is its meaning (`~> 7.1` → `~> 8.0` for 8.0.1,
// `~> 1.2.0` → `~> 2.0.0` for 2.0): extra segments are cut, missing ones padded with 0. A
// prerelease next, or either version invalid, is returned unchanged.
func FitRuby(cur, next string) string {
	c, okC := parseRuby(cur)
	n, okN := parseRuby(next)
	if !okC || !okN || n.pre != "" || c.n == 0 || c.pre != "" {
		return next
	}
	var b strings.Builder
	for i := range c.n {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n.parts[i])) // parts beyond n.n are 0
	}
	return b.String()
}
