package versioning

import (
	"slices"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"
)

// SemverRange is a Masterminds constraint (msemver.NewConstraint) compiled for repeated
// checks. Ranges and versions are checked many times per dependency (once per candidate, and
// npm checks every range it writes), and Masterminds parses a constraint with regexps and
// builds an error, with the version formatted into it, for every failed check. The common
// grammar is therefore parsed by hand and checked without allocating; anything else (hyphen
// ranges, `!=`, wildcards or partial versions with a prerelease, `V` prefixes, unusual
// whitespace, leading zeros) is left to Masterminds, alternative by alternative. Both accept
// and admit the same, except that prereleases follow node-semver (see semverGroup.pre).
type SemverRange struct {
	groups []semverGroup // the `||` alternatives
}

// semverGroup is one `||` alternative: comparators that must all hold, or the Masterminds
// constraint of an alternative outside the hand-parsed grammar.
type semverGroup struct {
	cmps []semverComparator
	ms   *msemver.Constraints
	// pre holds the [major, minor, patch] of every comparator naming a prerelease. npm
	// (node-semver's Range.test, semver/classes/range.js) admits a prerelease only when a
	// comparator of the same alternative names a prerelease of the same release, while
	// Masterminds admits every prerelease once one comparator names one, so that
	// `>=1.0.0-alpha.1 <2.0.0` would admit 1.5.0-rc.1.
	pre [][3]uint64
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
	if len(rng) > msemver.MaxConstraintLen || strings.Count(rng, "||") >= msemver.MaxConstraintGroups ||
		hyphenSpansAlternatives(rng) {
		// Masterminds judges the whole constraint, not an alternative, for its two limits and
		// for a hyphen range reaching across a `||`. Its alternatives then share their
		// prereleases (semverGroup.pre), which only such a malformed range has.
		c, err := msemver.NewConstraint(rng)
		if err != nil {
			return nil, err
		}
		return &SemverRange{groups: []semverGroup{semverFallback(c)}}, nil
	}
	segs := strings.Split(rng, "||")
	r := &SemverRange{groups: make([]semverGroup, 0, len(segs))}
	for _, seg := range segs {
		g, ok := parseSemverGroup(seg)
		if !ok {
			c, err := msemver.NewConstraint(seg)
			if err != nil {
				return nil, err
			}
			g = semverFallback(c)
		}
		r.groups = append(r.groups, g)
	}
	return r, nil
}

// hyphenSpansAlternatives reports whether a hyphen range of s may reach across a `||`:
// Masterminds rewrites `A - B` into comparators before it splits the alternatives, and the
// versions of its rewrite regexp admit a `|` and overlap each other, so `1.0 ||1.1 - 2.0` is
// one alternative of it and `0 - 0- 0|| 0 - 0` not what the alternatives are on their own.
// Such a range is compiled whole, as Masterminds compiles it.
func hyphenSpansAlternatives(s string) bool {
	if !strings.Contains(s, "||") {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '-' && isSpace(s[i-1]) {
			return true
		}
	}
	return false
}

// isSpace is the whitespace of Masterminds' constraint regexps.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// Check reports whether v satisfies the range, as msemver.Constraints.Check.
func (r *SemverRange) Check(v *msemver.Version) bool {
	sv := semverVersion{major: v.Major(), minor: v.Minor(), patch: v.Patch(), pre: v.Prerelease()}
	return r.check(sv, v)
}

// check is Check for a parsed version; mv is its Masterminds form, built on demand when an
// alternative outside the hand-parsed grammar needs it.
func (r *SemverRange) check(v semverVersion, mv *msemver.Version) bool {
	for i := range r.groups {
		g := &r.groups[i]
		if v.pre != "" && !g.admitsPre(v) {
			continue
		}
		if g.ms != nil {
			if mv == nil {
				mv = v.semver()
			}
			if g.ms.Check(mv) {
				return true
			}
			continue
		}
		ok := true
		for j := range g.cmps {
			if !g.cmps[j].check(v) {
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

// admitsPre reports whether a comparator of the alternative names a prerelease of v's
// release, the only prereleases npm admits (semverGroup.pre).
func (g *semverGroup) admitsPre(v semverVersion) bool {
	return slices.Contains(g.pre, [3]uint64{v.major, v.minor, v.patch})
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
		return r.check(v, nil)
	}, nil
}

// parseSemverGroup parses one `||` alternative of the grammar NewSemverRange handles itself:
// comparators separated by blanks or a comma, each an optional operator (`=`, `>`, `<`, `>=`,
// `=>`, `<=`, `=<`, `~`, `~>`, `^`) and a version: `v`? then one to three parts, each a
// decimal without leading zero or a wildcard (`x`, `X`, `*`); only a full numeric version may
// have a prerelease and metadata. ok is false for anything else, Masterminds deciding then.
func parseSemverGroup(s string) (g semverGroup, ok bool) {
	for i := 1; i < len(s); i++ {
		// Masterminds rewrites `A - B` (blanks around the hyphen) first: a hyphen range.
		if s[i] == '-' && isBlank(s[i-1]) {
			return semverGroup{}, false
		}
	}
	i := skipBlanks(s, 0)
	if i == len(s) {
		return semverGroup{}, false
	}
	for i < len(s) {
		c, j, ok := parseSemverComparator(s, i)
		if !ok {
			return semverGroup{}, false
		}
		g.cmps = append(g.cmps, c)
		if c.con.pre != "" {
			g.pre = append(g.pre, [3]uint64{c.con.major, c.con.minor, c.con.patch})
		}
		// A comparator ends at blanks, a comma or the end; a comma may be surrounded by blanks
		// but must be followed by another comparator.
		k := skipBlanks(s, j)
		comma := k < len(s) && s[k] == ','
		if comma {
			k = skipBlanks(s, k+1)
		}
		if k == j && k < len(s) || comma && k == len(s) {
			return semverGroup{}, false
		}
		i = k
	}
	return g, true
}

// semverFallback is the alternative of the Masterminds constraint c, which must not apply
// its own prerelease rule (semverGroup.pre).
func semverFallback(c *msemver.Constraints) semverGroup {
	c.IncludePrerelease = true
	return semverGroup{ms: c, pre: semverFallbackPre(c)}
}

// semverFallbackPre reads the comparators of the Masterminds constraint c back from its
// String (each the operator and the version as written, blanks between) and returns the
// [major, minor, patch] of those naming a prerelease, a missing or wildcard part counting as
// zero as parseConstraint fills it in. Masterminds keeps no comparator a caller could read,
// and semverGroup.pre needs the releases of an alternative outside the hand-parsed grammar
// as much as of one inside it.
func semverFallbackPre(c *msemver.Constraints) [][3]uint64 {
	var pre [][3]uint64
	for tok := range strings.FieldsSeq(c.String()) {
		ver := strings.TrimLeft(tok, "=!<>~^")
		if ver != "" && (ver[0] == 'v' || ver[0] == 'V') {
			ver = ver[1:]
		}
		if t, rest, ok := semverFallbackRelease(ver); ok && strings.HasPrefix(rest, "-") {
			pre = append(pre, t)
		}
	}
	return pre
}

// semverFallbackRelease reads the one to three dot-separated parts of ver, each a run of the
// bytes Masterminds' version regexp allows, and returns what follows them. A wildcard part
// and every part after it count as zero, as parseConstraint drops them.
func semverFallbackRelease(ver string) (t [3]uint64, rest string, ok bool) {
	i, wild := 0, false
	for n := range 3 {
		j := i
		for j < len(ver) && ('0' <= ver[j] && ver[j] <= '9' || ver[j] == 'x' || ver[j] == 'X' ||
			ver[j] == '*' || ver[j] == '|') {
			j++
		}
		if j == i {
			return t, "", false
		}
		switch part := ver[i:j]; {
		case part == "x" || part == "X" || part == "*":
			wild = true
		case !wild:
			u, err := strconv.ParseUint(part, 10, 64)
			if err != nil {
				return t, "", false
			}
			t[n] = u
		}
		i = j
		if i == len(ver) || ver[i] != '.' {
			break
		}
		i++
	}
	return t, ver[i:], true
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
