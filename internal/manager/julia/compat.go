package julia

import (
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// rewrite returns the compat entry spec rewritten for the version v under strategy (a
// config.RangeStrategies value; "" = the default rewrite), or false when it stays unchanged or
// would not admit v:
//
//   - pin: `=<v>` (`=1.4.5`), the whole entry.
//   - v admitted (bump, in-range-only): the terms entirely below v are dropped and the floor
//     of the term admitting v is raised to v at its precision (`0.21, 1` → `1` for 1.4.5,
//     `1.2` → `1.4`, `1.2 - 2` → `1.4 - 2`).
//   - widen: a term admitting v is appended, in the style of the term with the highest upper
//     bound: its operator and v's caret prefix (`0.21, 1` → `0.21, 1, 2` for 2.3.4; `0.22` for
//     0.22.1), the CompatHelper convention; `~` terms take v at their precision, `=` terms v. An
//     upper bound (`< 2`, the upper half of `1.2 - 1.5`) is raised in place instead.
//   - otherwise (replace, update-lockfile, bump): the whole entry becomes that term with v at
//     its precision (`0.21, 1` → `2`, `4.2` → `5.1`, `~1.2` → `~1.5`, `1.2 - 1.5` → `2.1 - 2.1`,
//     `< 2` → `< 3`); an `=` term takes v (`=6.5.1` → `=6.6.0`), since `=1.2` means 1.2.0 only.
func rewrite(spec, v, strategy string) (string, bool) {
	terms, err := versioning.ParseJuliaCompat(spec)
	if err != nil {
		return "", false
	}
	parts, ok := versioning.JuliaParts(v)
	if !ok {
		return "", false
	}
	var out string
	switch admitted := admits(terms, parts); {
	case strategy == dep.RangePin:
		out = "=" + fit(parts, 3)
	case admitted:
		out = raise(spec, terms, parts)
	case strategy == dep.RangeWiden:
		out = widen(spec, terms, parts)
	default:
		out = replaceTerm(spec, top(terms), parts)
	}
	if out == spec {
		return "", false
	}
	if t, err := versioning.ParseJuliaCompat(out); err != nil || !admits(t, parts) {
		return "", false
	}
	return out, true
}

func admits(terms []versioning.JuliaTerm, v [3]uint64) bool {
	for _, t := range terms {
		if t.Admits(v) {
			return true
		}
	}
	return false
}

// top is the term with the highest upper bound, the last of equals (an unbounded one first).
func top(terms []versioning.JuliaTerm) versioning.JuliaTerm {
	best := terms[0]
	for _, t := range terms[1:] {
		hb, okB := best.Upper()
		ht, okT := t.Upper()
		if !okB {
			continue
		}
		if !okT || versioning.JuliaCompareParts(ht, hb) >= 0 {
			best = t
		}
	}
	return best
}

// sep is the separator between the terms of spec: ", " unless spec writes ",".
func sep(spec string) string {
	if strings.Contains(spec, ",") && !strings.Contains(spec, ", ") {
		return ","
	}
	return ", "
}

// fit writes the first n parts of v (`1.4` for 1.4.5 and n = 2).
func fit(v [3]uint64, n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatUint(v[i], 10))
	}
	return b.String()
}

// caretPrefix is the shortest caret specifier admitting v's compatible versions: the major for
// 1.x (`2`), `0.<minor>` for 0.x (`0.22`), else the version (`0.0.5`).
func caretPrefix(v [3]uint64) string {
	switch {
	case v[0] != 0:
		return fit(v, 1)
	case v[1] != 0:
		return fit(v, 2)
	}
	return fit(v, 3)
}

// above is the exclusive upper bound of precision n just above v (`3` for 2.3.4 and n = 1,
// `1.8` for 1.7.2 and n = 2), the bound a `<` term is raised to.
func above(v [3]uint64, n int) string {
	v[n-1]++
	return fit(v, n)
}

// raise drops the terms of spec entirely below v and raises the floor of the terms admitting
// v to v at their precision.
func raise(spec string, terms []versioning.JuliaTerm, v [3]uint64) string {
	var kept []string
	for _, t := range terms {
		if t.Below(v) {
			continue
		}
		s := spec[t.Start:t.End]
		if t.Admits(v) && t.Floor() {
			s = spec[t.Start:t.Lo.Off] + fit(v, t.Lo.N) + spec[t.Lo.End:t.End]
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, sep(spec))
}

// widen adds v to spec: the upper bound of the top term raised in place for `<` and hyphen
// terms, else a new term in the top term's style appended.
func widen(spec string, terms []versioning.JuliaTerm, v [3]uint64) string {
	t := top(terms)
	switch t.Op {
	case "<":
		return spec[:t.Lo.Off] + above(v, t.Lo.N) + spec[t.Lo.End:]
	case "-":
		return spec[:t.Hi.Off] + fit(v, t.Hi.N) + spec[t.Hi.End:]
	case "~":
		return spec + sep(spec) + t.Op + fit(v, t.Lo.N)
	case "=":
		return spec + sep(spec) + t.Op + fit(v, 3)
	}
	return spec + sep(spec) + t.Op + caretPrefix(v) // "" or "^"
}

// replaceTerm is the term t of spec rewritten for v at its precision, alone.
func replaceTerm(spec string, t versioning.JuliaTerm, v [3]uint64) string {
	switch t.Op {
	case "<":
		return spec[t.Start:t.Lo.Off] + above(v, t.Lo.N)
	case "-":
		return spec[t.Start:t.Lo.Off] + fit(v, t.Lo.N) + spec[t.Lo.End:t.Hi.Off] + fit(v, t.Hi.N)
	case "=": // =1.2 is 1.2.0 only
		return spec[t.Start:t.Lo.Off] + fit(v, 3)
	}
	return spec[t.Start:t.Lo.Off] + fit(v, t.Lo.N)
}
