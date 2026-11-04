package gradle

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// setVersion records the version v found at span. A dynamic version (`1.+`, `[1.0,2.0)`,
// `latest.release`) becomes a range dep skipped as `dynamic version <v>` until ReadLocks
// finds its locked version; its RefSpan is the part Edits rewrites: the numeric prefix of
// `1.2.+`, the upper bound of a range (empty when unbounded), the whole `latest.*` selector
// (which admits every version, so its updates only move the lockfile).
func setVersion(d *dep.Dep, v string, span dep.Span) {
	d.Version, d.RefSpan = v, span
	if !dynamic(v) {
		return
	}
	d.Skip = "dynamic version " + v
	rng, sub, ok := translate(v)
	if !ok {
		return
	}
	if _, err := semver.NewConstraint(rng); err != nil {
		return
	}
	d.Range = rng
	d.RefSpan = dep.Span{Start: span.Start + sub.Start, End: span.Start + sub.End}
}

// dynamic reports whether v is a Gradle dynamic version: a `+` prefix, a Maven-style range
// or `latest.<status>`.
func dynamic(v string) bool {
	return strings.HasSuffix(v, "+") || strings.HasPrefix(v, "latest.") ||
		v != "" && strings.ContainsAny(v[:1], "[(]") && strings.Contains(v, ",")
}

// translate returns the Masterminds constraint equivalent to the dynamic version v and the
// span within v that Edits rewrites. Prefix versions become `>=1.2.0-0 <1.3.0-0` (space
// separated, prereleases included as Gradle's prefix match does); ranges become `>=1.0, <2.0`
// (comma separated, bounds verbatim); `latest.release` admits every release (`>=0.0.0`),
// `latest.integration` also prereleases (`>=0.0.0-0`). Other statuses (`latest.milestone`)
// depend on repository metadata and are not translated.
func translate(v string) (string, dep.Span, bool) {
	switch v {
	case "latest.release":
		return ">=0.0.0", dep.Span{End: len(v)}, true
	case "latest.integration":
		return ">=0.0.0-0", dep.Span{End: len(v)}, true
	}
	if p, ok := strings.CutSuffix(v, "+"); ok {
		if p == "" {
			return ">=0.0.0-0", dep.Span{}, true
		}
		p, ok = strings.CutSuffix(p, ".")
		comps := strings.Split(p, ".")
		if !ok || len(comps) > 3 {
			return "", dep.Span{}, false
		}
		lo := make([]int, 3)
		for i, c := range comps {
			n, err := strconv.Atoi(c)
			if err != nil || n < 0 || strconv.Itoa(n) != c {
				return "", dep.Span{}, false
			}
			lo[i] = n
		}
		hi := make([]int, 3)
		copy(hi, lo[:len(comps)])
		hi[len(comps)-1]++
		return fmt.Sprintf(">=%s-0 <%s-0", joinInts(lo), joinInts(hi)), dep.Span{End: len(p)}, true
	}
	if len(v) < 3 {
		return "", dep.Span{}, false
	}
	open, closer := v[0], v[len(v)-1]
	lo, hi, ok := strings.Cut(v[1:len(v)-1], ",")
	if !ok || strings.Contains(hi, ",") || !strings.ContainsRune("[(]", rune(open)) || !strings.ContainsRune("])[", rune(closer)) {
		return "", dep.Span{}, false
	}
	loT, hiT := strings.TrimSpace(lo), strings.TrimSpace(hi)
	var parts []string
	if loT != "" {
		op := ">="
		if open != '[' {
			op = ">"
		}
		parts = append(parts, op+loT)
	}
	// The upper bound token: offset after the comma plus its leading blanks.
	hiStart := 1 + len(lo) + 1 + (len(hi) - len(strings.TrimLeft(hi, " \t")))
	sub := dep.Span{Start: hiStart, End: hiStart + len(hiT)}
	if hiT != "" {
		op := "<"
		if closer == ']' {
			op = "<="
		}
		parts = append(parts, op+hiT)
	}
	if len(parts) == 0 {
		return "", dep.Span{}, false
	}
	return strings.Join(parts, ", "), sub, true
}

func joinInts(n []int) string {
	s := make([]string, len(n))
	for i, x := range n {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ".")
}

// rangeEdit rewrites the RefSpan of a range dep for a bump to nv: a prefix keeps its
// precision (`1.+` → `2.+`), an inclusive upper bound becomes nv, an exclusive one the next
// value of nv at the position where the bounds differ (`[1.0,2.0)` → `[1.0,3.0)` for 2.5.1).
// A range admitting nv, or without an upper bound, is kept. A `latest.*` selector (no upper
// bound, non-empty RefSpan) is never rewritten: its updates only move the lockfile.
func rangeEdit(d dep.Dep, nv string) (patch.Edit, error) {
	e := patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End}
	if !strings.Contains(d.Range, "<") && d.RefSpan.End > d.RefSpan.Start {
		return e, fmt.Errorf("%s: a latest.* selector admits every version; only its lockfile is updated", d.Name)
	}
	if lo, hi, ok := strings.Cut(d.Range, " "); ok && !strings.Contains(d.Range, ",") { // prefix form
		l := strings.Split(strings.TrimSuffix(strings.TrimPrefix(lo, ">="), "-0"), ".")
		h := strings.Split(strings.TrimSuffix(strings.TrimPrefix(hi, "<"), "-0"), ".")
		n := 1
		for n < len(l) && n < len(h) && l[n-1] == h[n-1] {
			n++
		}
		e.Old = strings.Join(l[:n], ".")
		comps := strings.Split(nv, ".")
		if len(comps) < n {
			return e, fmt.Errorf("%s has fewer than %d components", nv, n)
		}
		for _, c := range comps[:n] {
			if _, err := strconv.Atoi(c); err != nil {
				return e, fmt.Errorf("cannot write %s as a %d-component prefix", nv, n)
			}
		}
		e.New = strings.Join(comps[:n], ".")
		return e, nil
	}
	var lo, hi string
	inclusive := false
	for p := range strings.SplitSeq(d.Range, ", ") {
		switch {
		case strings.HasPrefix(p, "<="):
			hi, inclusive = p[2:], true
		case strings.HasPrefix(p, "<"):
			hi = p[1:]
		case strings.HasPrefix(p, ">="):
			lo = p[2:]
		case strings.HasPrefix(p, ">"):
			lo = p[1:]
		}
	}
	e.Old, e.New = hi, hi
	if hi == "" {
		return e, nil
	}
	if c, err := semver.NewConstraint(d.Range); err == nil {
		if v, err := semver.NewVersion(nv); err == nil && c.Check(v) {
			return e, nil
		}
	}
	if inclusive {
		e.New = nv
		return e, nil
	}
	l, h := strings.Split(lo, "."), strings.Split(hi, ".")
	k := 0
	for k < len(h)-1 && k < len(l) && l[k] == h[k] {
		k++
	}
	if lo == "" {
		k = 0
	}
	comps := strings.Split(nv, ".")
	if len(comps) <= k {
		return e, fmt.Errorf("%s has fewer than %d components", nv, k+1)
	}
	out := make([]string, len(h))
	for i := range out {
		out[i] = "0"
	}
	for i := range k + 1 {
		digits := comps[i]
		if j := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); j >= 0 {
			if i < k || j == 0 {
				return e, fmt.Errorf("cannot derive an upper bound from %s", nv)
			}
			digits = digits[:j]
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return e, fmt.Errorf("cannot derive an upper bound from %s", nv)
		}
		if i == k {
			n++
		}
		out[i] = strconv.Itoa(n)
	}
	e.New = strings.Join(out, ".")
	return e, nil
}
