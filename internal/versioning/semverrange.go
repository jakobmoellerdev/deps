package versioning

import (
	"strings"

	msemver "github.com/Masterminds/semver/v3"
)

// SemverRange is a Masterminds constraint (msemver.NewConstraint) compiled for repeated
// checks. Ranges and versions are checked many times per dependency (once per candidate, and
// npm checks every range it writes), and Masterminds parses a constraint with regexps and
// builds an error, with the version formatted into it, for every failed check. The common
// grammar is therefore parsed by hand and checked without allocating; anything else (hyphen
// ranges, `!=`, wildcards or partial versions with a prerelease, `V` prefixes, unusual
// whitespace, leading zeros) is left to Masterminds. Both accept and admit the same.
type SemverRange struct {
	groups [][]semverComparator // the `||` alternatives, each a list of ANDed comparators
	pre    []bool               // pre[i]: an alternative naming a prerelease admits prereleases
	ms     *msemver.Constraints // when the range is outside the hand-parsed grammar
}

// semverComparator is one Masterminds constraint: op (constraintOps' key) applied to con,
// where a wildcard or missing part makes it dirty: `*` (dirty only), `1`/`1.x` (minorDirty)
// or `1.2`/`1.2.x` (patchDirty), con having zeros for those parts.
type semverComparator struct {
	op                            string
	con                           semverVersion
	dirty, minorDirty, patchDirty bool
}

// NewSemverRange compiles rng as msemver.NewConstraint does, with its error.
func NewSemverRange(rng string) (*SemverRange, error) {
	if r, ok := parseSemverRange(rng); ok {
		return r, nil
	}
	c, err := msemver.NewConstraint(rng)
	if err != nil {
		return nil, err
	}
	return &SemverRange{ms: c}, nil
}

// Check reports whether v satisfies the range, as msemver.Constraints.Check.
func (r *SemverRange) Check(v *msemver.Version) bool {
	if r.ms != nil {
		return r.ms.Check(v)
	}
	return r.check(semverVersion{major: v.Major(), minor: v.Minor(), patch: v.Patch(), pre: v.Prerelease()})
}

// check is Check for a parsed version, without the Masterminds fallback.
func (r *SemverRange) check(v semverVersion) bool {
	for i, g := range r.groups {
		incPre := r.pre[i]
		if v.pre != "" && !incPre {
			continue // every constraint function rejects a prerelease first
		}
		ok := true
		for j := range g {
			if !g[j].check(v) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// MatchRange implements RangeMatcher with Masterminds constraint semantics, as the engine
// checks a semver range when the scheme has no grammar of its own.
func (semverScheme) MatchRange(rng string) (func(v string) bool, error) {
	r, err := NewSemverRange(rng)
	if err != nil {
		return nil, err
	}
	return func(s string) bool {
		v, ok := parseSemver(s)
		if !ok {
			return false
		}
		if r.ms != nil {
			return r.ms.Check(v.semver())
		}
		return r.check(v)
	}, nil
}

// parseSemverRange parses the grammar NewSemverRange handles itself: `||` alternatives of
// comparators separated by blanks or a comma, each an optional operator (`=`, `>`, `<`, `>=`,
// `=>`, `<=`, `=<`, `~`, `~>`, `^`) and a version: `v`? then one to three parts, each a
// decimal without leading zero or a wildcard (`x`, `X`, `*`); only a full numeric version may
// have a prerelease and metadata. ok is false for anything else, Masterminds deciding then.
func parseSemverRange(s string) (*SemverRange, bool) {
	if s == "" || len(s) > msemver.MaxConstraintLen {
		return nil, false
	}
	n := strings.Count(s, "||") + 1
	if n > msemver.MaxConstraintGroups {
		return nil, false
	}
	for i := 1; i < len(s); i++ {
		// Masterminds rewrites `A - B` (blanks around the hyphen) first: a hyphen range.
		if s[i] == '-' && isBlank(s[i-1]) {
			return nil, false
		}
	}
	r := &SemverRange{groups: make([][]semverComparator, 0, n), pre: make([]bool, 0, n)}
	for seg := range strings.SplitSeq(s, "||") {
		g, pre, ok := parseSemverGroup(seg)
		if !ok {
			return nil, false
		}
		r.groups = append(r.groups, g)
		r.pre = append(r.pre, pre)
	}
	return r, true
}

// parseSemverGroup parses one `||` alternative; pre reports a comparator with a prerelease.
func parseSemverGroup(s string) (g []semverComparator, pre, ok bool) {
	i := skipBlanks(s, 0)
	if i == len(s) {
		return nil, false, false
	}
	for i < len(s) {
		c, j, ok := parseSemverComparator(s, i)
		if !ok {
			return nil, false, false
		}
		g = append(g, c)
		pre = pre || c.con.pre != ""
		// A comparator ends at blanks, a comma or the end; a comma may be surrounded by blanks
		// but must be followed by another comparator.
		k := skipBlanks(s, j)
		comma := k < len(s) && s[k] == ','
		if comma {
			k = skipBlanks(s, k+1)
		}
		if k == j && k < len(s) || comma && k == len(s) {
			return nil, false, false
		}
		i = k
	}
	return g, pre, true
}

// parseSemverComparator parses the operator and version at i, returning the index after it.
func parseSemverComparator(s string, i int) (semverComparator, int, bool) {
	var c semverComparator
	switch {
	case strings.HasPrefix(s[i:], ">="), strings.HasPrefix(s[i:], "=>"),
		strings.HasPrefix(s[i:], "<="), strings.HasPrefix(s[i:], "=<"),
		strings.HasPrefix(s[i:], "~>"):
		c.op = s[i : i+2]
	case s[i] == '=', s[i] == '>', s[i] == '<', s[i] == '~', s[i] == '^':
		c.op = s[i : i+1]
	}
	i = skipBlanks(s, i+len(c.op))
	if i < len(s) && s[i] == 'v' {
		i++
	}
	var parts [3]string
	np := 0
	for {
		j := i
		if j < len(s) && (s[j] == 'x' || s[j] == 'X' || s[j] == '*') {
			j++
		} else {
			for j < len(s) && '0' <= s[j] && s[j] <= '9' {
				j++
			}
			if j == i || j-i > 1 && s[i] == '0' {
				return c, 0, false
			}
		}
		parts[np], np, i = s[i:j], np+1, j
		if np == 3 || i == len(s) || s[i] != '.' {
			break
		}
		i++
	}
	end := i
	if np == 3 && !isX(parts[0]) && !isX(parts[1]) && !isX(parts[2]) {
		// MAJOR.MINOR.PATCH with an optional prerelease and metadata, checked by
		// parseStrictSemver as Masterminds' NewVersion checks them.
		for end < len(s) && s[end] != ',' && s[end] != '|' && !isBlank(s[end]) {
			end++
		}
		start := i - len(parts[0]) - len(parts[1]) - len(parts[2]) - 2
		v, ok := parseStrictSemver(s[start:end])
		if !ok {
			return c, 0, false
		}
		c.con = semverVersion{major: v.major, minor: v.minor, patch: v.patch, pre: v.pre}
		return c, end, true
	}
	if end < len(s) && s[end] != ',' && !isBlank(s[end]) {
		return c, 0, false // a prerelease, metadata or anything else after a partial version
	}
	var ok bool
	switch {
	case isX(parts[0]):
		c.dirty = true
	case np < 2 || isX(parts[1]):
		c.dirty, c.minorDirty = true, true
		c.con.major, _, ok = semverNumber(parts[0])
	default:
		c.dirty, c.patchDirty = true, true
		c.con.major, _, ok = semverNumber(parts[0])
		if ok {
			c.con.minor, _, ok = semverNumber(parts[1])
		}
	}
	if c.dirty && !isX(parts[0]) && !ok {
		return c, 0, false // a part overflowing uint64
	}
	return c, end, true
}

func isX(p string) bool { return p == "x" || p == "X" || p == "*" }

// check is Masterminds' constraint function for c.op, after the prerelease check Check does
// per alternative.
func (c *semverComparator) check(v semverVersion) bool {
	switch c.op {
	case "", "=":
		if c.dirty {
			return c.tilde(v)
		}
		return compareSemver(v, c.con) == 0
	case ">":
		switch {
		case !c.dirty:
		case v.major > c.con.major:
			return true
		case v.major < c.con.major, c.minorDirty:
			return false
		case c.patchDirty:
			return v.minor > c.con.minor
		}
		return compareSemver(v, c.con) == 1
	case "<":
		return compareSemver(v, c.con) < 0
	case ">=", "=>":
		return compareSemver(v, c.con) >= 0
	case "<=", "=<":
		if !c.dirty {
			return compareSemver(v, c.con) <= 0
		}
		return v.major < c.con.major || v.major == c.con.major && (v.minor <= c.con.minor || c.minorDirty)
	case "~", "~>":
		return c.tilde(v)
	}
	return c.caret(v) // "^"
}

// tilde is Masterminds' constraintTilde.
func (c *semverComparator) tilde(v semverVersion) bool {
	switch {
	case compareSemver(v, c.con) < 0:
		return false
	case c.con.major == 0 && c.con.minor == 0 && c.con.patch == 0 && !c.minorDirty && !c.patchDirty:
		return true
	}
	return v.major == c.con.major && (v.minor == c.con.minor || c.minorDirty)
}

// caret is Masterminds' constraintCaret.
func (c *semverComparator) caret(v semverVersion) bool {
	switch {
	case compareSemver(v, c.con) < 0:
		return false
	case c.con.major > 0 || c.minorDirty:
		return v.major == c.con.major
	case v.major > 0:
		return false
	case c.con.minor > 0 || c.patchDirty:
		return v.minor == c.con.minor
	case v.minor > 0:
		return false
	}
	return v.patch == c.con.patch
}
