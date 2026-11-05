package pyreq

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// pep440 compares versions.
var pep440, _ = versioning.Get(Versioning)

// clause is one comparison of a specifier; offsets are into the specifier string.
type clause struct {
	op       string // "" = a bare Poetry or Pipenv version
	ver      string // without a trailing `.*`
	wild     bool   // trailing `.*`
	start    int    // first byte of the operator
	verStart int
	end      int // after the version and `.*`
}

// alt is one `||` alternative of a Poetry constraint: clauses that must all hold.
type alt struct {
	cls        []clause
	start, end int
}

// parseRange parses the trimmed specifier s of grammar g into its alternatives (Poetry `||`
// or `|`; one otherwise). Clauses are separated by commas (Poetry: also spaces); a space may
// separate an operator from its version. Versions must be PEP 440 versions; `.*` is allowed
// after `==`, `!=` and a bare version.
func parseRange(s string, g Grammar) ([]alt, bool) {
	var alts []alt
	cur := alt{start: 0}
	i := 0
	for {
		c, j, ok := parseClause(s, skipWS(s, i), g)
		if !ok {
			return nil, false
		}
		if len(cur.cls) == 0 {
			cur.start = c.start
		}
		cur.cls = append(cur.cls, c)
		cur.end = c.end
		i = skipWS(s, j)
		switch {
		case i == len(s):
			return append(alts, cur), true
		case s[i] == ',':
			i++
		case s[i] == '|' && g == Poetry:
			i++
			if i < len(s) && s[i] == '|' {
				i++
			}
			alts = append(alts, cur)
			cur = alt{}
		case g == Poetry && i > j: // space-separated clauses
		default:
			return nil, false
		}
	}
}

// parseClause reads one clause at i.
func parseClause(s string, i int, g Grammar) (clause, int, bool) {
	c := clause{start: i}
	for _, o := range ops[g] {
		if strings.HasPrefix(s[i:], o) {
			c.op = o
			break
		}
	}
	if c.op == "" && g == PEP440 {
		return c, i, false
	}
	j := skipWS(s, i+len(c.op))
	c.verStart = j
	for j < len(s) && !isWS(s[j]) && s[j] != ',' && s[j] != '|' {
		j++
	}
	c.end = j
	c.ver = s[c.verStart:j]
	if v, ok := strings.CutSuffix(c.ver, ".*"); ok && (c.op == "==" || c.op == "!=" || c.op == "") {
		c.ver, c.wild = v, true
	}
	_, valid := versioning.NormalizePEP440(c.ver)
	return c, j, valid
}

// anchor returns the version that stands for the trimmed specifier s (dep.Dep.Version): in
// its last alternative the first clause that is not an upper bound or exclusion, else its
// first `<` or `<=` clause.
func anchor(s string, g Grammar) (string, string) {
	if s == "" || s == "*" {
		return "", SkipUnconstrained
	}
	alts, ok := parseRange(s, g)
	if !ok {
		return "", SkipRange
	}
	var upper string
	for _, c := range alts[len(alts)-1].cls {
		switch c.op {
		case "<", "<=":
			if upper == "" {
				upper = c.ver
			}
		case "!=":
		default:
			return c.ver, ""
		}
	}
	if upper == "" {
		return "", SkipUnconstrained // only exclusions
	}
	return upper, ""
}

// InRange reports whether the range of d admits v.
func InRange(d dep.Dep, v string) bool {
	ok, err := versioning.PEP440Admits(d.Range, v)
	return ok && err == nil
}

// poetryManager is the name of the poetry manager, whose constraints may have `||`
// alternatives.
const poetryManager = "poetry"

// NewRange returns the range of d rewritten for v under strategy (see rangeEdits), or false
// when the rewrite leaves it unchanged or does not admit v.
func NewRange(d dep.Dep, v, strategy string) (string, bool) {
	edits, ok := rangeEdits(d.Range, v, strategy, d.Manager == poetryManager)
	if !ok {
		return "", false
	}
	out, err := patch.Apply([]byte(d.Range), edits)
	if err != nil || string(out) == d.Range || !InRange(dep.Dep{Range: string(out)}, v) {
		return "", false
	}
	return string(out), true
}

// Edits rewrites the range of u's dep for the new version under u.RangeStrategy (NewRange),
// clause by clause.
func Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if _, ok := NewRange(d, u.NewVersion, u.RangeStrategy); !ok {
		return nil, fmt.Errorf("range %q cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	edits, _ := rangeEdits(d.Range, u.NewVersion, u.RangeStrategy, d.Manager == poetryManager)
	for i := range edits {
		edits[i].Start += d.RefSpan.Start
		edits[i].End += d.RefSpan.Start
	}
	return edits, nil
}

// rangeEdits returns the edits of the specifier s for version v, after Renovate's pep440
// getNewValue (lib/modules/versioning/pep440/range.ts): under pin s becomes `==v` (an exact
// clause keeps its operator); otherwise a range admitting v is bumped (handleBumpStrategy) in
// the first alternative admitting it, one that does not is replaced (handleReplaceStrategy),
// under widen only its upper bounds when that admits v (handleWidenStrategy). A Poetry
// constraint (poetry) with several alternatives, or under widen one that is not exact, gets the
// replaced last alternative appended instead (Renovate's npm widen of `||` ranges, which its
// poetry versioning uses).
func rangeEdits(s, v, strategy string, poetry bool) ([]patch.Edit, bool) {
	alts, ok := parseRange(s, Poetry) // a superset of every grammar
	if !ok || !pep440.Valid(v) {
		return nil, false
	}
	if strategy == dep.RangePin {
		return pinEdits(s, alts, v), true
	}
	nv := release(v)
	if InRange(dep.Dep{Range: s}, v) {
		for _, a := range alts {
			if InRange(dep.Dep{Range: s[a.start:a.end]}, v) {
				return renderAlt(s, a, v, nv, modeBump), true
			}
		}
		return nil, false
	}
	widen := strategy == dep.RangeWiden && !exact(alts)
	if len(alts) == 1 {
		if widen {
			edits := renderAlt(s, alts[0], v, nv, modeWiden)
			if out, err := patch.Apply([]byte(s), edits); err == nil && InRange(dep.Dep{Range: string(out)}, v) {
				return edits, true
			}
		}
		if !widen || !poetry {
			return renderAlt(s, alts[0], v, nv, modeReplace), true
		}
	}
	last := alts[len(alts)-1]
	edits := renderAlt(s, last, v, nv, modeReplace)
	for i := range edits {
		edits[i].Start -= last.start
		edits[i].End -= last.start
	}
	text, err := patch.Apply([]byte(s[last.start:last.end]), edits)
	if err != nil {
		return nil, false
	}
	sep := " || "
	if len(alts) > 1 {
		sep = s[alts[len(alts)-2].end:last.start]
	}
	return []patch.Edit{{Start: last.end, End: last.end, New: sep + string(text)}}, true
}

// exact reports whether the specifier is one exact clause (`==1.2.3`, `===1.2.3`, a bare Poetry
// or Pipenv version).
func exact(alts []alt) bool {
	if len(alts) != 1 || len(alts[0].cls) != 1 {
		return false
	}
	c := alts[0].cls[0]
	return !c.wild && (c.op == "==" || c.op == "===" || c.op == "")
}

// pinEdits pins s to v: an exact specifier takes v as its version, anything else becomes `==v`.
func pinEdits(s string, alts []alt, v string) []patch.Edit {
	if !exact(alts) {
		return []patch.Edit{{Start: 0, End: len(s), Old: s, New: "==" + v}}
	}
	c := alts[0].cls[0]
	if c.ver == v {
		return nil
	}
	return []patch.Edit{{Start: c.verStart, End: c.end, Old: s[c.verStart:c.end], New: v}}
}

// Rewrite modes of renderAlt.
const (
	modeReplace = iota // v is outside the range: every clause follows it
	modeBump           // v is admitted: the floors are raised to it
	modeWiden          // v is outside the range: only the upper bounds follow it
)

// renderAlt rewrites the clauses of a for v (release nv) in mode.
func renderAlt(s string, a alt, v string, nv []int, mode int) []patch.Edit {
	trim := false // Renovate's hasZeroSpecifier
	for _, c := range a.cls {
		trim = trim || len(release(c.ver)) < 3
	}
	var edits []patch.Edit
	set := func(c clause, op, ver string) {
		if c.wild {
			ver += ".*"
		}
		e := patch.Edit{Start: c.start, End: c.end, New: op + ver}
		if op == c.op {
			e.Start, e.New = c.verStart, ver
		}
		if e.Old = s[e.Start:e.End]; e.Old != e.New {
			edits = append(edits, e)
		}
	}
	for _, c := range a.cls {
		base := release(c.ver)
		switch {
		case mode == modeWiden && c.op != "<" && c.op != "<=":
		case mode == modeBump && c.op == ">=":
			set(c, c.op, v)
		case mode == modeBump:
			updateValue(c, v, nv, set)
		case c.op == "<" && pep440.Compare(v, c.ver) >= 0:
			f := future(rangePrecision(a.cls), nv, base)
			if trim {
				f = trimZeros(f)
			}
			set(c, c.op, join(f))
		case c.op == ">" || c.op == ">=":
			if pep440.Compare(v, c.ver) <= 0 {
				set(c, ">=", v)
				continue
			}
			f := future(len(base)-1, nv, nil)
			if trim {
				f = trimZeros(f)
			}
			if c.op == ">" && join(f) == v && len(f) > 1 {
				f = f[:len(f)-1]
			}
			set(c, c.op, join(f))
		default:
			updateValue(c, v, nv, set)
		}
	}
	return edits
}

// updateValue is Renovate's updateRangeValue: exclusions are kept, wildcards move to v at
// their precision, `~=` (and Poetry's `^`, `~`) take v cut or zero-padded to their precision,
// pins and `<=` move up to v, `<` moves past v at its own precision and `>`/`>=` only down.
func updateValue(c clause, v string, nv []int, set func(clause, string, string)) {
	base := release(c.ver)
	switch {
	case c.op == "!=":
	case c.wild:
		set(c, c.op, join(future(-1, nv, base)))
	case c.op == "~=" || c.op == "^" || c.op == "~":
		switch {
		case len(base) < len(nv):
			set(c, c.op, join(nv[:len(base)]))
		case len(base) > len(nv):
			set(c, c.op, join(slices.Concat(nv, make([]int, len(base)-len(nv)))))
		default:
			set(c, c.op, v)
		}
	case c.op == "<":
		if pep440.Compare(v, c.ver) >= 0 {
			set(c, c.op, join(future(len(base)-1, nv, base)))
		}
	case c.op == ">" || c.op == ">=":
		if pep440.Compare(v, c.ver) <= 0 {
			set(c, ">=", v)
		}
	default: // ==, ===, =, bare, <=
		if pep440.Compare(v, c.ver) > 0 {
			set(c, c.op, v)
		}
	}
}

// rangePrecision is Renovate's getRangePrecision: the release part a two-clause range differs
// in (one more when that is the major followed by a zero), else the last part of the bound.
func rangePrecision(cls []clause) int {
	b := cls[0]
	if len(cls) > 1 {
		b = cls[1]
	}
	bound := release(b.ver)
	p := -1
	switch len(cls) {
	case 1:
		p = len(bound) - 1
	case 2:
		lower := release(cls[0].ver)
		for i, n := range bound {
			if i < len(lower) && n > lower[i] {
				p = i
				break
			}
		}
	}
	if p == 0 && len(bound) > 1 && bound[1] == 0 {
		p++
	}
	if p == -1 {
		p = len(bound) - 1
	}
	return p
}

// future is Renovate's getFutureVersion: base's length (nv's without base) with nv's parts
// before policy (all of them for policy < 0), nv's part at policy (plus one with a base) and
// zeros after it.
func future(policy int, nv, base []int) []int {
	inc := 1
	if base == nil {
		base, inc = nv, 0
	}
	out := make([]int, len(base))
	for i := range base {
		n := 0
		if i < len(nv) {
			n = nv[i]
		}
		switch {
		case policy < 0 || i < policy:
			out[i] = n
		case i == policy:
			out[i] = n + inc
		}
	}
	return out
}

// trimZeros drops trailing zero parts, keeping the first.
func trimZeros(r []int) []int {
	n := len(r)
	for n > 1 && r[n-1] == 0 {
		n--
	}
	return r[:n]
}

// release returns the release parts of a PEP 440 version (`v1!2.3rc1` → [2 3]).
func release(v string) []int {
	v = strings.TrimLeft(v, "vV")
	if _, rest, ok := strings.Cut(v, "!"); ok {
		v = rest
	}
	var out []int
	for p := range strings.SplitSeq(v, ".") {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		n, err := strconv.Atoi(p[:end])
		if err != nil {
			break
		}
		out = append(out, n)
		if end < len(p) {
			break
		}
	}
	return out
}

func join(r []int) string {
	var b strings.Builder
	for i, n := range r {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}
