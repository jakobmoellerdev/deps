package npm

import (
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// An npm range (node-semver's grammar) is a set of `||` alternatives; each is a hyphen range
// (`1.2.3 - 2.3.4`) or space-separated comparators: an optional operator (`^`, `~`, `~>`, `>=`,
// `>`, `<=`, `<`, `=`) and a partial version with `x`, `X` or `*` wildcards (`1.x`, `^1.2`,
// `v1.2.3-rc.1`). Offsets are byte offsets into the range string.

// version is a partial version as written: parts are digits or a wildcard, pre is the
// prerelease and build suffix of a full version (with its `-` or `+`).
type version struct {
	start, end int
	parts      []string
	pre        string
	v          bool // written with a `v` prefix
}

// nums returns the numeric parts before the first wildcard.
func (pv version) nums() []int {
	var out []int
	for _, p := range pv.parts {
		if isWild(p) {
			break
		}
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// full is the lowest version pv admits as MAJOR.MINOR.PATCH[-pre] (missing parts zero), "" for
// a version without a number (`*`).
func (pv version) full() string {
	n := pv.nums()
	if len(n) == 0 {
		return ""
	}
	n = append(n, 0, 0, 0)[:3]
	s := strconv.Itoa(n[0]) + "." + strconv.Itoa(n[1]) + "." + strconv.Itoa(n[2])
	if len(pv.nums()) == 3 {
		s += pv.pre
	}
	return s
}

// at writes v (a full version) at pv's precision: as many parts as pv has, its wildcards kept;
// v's prerelease only when pv is a full version.
func (pv version) at(v *semver.Version) string {
	var b strings.Builder
	if pv.v {
		b.WriteByte('v')
	}
	nv := [3]uint64{v.Major(), v.Minor(), v.Patch()}
	wild := false
	for i, p := range pv.parts {
		if i > 0 {
			b.WriteByte('.')
		}
		if wild = wild || isWild(p); wild {
			b.WriteString(p)
			continue
		}
		b.WriteString(strconv.FormatUint(nv[i], 10))
	}
	if len(pv.parts) == 3 && !wild && v.Prerelease() != "" {
		b.WriteString("-" + v.Prerelease())
	}
	return b.String()
}

// comparator is one comparison; op "" is a bare version (an x-range or an exact version).
type comparator struct {
	op    string
	start int // of the operator
	ver   version
}

// floor reports whether the comparator bounds the alternative from below by its version.
func (c comparator) floor() bool { return c.op != "<" && c.op != "<=" && len(c.ver.nums()) > 0 }

// alternative is one `||` alternative: comparators that must all hold, or a hyphen range
// (cmps[0] its low, cmps[1] its high end, both with op "").
type alternative struct {
	start, end int
	cmps       []comparator
	hyphen     bool
}

// npmRange is a parsed npm range.
type npmRange struct {
	raw  string
	alts []alternative
}

func isWild(p string) bool { return p == "x" || p == "X" || p == "*" }

// ops are the comparison operators, longest first.
var ops = []string{"~>", ">=", "<=", "^", "~", ">", "<", "="}

// parseRange parses an npm range; ok is false for anything outside the grammar, an empty
// alternative, or a range without any version (`*`, `x`).
func parseRange(s string) (npmRange, bool) {
	r := npmRange{raw: s}
	anyNum := false
	start := 0
	for {
		end := strings.Index(s[start:], "||")
		if end < 0 {
			end = len(s)
		} else {
			end += start
		}
		a, ok := parseAlternative(s, start, end)
		if !ok {
			return npmRange{}, false
		}
		for _, c := range a.cmps {
			anyNum = anyNum || len(c.ver.nums()) > 0
		}
		r.alts = append(r.alts, a)
		if end == len(s) {
			break
		}
		start = end + 2
	}
	return r, anyNum
}

// parseAlternative parses s[start:end].
func parseAlternative(s string, start, end int) (alternative, bool) {
	a := alternative{}
	i := skipSpace(s, start, end)
	a.start = i
	for i < end {
		c := comparator{start: i}
		for _, o := range ops {
			if strings.HasPrefix(s[i:end], o) {
				c.op = o
				break
			}
		}
		j := skipSpace(s, i+len(c.op), end)
		v, ok := parseVersion(s, j, end)
		if !ok {
			return alternative{}, false
		}
		c.ver = v
		a.cmps = append(a.cmps, c)
		a.end = v.end
		i = skipSpace(s, v.end, end)
		if i < end && s[i] == '-' && c.op == "" && len(a.cmps) == 1 && !a.hyphen && i > v.end {
			k := skipSpace(s, i+1, end)
			if k == i+1 {
				return alternative{}, false
			}
			hi, ok := parseVersion(s, k, end)
			if !ok {
				return alternative{}, false
			}
			a.cmps, a.hyphen, a.end = append(a.cmps, comparator{start: k, ver: hi}), true, hi.end
			if i = skipSpace(s, hi.end, end); i < end {
				return alternative{}, false
			}
		}
	}
	return a, len(a.cmps) > 0
}

func skipSpace(s string, i, end int) int {
	for i < end && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// parseVersion reads a partial version at i: `v`? xr(.xr(.xr pre? build?)?)?.
func parseVersion(s string, i, end int) (version, bool) {
	v := version{start: i}
	if i < end && (s[i] == 'v' || s[i] == 'V') {
		v.v = true
		i++
	}
	for {
		j := i
		for j < end && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		switch {
		case j > i:
			if _, err := strconv.Atoi(s[i:j]); err != nil {
				return version{}, false
			}
		case j < end && isWild(s[j:j+1]):
			j++
		default:
			return version{}, false
		}
		v.parts = append(v.parts, s[i:j])
		i = j
		if len(v.parts) == 3 || i >= end || s[i] != '.' {
			break
		}
		i++
	}
	if len(v.parts) == 3 && !isWild(v.parts[2]) && i < end && (s[i] == '-' || s[i] == '+') {
		j := i + 1
		for j < end && (s[j] == '-' || s[j] == '.' || s[j] == '+' || s[j] >= '0' && s[j] <= '9' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
			j++
		}
		if j == i+1 {
			return version{}, false
		}
		v.pre, i = s[i:j], j
	}
	if i < end && s[i] != ' ' && s[i] != '\t' {
		return version{}, false
	}
	v.end = i
	return v, true
}

// anchor is the version that stands for the range (dep.Dep.Version): in the last alternative
// the lowest version of its first floor comparator (`^1.2` → 1.2.0, `1.x` → 1.0.0), else of
// its first upper bound (`<2` → 2.0.0).
func (r npmRange) anchor() string {
	last := r.alts[len(r.alts)-1]
	for _, c := range last.cmps {
		if c.floor() {
			return c.ver.full()
		}
	}
	for _, c := range last.cmps {
		if f := c.ver.full(); f != "" {
			return f
		}
	}
	return ""
}

// admits reports whether the range admits v (as a Masterminds constraint).
func admits(rng, v string) bool {
	c, err := semver.NewConstraint(rng)
	if err != nil {
		return false
	}
	sv, err := semver.NewVersion(v)
	return err == nil && c.Check(sv)
}

// newRange writes the range for v under rangeStrategy strategy (dep.Update.RangeStrategy),
// after Renovate's npm getNewValue: pin writes v; a version the range admits raises the floors
// of the first alternative admitting it (bump: `^1.2.3` → `^1.4.0`, `>=1.2.3 <2` → `>=1.4.0 <2`,
// the low end of a hyphen range); one outside it is, under widen, admitted by moving the upper
// bounds of the last alternative (`<2` → `<4` for 3.1.0, the high end of a hyphen range) or
// else by appending the replaced last alternative (`^1.2.3` → `^1.2.3 || ^2.0.0`); under any
// other strategy, and for an exact version, the last alternative is replaced and becomes the
// whole range: floors take v, upper bounds move past it, a hyphen range moves its high end.
// Versions keep their precision, wildcards and `v` prefix (`1.x` → `2.x`, `^1.2` → `^2.0`);
// `>` becomes `>=` when it takes v.
func (r npmRange) newRange(v, strategy string) string {
	nv, err := semver.NewVersion(v)
	if err != nil {
		return r.raw
	}
	if strategy == dep.RangePin {
		return v
	}
	if admits(r.raw, v) {
		for _, a := range r.alts {
			if admits(r.raw[a.start:a.end], v) {
				return r.raw[:a.start] + r.render(a, nv, modeBump) + r.raw[a.end:]
			}
		}
		return r.raw
	}
	last := r.alts[len(r.alts)-1]
	if strategy == dep.RangeWiden && !r.exact() {
		if w := r.raw[:last.start] + r.render(last, nv, modeWiden) + r.raw[last.end:]; w != r.raw && admits(w, v) {
			return w
		}
		sep := " || "
		if len(r.alts) > 1 {
			sep = r.raw[r.alts[len(r.alts)-2].end:last.start]
		}
		return r.raw[:last.end] + sep + r.render(last, nv, modeReplace) + r.raw[last.end:]
	}
	return r.render(last, nv, modeReplace)
}

// exact reports whether the range is one exact version (`1.2.3`, `=1.2.3`).
func (r npmRange) exact() bool {
	if len(r.alts) != 1 || len(r.alts[0].cmps) != 1 {
		return false
	}
	c := r.alts[0].cmps[0]
	return (c.op == "" || c.op == "=") && len(c.ver.nums()) == 3
}

// Rewrite modes of render.
const (
	modeReplace = iota // v is outside the range: floors take v, upper bounds move past it
	modeBump           // v is admitted: floors take v
	modeWiden          // v is outside the range: only upper bounds move past it
)

// render writes alternative a for v in mode, keeping the bytes between its comparators.
func (r npmRange) render(a alternative, v *semver.Version, mode int) string {
	var b strings.Builder
	pos := a.start
	set := func(c comparator, op, ver string) {
		from := c.ver.start
		if op != c.op {
			from = c.start
			ver = op + ver
		}
		b.WriteString(r.raw[pos:from])
		b.WriteString(ver)
		pos = c.ver.end
	}
	for i, c := range a.cmps {
		upper := c.op == "<" || c.op == "<=" || a.hyphen && i == 1
		switch {
		case upper && mode == modeBump, upper && below(v, c, a.hyphen):
		case upper && c.op == "<":
			set(c, c.op, boundary(v, c.ver))
		case upper:
			set(c, c.op, c.ver.at(v))
		case !c.floor(), mode == modeWiden, a.hyphen && mode == modeReplace:
			// wildcards, and floors that stay: under widen, and a hyphen range's low end
		case c.op == ">":
			set(c, ">=", c.ver.at(v))
		default:
			set(c, c.op, c.ver.at(v))
		}
	}
	b.WriteString(r.raw[pos:a.end])
	return b.String()
}

// below reports whether v is within upper bound c: `<V` below V, `<=V` and a hyphen range's
// high end at or below it (partial: within its last written part, `- 2` admits 2.9.9).
func below(v *semver.Version, c comparator, hyphen bool) bool {
	bound, err := semver.NewVersion(c.ver.full())
	if err != nil {
		return true
	}
	switch {
	case c.op == "<":
		return v.LessThan(bound)
	case hyphen && len(c.ver.nums()) < 3:
		return admits("<"+boundaryOf(bound, len(c.ver.nums())), v.String())
	}
	return !v.GreaterThan(bound)
}

// boundaryOf is the version after bound's first n parts (`2` → 3.0.0, `2.1` → 2.2.0).
func boundaryOf(bound *semver.Version, n int) string {
	switch n {
	case 1:
		return semver.New(bound.Major()+1, 0, 0, "", "").String()
	default:
		return semver.New(bound.Major(), bound.Minor()+1, 0, "", "").String()
	}
}

// boundary is the first version above v's compatibility range (4.0.0 for 3.1.0, 0.6.0 for
// 0.5.1, 0.0.4 for 0.0.3) written at like's precision, with enough parts to be exact.
func boundary(v *semver.Version, like version) string {
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
	pv := version{v: like.v, parts: make([]string, max(len(like.parts), need))}
	for i := range pv.parts {
		pv.parts[i] = "0"
	}
	return pv.at(next)
}
