package beam

import (
	"strconv"
	"strings"
	"sync"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// requirement is a parsed Elixir version requirement and its matcher.
type requirement struct {
	r     versioning.HexRequirement
	match func(string) bool
}

// requirements caches parse by requirement string (nil when it does not parse): the engine
// rewrites a dep's requirement for every candidate release.
var requirements sync.Map // string -> *requirement

// parse returns the parsed requirement rng, or nil.
func parse(rng string) *requirement {
	if p, ok := requirements.Load(rng); ok {
		return p.(*requirement)
	}
	var p *requirement
	if r, err := versioning.ParseHexRequirement(rng); err == nil {
		p = &requirement{r: r, match: r.Matcher()}
	}
	got, _ := requirements.LoadOrStore(rng, p)
	return got.(*requirement)
}

// anchor returns the version standing for requirement rng (dep.Dep.Version): in its last
// alternative the version of the first comparator with a lower bound (`~> 1.0 or ~> 2.3` →
// 2.3.0; a two-part `~>` padded with a zero patch), "" without one (`< 2.0.0`, and `>= 0.0.0`,
// which admits everything; ReadLocks takes the locked version then); ok is false when rng does
// not parse.
func anchor(rng string) (string, bool) {
	p := parse(rng)
	if p == nil {
		return "", false
	}
	alts := p.r.Alts
	for _, c := range alts[len(alts)-1].Cmps {
		switch c.Op {
		case ">=":
			if c.Ver == "0.0.0" {
				continue
			}
			return c.Ver, true
		case "", "==", "~>", ">":
			if c.Parts() == 2 {
				core, pre, _ := strings.Cut(c.Ver, "-")
				if pre != "" {
					pre = "-" + pre
				}
				return core + ".0" + pre, true
			}
			return c.Ver, true
		}
	}
	return "", true
}

// Complex reports whether the requirement rng has more than one comparator (`>= 1.0.0 and <
// 2.0.0`, `~> 1.0 or ~> 2.0`): rangeStrategy auto widens those of mix, and bump widens them
// for both managers, as in Renovate.
func Complex(rng string) bool {
	p := parse(rng)
	return p != nil && (len(p.r.Alts) > 1 || len(p.r.Alts[0].Cmps) > 1)
}

// target is the new version: its numeric parts and prerelease (without `-`).
type target struct {
	parts [3]uint64
	pre   string
}

// at writes the version with n numeric parts (2 or 3); the prerelease only with all three.
func (t target) at(n int) string {
	s := strconv.FormatUint(t.parts[0], 10) + "." + strconv.FormatUint(t.parts[1], 10)
	if n < 3 {
		return s
	}
	s += "." + strconv.FormatUint(t.parts[2], 10)
	if t.pre != "" {
		s += "-" + t.pre
	}
	return s
}

// newRange writes requirement rng for version v under rangeStrategy strategy
// (dep.Update.RangeStrategy), after Renovate's hex versioning; ok is false when the
// requirement stays unchanged or does not admit v. pin writes `== v`. A version the
// requirement admits raises the lower bounds of the first alternative admitting it (bump: `~>
// 1.7` → `~> 1.9`, `~> 1.7.2` → `~> 1.7.5`, `>= 1.0.0 and < 2.0.0` → `>= 1.4.0 and < 2.0.0`);
// exact versions, exclusions and upper bounds stay. One outside it is, under widen, admitted by
// moving the upper bounds of the last alternative (`>= 1.0.0 and < 2.0.0` → `>= 1.0.0 and <
// 3.0.0`) or else by appending the replaced last alternative (`~> 1.7` → `~> 1.7 or ~> 2.1`);
// under any other strategy, and for an exact version, the replaced last alternative becomes the
// whole requirement (replaceAlt). Operators, the blanks after them and the words between
// comparators are kept.
func newRange(rng, v, strategy string) (string, bool) {
	p := parse(rng)
	sv, err := msemver.StrictNewVersion(v)
	if p == nil || err != nil {
		return "", false
	}
	t := target{parts: [3]uint64{sv.Major(), sv.Minor(), sv.Patch()}, pre: sv.Prerelease()}
	alts := p.r.Alts
	last := alts[len(alts)-1]
	var out string
	switch {
	case strategy == dep.RangePin:
		out = "== " + v
		if p.exact() && alts[0].Cmps[0].Op == "" {
			out = v
		}
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
			out = rng + " or " + replaceAlt(rng, last, t, v)
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

// exact reports whether the requirement is one exact version (`1.2.3`, `== 1.2.3`).
func (p *requirement) exact() bool {
	return len(p.r.Alts) == 1 && len(p.r.Alts[0].Cmps) == 1 && (p.r.Alts[0].Cmps[0].Op == "" || p.r.Alts[0].Cmps[0].Op == "==")
}

// render writes comparator c with operator op and version ver, keeping the blanks after its
// operator.
func render(rng string, c versioning.HexCmp, op, ver string) string {
	if op == c.Op && ver == c.Ver {
		return rng[c.Start:c.End]
	}
	return op + rng[c.Start+len(c.Op):c.Off] + ver
}

// renderAlt writes alternative a with its comparators replaced by texts, keeping the bytes
// between them.
func renderAlt(rng string, a versioning.HexAlt, texts []string) string {
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

// bumpAlt raises the lower bounds of alternative a, which admits the new version, to it: `~>`
// at its precision, `>=` and `>` (which becomes `>=`) to the whole version.
func bumpAlt(rng string, a versioning.HexAlt, t target) string {
	texts := make([]string, len(a.Cmps))
	for i, c := range a.Cmps {
		texts[i] = rng[c.Start:c.End]
		switch c.Op {
		case "~>":
			texts[i] = render(rng, c, c.Op, t.at(c.Parts()))
		case ">=", ">":
			texts[i] = render(rng, c, ">=", t.at(3))
		}
	}
	return renderAlt(rng, a, texts)
}

// upper moves upper bound c past v: `<` at the bound's precision, its last non-zero part (`<
// 2.0.0` → `< 3.0.0` for 2.3.1, `< 0.15.0` → `< 0.17.0` for 0.16.0), `<=` to v. Bounds admitting
// v stay.
func upper(rng string, c versioning.HexCmp, t target, v string) string {
	if one := parse(rng[c.Start:c.End]); one != nil && one.match(v) {
		return rng[c.Start:c.End]
	}
	if c.Op == "<=" {
		return render(rng, c, c.Op, t.at(3))
	}
	sv, err := msemver.StrictNewVersion(c.Ver)
	if err != nil {
		return rng[c.Start:c.End]
	}
	bound := [3]uint64{sv.Major(), sv.Minor(), sv.Patch()}
	p := 0
	for i, x := range bound {
		if x != 0 {
			p = i
		}
	}
	out := target{}
	copy(out.parts[:p], t.parts[:p])
	out.parts[p] = t.parts[p] + 1
	return render(rng, c, c.Op, out.at(3))
}

// widenAlt moves only the upper bounds (`<`, `<=`) of alternative a past the new version.
func widenAlt(rng string, a versioning.HexAlt, t target, v string) string {
	texts := make([]string, len(a.Cmps))
	for i, c := range a.Cmps {
		texts[i] = rng[c.Start:c.End]
		if c.Op == "<" || c.Op == "<=" {
			texts[i] = upper(rng, c, t, v)
		}
	}
	return renderAlt(rng, a, texts)
}

// replaceAlt writes alternative a for a new version outside it: exact versions take it, `~>`
// takes it at its precision (`~> 1.7` → `~> 2.1`, `~> 1.7.2` → `~> 2.1.0` for 2.1.0), `>=` and
// `>` (which becomes `>=`) the whole version, upper bounds move past it (upper), exclusions
// stay.
func replaceAlt(rng string, a versioning.HexAlt, t target, v string) string {
	texts := make([]string, len(a.Cmps))
	for i, c := range a.Cmps {
		texts[i] = rng[c.Start:c.End]
		switch c.Op {
		case "", "==":
			texts[i] = render(rng, c, c.Op, t.at(3))
		case "~>":
			texts[i] = render(rng, c, c.Op, t.at(c.Parts()))
		case ">=", ">":
			texts[i] = render(rng, c, ">=", t.at(3))
		case "<", "<=":
			texts[i] = upper(rng, c, t, v)
		}
	}
	return renderAlt(rng, a, texts)
}
