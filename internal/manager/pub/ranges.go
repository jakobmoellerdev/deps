package pub

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// newRange returns the constraint rng rewritten for version v under strategy (a
// config.RangeStrategies value other than auto; "" = replace, Renovate's auto for pub), or
// false when that leaves it unchanged or does not admit v:
//   - pin writes v.
//   - A version rng admits raises its lower bound (`^1.2.3` → `^1.4.0`, `>=1.2.3 <2.0.0` →
//     `>=1.4.0 <2.0.0`, `>1.2.3` → `>=1.4.0`); a constraint without one (`<2.0.0`) stays.
//   - An exact version is replaced by v under every strategy.
//   - widen keeps the lower bound and moves the upper ones past v: `^1.2.3` → `>=1.2.3 <3.0.0`
//     for 2.1.0, `>=1.0.0 <2.0.0` → `>=1.0.0 <3.0.0`, `<=2.0.0` → `<=2.1.0`.
//   - Any other strategy replaces: `^1.2.3` → `^2.1.0`, `>=1.0.0 <2.0.0` → `>=2.1.0 <3.0.0`.
//
// An exclusive upper bound moved past v becomes the first version `^v` excludes (the next
// major, or the next minor of a 0.x version); an inclusive one becomes v.
func newRange(rng, v, strategy string) (string, bool) {
	c, err := versioning.ParsePubConstraint(rng)
	if err != nil || c.Any {
		return "", false
	}
	floor, hasFloor := c.Floor()
	if hasFloor && versioning.ComparePub(v, floor.V) < 0 {
		return "", false // not an upgrade
	}
	var out string
	switch {
	case strategy == dep.RangePin:
		out = v
	case len(c.Terms) == 1 && c.Terms[0].Op == "":
		out = v
	case c.Admits(v):
		if !hasFloor {
			return "", false
		}
		out = setTerm(rng, floor, raisedOp(floor.Op), v)
	case len(c.Terms) == 1 && c.Terms[0].Op == "^":
		if strategy != dep.RangeWiden {
			out = setTerm(rng, c.Terms[0], "^", v)
			break
		}
		hi, _ := versioning.PubNextBreaking(v)
		out = ">=" + c.Terms[0].V + " <" + hi
	default:
		out = rng
		// Rewrite from the last term so the spans of earlier ones stay valid.
		for _, t := range slices.Backward(c.Terms) {
			switch t.Op {
			case "<":
				hi, _ := versioning.PubNextBreaking(v)
				out = setTerm(out, t, "<", hi)
			case "<=":
				out = setTerm(out, t, "<=", v)
			default:
				if strategy != dep.RangeWiden && hasFloor && t == floor {
					out = setTerm(out, t, raisedOp(t.Op), v)
				}
			}
		}
	}
	if out == rng {
		return "", false
	}
	if nc, err := versioning.ParsePubConstraint(out); err != nil || !nc.Admits(v) {
		return "", false
	}
	return out, true
}

// raisedOp is the operator of a raised lower bound: `>` becomes `>=` (it must admit v).
func raisedOp(op string) string {
	if op == ">" {
		return ">="
	}
	return op
}

// setTerm returns rng with term t written as op followed by v; blanks between the operator and
// the version are kept.
func setTerm(rng string, t versioning.PubTerm, op, v string) string {
	start := t.Start
	for start > 0 && (rng[start-1] == ' ' || rng[start-1] == '\t') && t.Op != "" {
		start--
	}
	blanks := rng[start:t.Start]
	start -= len(t.Op)
	return rng[:start] + op + blanks + v + rng[t.End:]
}
