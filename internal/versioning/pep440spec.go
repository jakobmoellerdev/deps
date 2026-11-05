package versioning

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// PEP440Admits reports whether version satisfies the specifier set spec. spec is a PEP 440
// specifier set (`>=3.8,!=3.9.*,<4`; operators ==, ===, ~=, !=, <, <=, >, >=, ==X.*, !=X.*)
// or a Poetry constraint (`^3.9`, `~3.9`, a bare version meaning ==, `*`, clauses also
// separated by spaces, alternatives by `||`); "" admits everything. Prereleases are admitted
// like any version (callers filter them); local versions are compared by their public part.
// As in PEP 440, `<V` excludes the prereleases of V and `>V` its post releases.
//
// version is checked against each clause in turn: v is in an intersection exactly when it
// is in every clause, so the clauses' sets are never intersected.
func PEP440Admits(spec, version string) (bool, error) {
	v, ok := parsePEP440(version)
	v.local = ""
	admits, alts := false, 0
	for alt := range strings.FieldsFuncSeq(spec, isPipe) {
		in := true
		err := eachPEP440Clause(alt, false, func(c pepSet) { in = in && c.contains(v) })
		if err != nil {
			return false, fmt.Errorf("invalid version specifier %q: %w", spec, err)
		}
		admits, alts = admits || in, alts+1
	}
	if !ok {
		return false, fmt.Errorf("invalid PEP 440 version %q", version)
	}
	return admits || alts == 0, nil // without alternatives spec is blank and admits all
}

// PEP440Spec is a specifier set compiled by ParsePEP440Spec, for checking many versions.
type PEP440Spec struct {
	alts [][]pepSet // the sets of the clauses of each `||` alternative
}

// ParsePEP440Spec compiles spec (see PEP440Admits), failing as PEP440Admits does.
func ParsePEP440Spec(spec string) (*PEP440Spec, error) {
	var p PEP440Spec
	for alt := range strings.FieldsFuncSeq(spec, isPipe) {
		var clauses []pepSet
		if err := eachPEP440Clause(alt, false, func(c pepSet) { clauses = append(clauses, c) }); err != nil {
			return nil, fmt.Errorf("invalid version specifier %q: %w", spec, err)
		}
		p.alts = append(p.alts, clauses)
	}
	return &p, nil
}

// Admits is PEP440Admits for the compiled specifier set.
func (p *PEP440Spec) Admits(version string) (bool, error) {
	v, ok := parsePEP440(version)
	if !ok {
		return false, fmt.Errorf("invalid PEP 440 version %q", version)
	}
	v.local = ""
	if len(p.alts) == 0 { // spec is blank and admits all
		return true, nil
	}
	for _, clauses := range p.alts {
		if !slices.ContainsFunc(clauses, func(c pepSet) bool { return !c.contains(v) }) {
			return true, nil
		}
	}
	return false, nil
}

// PEP440Subset reports whether every version admitted by the specifier set a is admitted by
// b (both in PEP440Admits' syntax): a project requiring Python a can install a package
// requiring b. Only final releases count (`==3.11.*` is a subset of `>=3.11` although
// 3.11.0rc1 is not admitted by the latter), taken as dense: `>=3.9` is no subset of `>=3.9.1`.
func PEP440Subset(a, b string) (bool, error) {
	sa, err := parsePEP440Set(a, true)
	if err != nil {
		return false, err
	}
	sb, err := parsePEP440Set(b, true)
	if err != nil {
		return false, err
	}
	return len(sa.intersect(sb.complement())) == 0, nil
}

// pepBound is one end of a version interval; inf marks the unbounded end.
type pepBound struct {
	v    pep440Version
	inf  bool
	incl bool
}

// pepInterval is a non-empty range of versions.
type pepInterval struct{ lo, hi pepBound }

// pepSet is a union of intervals; nil is the empty set.
type pepSet []pepInterval

// pepAll admits every version.
var pepAll = pepSet{{lo: pepBound{inf: true}, hi: pepBound{inf: true}}}

func (s pepSet) contains(v pep440Version) bool {
	for _, iv := range s {
		if iv.lo.below(v) && iv.hi.above(v) {
			return true
		}
	}
	return false
}

// below reports whether lower bound b admits v.
func (b pepBound) below(v pep440Version) bool {
	if b.inf {
		return true
	}
	c := comparePEP440(b.v, v)
	return c < 0 || c == 0 && b.incl
}

// above reports whether upper bound b admits v.
func (b pepBound) above(v pep440Version) bool {
	if b.inf {
		return true
	}
	c := comparePEP440(b.v, v)
	return c > 0 || c == 0 && b.incl
}

func (s pepSet) intersect(t pepSet) pepSet {
	var out pepSet
	for _, a := range s {
		for _, b := range t {
			iv := pepInterval{lo: tighter(a.lo, b.lo, 1), hi: tighter(a.hi, b.hi, -1)}
			if iv.nonEmpty() {
				out = append(out, iv)
			}
		}
	}
	return out
}

// tighter returns the greater lower bound (dir 1) or the smaller upper bound (dir -1); at
// equal versions the exclusive bound.
func tighter(a, b pepBound, dir int) pepBound {
	switch {
	case a.inf:
		return b
	case b.inf:
		return a
	}
	if c := comparePEP440(a.v, b.v) * dir; c != 0 {
		if c > 0 {
			return a
		}
		return b
	}
	if !a.incl {
		return a
	}
	return b
}

func (iv pepInterval) nonEmpty() bool {
	if iv.lo.inf || iv.hi.inf {
		return true
	}
	c := comparePEP440(iv.lo.v, iv.hi.v)
	return c < 0 || c == 0 && iv.lo.incl && iv.hi.incl
}

// complement returns the versions s does not admit.
func (s pepSet) complement() pepSet {
	out := pepAll
	for _, iv := range s {
		var c pepSet
		if !iv.lo.inf {
			c = append(c, pepInterval{lo: pepBound{inf: true}, hi: pepBound{v: iv.lo.v, incl: !iv.lo.incl}})
		}
		if !iv.hi.inf {
			c = append(c, pepInterval{lo: pepBound{v: iv.hi.v, incl: !iv.hi.incl}, hi: pepBound{inf: true}})
		}
		out = out.intersect(c)
	}
	return out
}

// parsePEP440Set parses spec (see PEP440Admits): `||` alternatives of clauses separated by
// commas or spaces. final restricts the set to final releases: prereleases and post releases
// are left out of the bounds.
func parsePEP440Set(spec string, final bool) (pepSet, error) {
	var out pepSet
	for alt := range strings.FieldsFuncSeq(spec, isPipe) {
		set, err := parsePEP440And(alt, final)
		if err != nil {
			return nil, fmt.Errorf("invalid version specifier %q: %w", spec, err)
		}
		out = append(out, set...)
	}
	if out == nil && strings.TrimSpace(strings.ReplaceAll(spec, "|", "")) == "" {
		return pepAll, nil
	}
	return out, nil
}

func isPipe(r rune) bool { return r == '|' }

// pepSpecOps are the specifier operators, longest first.
var pepSpecOps = []string{"===", "==", "!=", "~=", "<=", ">=", "<", ">", "^", "~", "="}

// parsePEP440And parses clauses separated by commas or spaces; a space may also separate an
// operator from its version.
func parsePEP440And(s string, final bool) (pepSet, error) {
	set := pepAll
	if err := eachPEP440Clause(s, final, func(c pepSet) { set = set.intersect(c) }); err != nil {
		return nil, err
	}
	return set, nil
}

// eachPEP440Clause passes the set of each clause of s to f in order (see parsePEP440And) and
// stops at the first invalid one.
func eachPEP440Clause(s string, final bool, f func(pepSet)) error {
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
		if i == len(s) {
			return nil
		}
		op := ""
		for _, o := range pepSpecOps {
			if strings.HasPrefix(s[i:], o) {
				op = o
				break
			}
		}
		i += len(op)
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != ',' {
			j++
		}
		if i == j {
			return fmt.Errorf("operator %q without version", op)
		}
		c, err := pepClause(op, s[i:j], final)
		if err != nil {
			return err
		}
		f(c)
		i = j
	}
}

// pepClause returns the versions admitted by one clause (final: see parsePEP440Set).
func pepClause(op, ver string, final bool) (pepSet, error) {
	if ver == "*" && (op == "" || op == "==" || op == "=") {
		return pepAll, nil
	}
	prefix, wild := strings.CutSuffix(ver, ".*")
	v, ok := parsePEP440(prefix)
	if !ok {
		return nil, fmt.Errorf("invalid version %q", ver)
	}
	if wild {
		if v.pre != pepNoPre || v.hasPost || v.hasDev || v.local != "" {
			return nil, fmt.Errorf("invalid wildcard version %q", ver)
		}
		switch op {
		case "", "==", "=":
			return pepRange(familyStart(v, final), pepNext(v, v.n-1, final)), nil
		case "!=":
			return pepRange(familyStart(v, final), pepNext(v, v.n-1, final)).complement(), nil
		}
		return nil, fmt.Errorf("wildcard version %q with %q", ver, op)
	}
	v.local = ""
	point := pepSet{{lo: pepBound{v: v, incl: true}, hi: pepBound{v: v, incl: true}}}
	switch op {
	case "", "==", "===", "=":
		return point, nil
	case "!=":
		return point.complement(), nil
	case ">=":
		return pepSet{{lo: pepBound{v: v, incl: true}, hi: pepBound{inf: true}}}, nil
	case "<=":
		return pepSet{{lo: pepBound{inf: true}, hi: pepBound{v: v, incl: true}}}, nil
	case ">":
		if !final && v.pre == pepNoPre && !v.hasDev && !v.hasPost { // >V excludes V's post releases
			v.hasPost, v.post, v.hasDev, v.dev = true, math.MaxInt, false, 0
		}
		return pepSet{{lo: pepBound{v: v}, hi: pepBound{inf: true}}}, nil
	case "<":
		if v.pre == pepNoPre && !v.hasDev && !v.hasPost { // <V excludes V's prereleases
			v = familyStart(v, final)
		}
		return pepSet{{lo: pepBound{inf: true}, hi: pepBound{v: v}}}, nil
	case "~=":
		if v.n < 2 {
			return nil, fmt.Errorf("~= needs two release parts: %q", ver)
		}
		return pepRange(v, pepNext(v, v.n-2, final)), nil
	case "^":
		k := 0
		for k < v.n-1 && v.release[k] == 0 {
			k++
		}
		return pepRange(v, pepNext(v, k, final)), nil
	case "~":
		return pepRange(v, pepNext(v, min(1, v.n-1), final)), nil
	}
	return nil, fmt.Errorf("unknown operator %q", op)
}

// pepRange is [lo, hi).
func pepRange(lo, hi pep440Version) pepSet {
	return pepSet{{lo: pepBound{v: lo, incl: true}, hi: pepBound{v: hi}}}
}

// familyStart is the lowest version of v's release: its first dev release (V.dev0), or the
// release itself when final.
func familyStart(v pep440Version, final bool) pep440Version {
	return pep440Version{epoch: v.epoch, release: v.release, n: v.n, pre: pepNoPre, hasDev: !final}
}

// pepNext is the lowest version after every version starting with v's release parts 0..k: part
// k incremented, the parts after it dropped (familyStart of that).
func pepNext(v pep440Version, k int, final bool) pep440Version {
	n := pep440Version{epoch: v.epoch, n: k + 1, pre: pepNoPre, hasDev: !final}
	copy(n.release[:k+1], v.release[:k+1])
	n.release[k]++
	return n
}
