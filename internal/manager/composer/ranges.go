package composer

import (
	"cmp"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// constraint is a parsed composer.json constraint and its matcher.
type constraint struct {
	c     versioning.ComposerConstraint
	match func(string) bool
}

// constraints caches parse by constraint string (nil when it does not parse): the engine
// rewrites a dep's constraint for every candidate release.
var constraints sync.Map // string -> *constraint

// parse returns the parsed constraint rng, or nil.
func parse(rng string) *constraint {
	if p, ok := constraints.Load(rng); ok {
		return p.(*constraint)
	}
	var p *constraint
	if c, err := versioning.ParseComposerConstraint(rng); err == nil {
		p = &constraint{c: c, match: c.Matcher()}
	}
	got, _ := constraints.LoadOrStore(rng, p)
	return got.(*constraint)
}

// anchor returns the version standing for constraint rng (dep.Dep.Version): in its last
// alternative the version of the first comparator with a lower bound (`^1.0|^2.3` → 2.3, a
// hyphen range's low end, `1.2.*` → 1.2), "" without one (`*`, `<2.0`); ok is false when rng
// does not parse.
func anchor(rng string) (string, bool) {
	p := parse(rng)
	if p == nil {
		return "", false
	}
	last := p.c.Alts[len(p.c.Alts)-1]
	for _, c := range last.Cmps {
		switch c.Op {
		case "", "=", "==", "^", "~", ">=", ">":
			if !isWildcard(c.Ver) {
				return c.Ver, true
			}
			if v := wildcardBase(c.Ver); v != "" {
				return v, true
			}
		}
	}
	return "", true
}

// isWildcard reports whether ver ends in a `*`, `x` or `X` part (`1.2.*`, `*`).
func isWildcard(ver string) bool {
	_, _, ok := versioning.ComposerVersionParts(ver)
	return !ok
}

// wildcardBase is the version before a wildcard part (`1.2.*` → 1.2, `*` → "").
func wildcardBase(ver string) string {
	if base, _, ok := strings.CutLast(ver, "."); ok {
		return base
	}
	return ""
}

// precision is the number of numeric parts ver is written with (`1.2.*` → 2, `*` → 0).
func precision(ver string) int {
	if p, _, ok := versioning.ComposerVersionParts(ver); ok {
		return len(p)
	}
	if b := wildcardBase(ver); b != "" {
		return precision(b)
	}
	return 0
}

// target is the new version: its numeric parts and stability suffix (`-beta1`).
type target struct {
	parts  []int
	suffix string
}

// at writes the version with n numeric parts (cut, or padded with zeros) in like's style (a
// `v` prefix); the stability suffix only when no part was cut.
func (t target) at(like string, n int) string {
	var b strings.Builder
	if like != "" && (like[0] == 'v' || like[0] == 'V') {
		b.WriteByte(like[0])
	}
	for i := range n {
		if i > 0 {
			b.WriteByte('.')
		}
		p := 0
		if i < len(t.parts) {
			p = t.parts[i]
		}
		b.WriteString(strconv.Itoa(p))
	}
	if n >= len(t.parts) {
		b.WriteString(t.suffix)
	}
	return b.String()
}

// full writes the whole version in like's style.
func (t target) full(like string) string { return t.at(like, len(t.parts)) }

// newRange writes constraint rng for version v under rangeStrategy strategy
// (dep.Update.RangeStrategy), after Renovate's composer getNewValue; ok is false when the
// constraint stays unchanged or does not admit v. pin writes v. A version the constraint
// admits raises the lower bounds of the first alternative admitting it at their precision
// (bump: `^1.2` → `^1.4`, `~1.2.3` → `~1.2.5`, `>=1.2 <2.0` → `>=1.4 <2.0`, a hyphen range's
// low end); exact versions, wildcards and upper bounds stay. One outside it is, under widen,
// admitted by moving the upper bounds of the last alternative (`>=1.0 <2.0` → `>=1.0 <3.0`, a
// hyphen range's high end) or else by appending the replaced last alternative with the
// constraint's separator (`^1.0` → `^1.0 || ^2.0`, `^1.0|^2.0` → `^1.0|^2.0|^3.0`); under any
// other strategy, and for an exact version, the replaced last alternative becomes the whole
// constraint (replaceAlt). Operators, `v` prefixes, stability flags (`@dev`) and the bytes
// between comparators are kept.
func newRange(rng, v, strategy string) (string, bool) {
	p := parse(rng)
	parts, suffix, ok := versioning.ComposerVersionParts(v)
	if p == nil || !ok {
		return "", false
	}
	t := target{parts: parts, suffix: suffix}
	alts := p.c.Alts
	last := alts[len(alts)-1]
	var out string
	switch {
	case strategy == dep.RangePin:
		out = t.full(alts[0].Cmps[0].Ver)
	case p.match(v):
		for _, a := range alts {
			if one := parse(rng[a.Start:a.End]); one != nil && one.match(v) {
				out = rng[:a.Start] + bumpAlt(rng, a, t) + rng[a.End:]
				break
			}
		}
	case strategy == dep.RangeWiden && !p.exact():
		out = rng[:last.Start] + widenAlt(rng, last, t, v) + rng[last.End:]
		if q := parse(out); q == nil || !q.match(v) {
			out = rng + cmp.Or(p.c.Sep, " || ") + replaceAlt(rng, last, t, v)
		}
	default:
		out = replaceAlt(rng, last, t, v)
	}
	if out == rng {
		return "", false
	}
	if q := parse(out); q == nil || !q.match(v) {
		return "", false
	}
	return out, true
}

// exact reports whether the constraint is one exact version (`1.2.3`, `==1.2.3`).
func (p *constraint) exact() bool {
	if len(p.c.Alts) != 1 || len(p.c.Alts[0].Cmps) != 1 {
		return false
	}
	c := p.c.Alts[0].Cmps[0]
	return (c.Op == "" || c.Op == "=" || c.Op == "==") && !isWildcard(c.Ver)
}

// render writes comparator c with operator op and version ver, keeping the blanks after its
// operator and its stability flag.
func render(rng string, c versioning.ComposerCmp, op, ver string) string {
	if op == c.Op && ver == c.Ver {
		return rng[c.Start:c.End]
	}
	gap := rng[c.Start+len(c.Op) : c.Off]
	if c.Op == "" {
		gap = ""
	}
	return op + gap + ver + rng[c.Off+len(c.Ver):c.End]
}

// renderAlt writes alternative a with its comparators replaced by texts, keeping the bytes
// between them.
func renderAlt(rng string, a versioning.ComposerAlt, texts []string) string {
	var b strings.Builder
	pos := a.Start
	for i, c := range a.Cmps {
		b.WriteString(rng[pos:c.Start])
		b.WriteString(texts[i])
		pos = c.End
	}
	b.WriteString(rng[pos:a.End])
	return b.String()
}

// bumpAlt raises the lower bounds of alternative a, which admits the new version, to it.
func bumpAlt(rng string, a versioning.ComposerAlt, t target) string {
	texts := make([]string, len(a.Cmps))
	for i, c := range a.Cmps {
		texts[i] = rng[c.Start:c.End]
		switch {
		case a.Hyphen && i == 0:
			texts[i] = render(rng, c, c.Op, t.at(c.Ver, precision(c.Ver)))
		case a.Hyphen, isWildcard(c.Ver):
		case c.Op == "^" || c.Op == "~" || c.Op == ">=":
			texts[i] = render(rng, c, c.Op, t.at(c.Ver, precision(c.Ver)))
		case c.Op == ">":
			texts[i] = render(rng, c, ">=", t.at(c.Ver, precision(c.Ver)))
		}
	}
	return renderAlt(rng, a, texts)
}

// admits reports whether comparator c alone admits v.
func admits(rng string, c versioning.ComposerCmp, v string) bool {
	p := parse(rng[c.Start:c.End])
	return p != nil && p.match(v)
}

// upper moves upper bound c past v: `<` at the bound's precision, its last non-zero part (`<2.0`
// → `<3.0`, `<1.3` → `<1.5` for 1.4.2), `<=` and a full hyphen high end to v, a partial hyphen
// high end (inclusive of its last part) to v at its precision. Bounds admitting v stay.
func upper(rng string, c versioning.ComposerCmp, hyphen bool, t target, v string) string {
	if !hyphen && admits(rng, c, v) {
		return rng[c.Start:c.End]
	}
	n := precision(c.Ver)
	switch {
	case hyphen && n < 3 && !versioning.IsComposerPrerelease(c.Ver):
		return render(rng, c, c.Op, t.at(c.Ver, n))
	case hyphen || c.Op == "<=":
		return render(rng, c, c.Op, t.full(c.Ver))
	}
	bound, _, _ := versioning.ComposerVersionParts(c.Ver)
	p := 0
	for i, x := range bound {
		if x != 0 {
			p = i
		}
	}
	out := target{parts: make([]int, len(bound))}
	for i := range p {
		if i < len(t.parts) {
			out.parts[i] = t.parts[i]
		}
	}
	if p < len(t.parts) {
		out.parts[p] = t.parts[p]
	}
	out.parts[p]++
	return render(rng, c, c.Op, out.full(c.Ver))
}

// widenAlt moves only the upper bounds of alternative a (`<`, `<=`, a hyphen range's high end)
// past the new version.
func widenAlt(rng string, a versioning.ComposerAlt, t target, v string) string {
	texts := make([]string, len(a.Cmps))
	for i, c := range a.Cmps {
		texts[i] = rng[c.Start:c.End]
		switch {
		case a.Hyphen && i == 1, c.Op == "<" || c.Op == "<=":
			texts[i] = upper(rng, c, a.Hyphen, t, v)
		}
	}
	return renderAlt(rng, a, texts)
}

// replaceAlt writes alternative a for a new version outside it: exact versions take it as
// published, wildcards at their precision (`1.2.*` → `1.4.*`), `^`, `~`, `>=` and `>` at
// their precision (`^5.2.8` → `^6.0.1`, `~1.2` → `~1.4`; `>` becomes `>=`), except that a
// two-part `^`/`~` moving to a new major takes `<major>.0` as Renovate writes it (`^4.1` →
// `^5.0` for 5.2.0), upper bounds move past it (upper), exclusions stay.
func replaceAlt(rng string, a versioning.ComposerAlt, t target, v string) string {
	texts := make([]string, len(a.Cmps))
	for i, c := range a.Cmps {
		texts[i] = rng[c.Start:c.End]
		n := precision(c.Ver)
		switch {
		case a.Hyphen && i == 0:
			texts[i] = render(rng, c, c.Op, t.at(c.Ver, n))
		case a.Hyphen:
			texts[i] = upper(rng, c, true, t, v)
		case isWildcard(c.Ver):
			if n > 0 {
				texts[i] = render(rng, c, c.Op, target{parts: t.parts}.at(c.Ver, n)+c.Ver[len(c.Ver)-2:])
			}
		case c.Op == "" || c.Op == "=" || c.Op == "==":
			texts[i] = render(rng, c, c.Op, t.full(c.Ver))
		case c.Op == "^" || c.Op == "~":
			cur, _, _ := versioning.ComposerVersionParts(c.Ver)
			w := t.at(c.Ver, n)
			if n == 2 && len(t.parts) > 0 && t.parts[0] > cur[0] {
				w = target{parts: []int{t.parts[0], 0}}.at(c.Ver, 2)
			}
			texts[i] = render(rng, c, c.Op, w)
		case c.Op == ">=" || c.Op == ">":
			texts[i] = render(rng, c, ">=", t.at(c.Ver, n))
		case c.Op == "<" || c.Op == "<=":
			texts[i] = upper(rng, c, false, t, v)
		}
	}
	return renderAlt(rng, a, texts)
}
