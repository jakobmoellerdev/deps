package paket

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// nugetScheme orders and validates NuGet versions, which Paket versions are.
var nugetScheme, _ = versioning.Get(Versioning)

// term is one comparison of a version constraint: `~> 1.2`, `>= 1.2.3`, `< 2`, `= 1.0`, `1.0`.
// op is "" for a bare version; start and end are the offsets of the version in the constraint
// and opStart the offset of the operator (the term's start, after a resolver strategy prefix).
type term struct {
	op                  string
	version             string
	opStart, start, end int
}

// constraint is a parsed `paket.dependencies` version constraint: its comparisons, then the
// prerelease channels it allows (`>= 2 beta rc`, `>= 3 prerelease`).
type constraint struct {
	terms    []term
	channels []string
}

// paketOps are the comparison operators, longest first.
var paketOps = []string{">=", "<=", "==", "~>", ">", "<", "="}

// errConstraint is the error of a constraint outside Paket's grammar.
var errConstraint = errors.New("unsupported version constraint")

// isConstraintToken reports whether the whitespace-separated token tok starts a comparison: an
// operator (after an optional `!`/`@` resolver strategy) or a digit (Renovate's test).
func isConstraintToken(tok string) bool {
	tok = strings.TrimLeft(tok, "!@")
	if tok == "" {
		return false
	}
	for _, op := range paketOps {
		if strings.HasPrefix(tok, op) {
			return true
		}
	}
	return tok[0] >= '0' && tok[0] <= '9'
}

// parseConstraint parses s, a constraint as written: comparisons (an operator and a version,
// together or apart, after an optional `!`/`@` resolver strategy) followed by prerelease
// channel names.
func parseConstraint(s string) (constraint, error) {
	var c constraint
	toks := fields(s)
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		text := s[tok.start:tok.end]
		if !isConstraintToken(text) {
			if len(c.terms) == 0 || strings.ContainsAny(text, ":=<>~") {
				return constraint{}, errConstraint
			}
			c.channels = append(c.channels, strings.ToLower(text))
			continue
		}
		if len(c.channels) > 0 {
			return constraint{}, errConstraint // a comparison after the channels
		}
		t := term{opStart: tok.start + len(text) - len(strings.TrimLeft(text, "!@"))}
		rest := text[t.opStart-tok.start:]
		for _, op := range paketOps {
			if strings.HasPrefix(rest, op) {
				t.op = op
				break
			}
		}
		t.start = t.opStart + len(t.op)
		if t.start == tok.end { // `>= 1.2`: the version is the next token
			if i+1 == len(toks) {
				return constraint{}, errConstraint
			}
			i++
			t.start = toks[i].start
			tok = toks[i]
		}
		t.end = tok.end
		t.version = s[t.start:t.end]
		if !nugetScheme.Valid(t.version) {
			return constraint{}, errConstraint
		}
		c.terms = append(c.terms, t)
	}
	if len(c.terms) == 0 {
		return constraint{}, errConstraint
	}
	return c, nil
}

// span is a token's offsets.
type span struct{ start, end int }

// fields returns the offsets of the blank-separated tokens of s.
func fields(s string) []span {
	var out []span
	start := -1
	for i := 0; i <= len(s); i++ {
		blank := i == len(s) || s[i] == ' ' || s[i] == '\t'
		switch {
		case blank && start >= 0:
			out = append(out, span{start, i})
			start = -1
		case !blank && start < 0:
			start = i
		}
	}
	return out
}

// pinned reports whether c pins one version: `1.2.3`, `= 1.2.3` or `== 1.2.3`.
func (c constraint) pinned() bool {
	return len(c.terms) == 1 && (c.terms[0].op == "" || c.terms[0].op == "=" || c.terms[0].op == "==")
}

// floor returns the version of c's lower bound (`>=`, `>`, `~>`; the highest), or "".
func (c constraint) floor() string {
	var out string
	for _, t := range c.terms {
		if (t.op == ">=" || t.op == ">" || t.op == "~>") && (out == "" || nugetScheme.Compare(t.version, out) > 0) {
			out = t.version
		}
	}
	return out
}

// admits reports whether v satisfies every comparison of c and, when v is a prerelease, its
// channel is allowed: the `prerelease` keyword allows every channel, a channel name those whose
// label contains it, and a comparison on a prerelease version the prereleases of that version's
// release.
func (c constraint) admits(v string) bool {
	if !nugetScheme.Valid(v) {
		return false
	}
	for _, t := range c.terms {
		cmp := nugetScheme.Compare(v, t.version)
		ok := false
		switch t.op {
		case "", "=", "==":
			ok = cmp == 0
		case ">=":
			ok = cmp >= 0
		case ">":
			ok = cmp > 0
		case "<=":
			ok = cmp <= 0
		case "<":
			ok = cmp < 0
		case "~>":
			ok = cmp >= 0 && nugetScheme.Compare(v, twiddleUpper(t.version)) < 0
		}
		if !ok {
			return false
		}
	}
	parts, pre, _ := versioning.NuGetParts(v)
	if pre == "" {
		return true
	}
	label := strings.ToLower(pre)
	for _, ch := range c.channels {
		if ch == "prerelease" || strings.Contains(label, ch) {
			return true
		}
	}
	for _, t := range c.terms {
		tp, tpre, _ := versioning.NuGetParts(t.version)
		if tpre != "" && joinParts(tp, 3) == joinParts(parts, 3) {
			return true
		}
	}
	return false
}

// twiddleUpper is the exclusive upper bound of `~> v`: v without its last part, the new last
// part incremented (`1.2.3` → 1.3, `1.2` → 2, `1.2.3.4` → 1.2.4); `~> 1` is below 2.
func twiddleUpper(v string) string {
	parts, _, _ := versioning.NuGetParts(v)
	if len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	up := append([]uint64(nil), parts...)
	up[len(up)-1]++
	return joinParts(up, 0)
}

// joinParts renders parts, padded with zeros to at least n.
func joinParts(parts []uint64, n int) string {
	var b strings.Builder
	for i := range max(len(parts), n) {
		if i > 0 {
			b.WriteByte('.')
		}
		var p uint64
		if i < len(parts) {
			p = parts[i]
		}
		b.WriteString(strconv.FormatUint(p, 10))
	}
	return b.String()
}

// atPrecision returns v's numeric parts cut or padded to as many as ref has.
func atPrecision(v, ref string) string {
	parts, _, _ := versioning.NuGetParts(v)
	refParts, _, _ := versioning.NuGetParts(ref)
	return joinParts(parts[:min(len(parts), len(refParts))], len(refParts))
}

// constraints caches parseConstraint by constraint string (nil when it does not parse): the
// engine checks and rewrites a dep's range for every candidate release.
var constraints sync.Map // string -> *constraint

func cachedConstraint(s string) *constraint {
	if p, ok := constraints.Load(s); ok {
		return p.(*constraint)
	}
	var p *constraint
	if c, err := parseConstraint(s); err == nil {
		p = &c
	}
	got, _ := constraints.LoadOrStore(s, p)
	return got.(*constraint)
}

// InRange implements manager.RangeUpdater: whether d's constraint admits v.
func (*paketManager) InRange(d dep.Dep, v string) bool {
	c := cachedConstraint(d.Range)
	return c != nil && c.admits(v)
}

// NewRange implements manager.RangeUpdater for Paket constraints, keeping their shape (the
// operators, resolver strategy prefixes and channels). pin writes v as the bare version, which
// Paket reads as an exact pin. A version the constraint admits only moves its floor, which the
// engine writes under bump: a `>=` floor becomes v and a `~>` comparison v at its precision
// (`~> 1.2` → `~> 1.5` for 1.5.0, same upper bound). A newer version it does not admit moves
// each bound past it: `~> 1.2` → `~> 2.1` for 2.1.0 (under widen `>= 1.2 < 3.0`), `< 2.0` →
// `< 2.2` for 2.1.4 (v at the bound's precision, last part incremented), `<= 2.0` → `<= 2.1.4`;
// a `>=`/`>` floor below a moved `~>` is dropped (`~> 1.2 >= 1.2.3` → `~> 2.1`). Below the
// floor, or for a prerelease whose channel stays excluded, nothing is written.
func (*paketManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	c := cachedConstraint(d.Range)
	if c == nil || d.RangeFixed || !nugetScheme.Valid(v) {
		return "", false
	}
	if strategy == dep.RangePin {
		if c.pinned() && c.terms[0].version == v {
			return "", false
		}
		return v, true
	}
	type repl struct {
		start, end int
		text       string
	}
	var rs []repl
	admitted := c.admits(v)
	moved := false // a ~> comparison moved past its old upper bound
	for _, t := range c.terms {
		switch {
		case admitted && t.op == ">=":
			rs = append(rs, repl{t.start, t.end, v})
		case admitted && t.op == "~>":
			rs = append(rs, repl{t.start, t.end, atPrecision(v, t.version)})
		case admitted:
		case t.op == "~>" && nugetScheme.Compare(v, twiddleUpper(t.version)) >= 0:
			moved = true
			if strategy == dep.RangeWiden {
				rs = append(rs, repl{t.opStart, t.end, ">= " + t.version + " < " + twiddleUpper(atPrecision(v, t.version))})
			} else {
				rs = append(rs, repl{t.start, t.end, atPrecision(v, t.version)})
			}
		case t.op == "<" && nugetScheme.Compare(v, t.version) >= 0:
			parts, _, _ := versioning.NuGetParts(v)
			bound, _, _ := versioning.NuGetParts(t.version)
			up := make([]uint64, len(bound))
			copy(up, parts)
			up[len(up)-1]++
			rs = append(rs, repl{t.start, t.end, joinParts(up, 0)})
		case t.op == "<=" && nugetScheme.Compare(v, t.version) > 0:
			rs = append(rs, repl{t.start, t.end, v})
		}
	}
	if moved && strategy != dep.RangeWiden {
		for _, t := range c.terms {
			if t.op == ">=" || t.op == ">" { // a floor below the moved ~>: redundant
				start := t.opStart
				for start > 0 && (d.Range[start-1] == '!' || d.Range[start-1] == '@') {
					start--
				}
				for start > 0 && (d.Range[start-1] == ' ' || d.Range[start-1] == '\t') {
					start--
				}
				rs = append(rs, repl{start, t.end, ""})
			}
		}
	}
	if len(rs) == 0 {
		return "", false
	}
	var b strings.Builder
	last := 0
	slices.SortFunc(rs, func(a, b repl) int { return a.start - b.start })
	for _, r := range rs {
		b.WriteString(d.Range[last:r.start])
		b.WriteString(r.text)
		last = r.end
	}
	b.WriteString(d.Range[last:])
	out := strings.TrimSpace(b.String())
	if out == d.Range {
		return "", false
	}
	if nc, err := parseConstraint(out); err != nil || !nc.admits(v) {
		return "", false
	}
	return out, true
}
