package cargo

import (
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// comparatorRe is one comparator of a Cargo requirement: an optional operator and a
// MAJOR[.MINOR[.PATCH[-pre][+build]]] version (group 2), optionally ending in a `.*`/`.x`
// wildcard (group 3), or a bare `*`/`x` wildcard (group 4).
var comparatorRe = regexp.MustCompile(`^(\^|~|=|>=|>|<=|<)?\s*(?:(\d+(?:\.\d+(?:\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)?)?)(\.[*xX])?|([*xX]))$`)

// comparator is one part of a requirement; ver is a slice of the requirement text at off.
type comparator struct {
	op, ver, wild string
	off           int
}

// floor reports whether the comparator bounds the requirement from below (its version is the
// one a bump raises).
func (c comparator) floor() bool { return c.ver != "" && c.op != "<" && c.op != "<=" }

// requirement is a parsed Cargo version requirement: comparators joined by commas.
type requirement struct {
	raw  string
	cmps []comparator
}

// parseRequirement parses a requirement as written; ok is false for anything Cargo's grammar
// does not have, or a wildcard combined with an operator.
func parseRequirement(raw string) (requirement, bool) {
	r := requirement{raw: raw}
	off := 0
	for part := range strings.SplitSeq(raw, ",") {
		lead := len(part) - len(strings.TrimLeft(part, " \t"))
		s := strings.TrimSpace(part)
		m := comparatorRe.FindStringSubmatchIndex(s)
		if m == nil {
			return requirement{}, false
		}
		c := comparator{}
		if m[2] >= 0 {
			c.op = s[m[2]:m[3]]
		}
		switch {
		case m[4] >= 0:
			c.ver, c.off = s[m[4]:m[5]], off+lead+m[4]
			if m[6] >= 0 {
				c.wild = s[m[6]:m[7]]
				if c.op != "" || strings.Count(c.ver, ".") == 2 {
					return requirement{}, false
				}
			}
		default:
			c.wild = s[m[8]:m[9]]
			if c.op != "" {
				return requirement{}, false
			}
		}
		r.cmps = append(r.cmps, c)
		off += len(part) + 1
	}
	return r, true
}

// version is the version a bump raises: that of the first floor comparator, "" for a
// requirement without one (`*`, `<2`).
func (r requirement) version() string {
	for _, c := range r.cmps {
		if c.floor() {
			return c.ver
		}
	}
	return ""
}

// constraint renders r as a Masterminds constraint: a bare version is Cargo's caret.
func (r requirement) constraint() string {
	parts := make([]string, len(r.cmps))
	for i, c := range r.cmps {
		op := c.op
		if op == "" && c.wild == "" {
			op = "^"
		}
		parts[i] = op + c.ver + c.wild
	}
	return strings.Join(parts, ", ")
}

// rewrite writes r for v under rangeStrategy strategy (dep.Update.RangeStrategy): pin writes
// `=v` (an exact requirement keeps its spacing), widen of a requirement that does not admit v
// and is not exact moves only its upper bounds (`>=1.2, <2` → `>=1.2, <3`) and, when that is
// not enough, turns it into `>=<floor>, <boundary above v>` (`1.2` → `>=1.2, <3` for 2.0.1);
// anything else is render.
func (r requirement) rewrite(v, strategy string) string {
	switch {
	case strategy == dep.RangePin && r.exact():
		c := r.cmps[0]
		return r.raw[:c.off] + v + r.raw[c.off+len(c.ver):]
	case strategy == dep.RangePin:
		return "=" + v
	case strategy == dep.RangeWiden && !r.exact() && !r.admits(v):
		if w := r.renderWith(v, true); w != r.raw {
			if n, ok := parseRequirement(w); ok && n.admits(v) {
				return w
			}
		}
		nv, err := semver.NewVersion(v)
		if floor := r.version(); err == nil && floor != "" {
			return ">=" + floor + ", <" + boundary(nv, 1)
		}
	}
	return r.render(v)
}

// exact reports whether r is one `=MAJOR.MINOR.PATCH` comparator.
func (r requirement) exact() bool {
	return len(r.cmps) == 1 && r.cmps[0].op == "=" && r.cmps[0].wild == "" && partsOf(r.cmps[0].ver) == 3
}

// admits reports whether r admits v (its Masterminds constraint).
func (r requirement) admits(v string) bool {
	c, err := semver.NewConstraint(r.constraint())
	if err != nil {
		return false
	}
	sv, err := semver.NewVersion(v)
	return err == nil && c.Check(sv)
}

// render writes r with its versions moved to v, keeping every operator, wildcard, spacing and
// the precision each version was written with: floor comparators take v (`1.2` → `2.0` for
// 2.0.1), upper bounds that v reaches move to the next compatibility boundary above v
// (`<2` → `<3`, `<0.5` → `<0.6`), `<=` bounds below v to v.
func (r requirement) render(v string) string { return r.renderWith(v, false) }

// renderWith is render, keeping the floor comparators when keepFloors.
func (r requirement) renderWith(v string, keepFloors bool) string {
	nv, err := semver.NewVersion(v)
	if err != nil {
		nv = nil
	}
	var b strings.Builder
	pos := 0
	for _, c := range r.cmps {
		if c.ver == "" {
			continue
		}
		repl := c.ver
		switch {
		case c.floor() && keepFloors:
		case c.floor():
			repl = truncate(v, c.ver)
		case nv == nil:
		case c.op == "<":
			if bound, err := semver.NewVersion(c.ver); err == nil && !nv.LessThan(bound) {
				repl = boundary(nv, partsOf(c.ver))
			}
		case c.op == "<=":
			if bound, err := semver.NewVersion(c.ver); err == nil && nv.GreaterThan(bound) {
				repl = truncate(v, c.ver)
			}
		}
		b.WriteString(r.raw[pos:c.off])
		b.WriteString(repl)
		pos = c.off + len(c.ver)
	}
	b.WriteString(r.raw[pos:])
	return b.String()
}

// partsOf counts the numeric parts of a version as written.
func partsOf(ver string) int {
	core, _, _ := strings.Cut(ver, "-")
	core, _, _ = strings.Cut(core, "+")
	return strings.Count(core, ".") + 1
}

// truncate writes v with as many parts as like (a full v for a full like).
func truncate(v, like string) string {
	n := partsOf(like)
	if n >= 3 {
		return v
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < n {
		return v
	}
	return strings.Join(parts[:n], ".")
}

// boundary is the first version above v's compatibility range (2.0.0 for 1.4.2, 0.6.0 for
// 0.5.1, 0.0.4 for 0.0.3), written with at least n parts and enough to be exact.
func boundary(v *semver.Version, n int) string {
	var next *semver.Version
	need := 1
	switch {
	case v.Major() > 0:
		next = semver.New(v.Major()+1, 0, 0, "", "")
	case v.Minor() > 0:
		next, need = semver.New(0, v.Minor()+1, 0, "", ""), 2
	default:
		next, need = semver.New(0, 0, v.Patch()+1, "", ""), 3
	}
	return truncate(next.String(), strings.Repeat("0.", max(n, need)-1)+"0")
}
