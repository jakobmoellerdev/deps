package versioning

import (
	"fmt"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// hcVersion is a version as written in a Terraform version constraint (HashiCorp
// go-version): 1–3 numeric parts, an optional "v" prefix and, on a full version, an optional
// "-prerelease". The parts live in an array, so parsing allocates nothing.
type hcVersion struct {
	parts [3]int
	n     int    // number of parts
	v     bool   // "v" prefix
	pre   string // prerelease without "-"; "" = release
}

func parseHashicorp(s string) (hcVersion, bool) {
	var h hcVersion
	if s != "" && s[0] == 'v' {
		h.v, s = true, s[1:]
	}
	for {
		i := 0
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		if i == 0 || h.n == len(h.parts) {
			return hcVersion{}, false
		}
		p, err := strconv.Atoi(s[:i])
		if err != nil {
			return hcVersion{}, false // overflow
		}
		h.parts[h.n], h.n, s = p, h.n+1, s[i:]
		if s == "" {
			return h, true
		}
		switch {
		case s[0] == '.':
			s = s[1:]
		case s[0] == '-' && h.n == 3 && validSuffix(s[1:]) && !strings.Contains(s, "+"):
			h.pre = s[1:]
			return h, true
		default:
			return hcVersion{}, false
		}
	}
}

// hashicorpCompat rejects prereleases unless cur is one.
func hashicorpCompat(cur, next hcVersion) string {
	if next.pre != "" && cur.pre == "" {
		return "prerelease"
	}
	return ""
}

// compareHashicorp orders the parts numerically (a missing part counts as 0), then a release
// above its prereleases, then fewer parts below more so the order is total.
func compareHashicorp(a, b hcVersion) int {
	for i := range a.parts {
		if a.parts[i] != b.parts[i] {
			if a.parts[i] < b.parts[i] {
				return -1
			}
			return 1
		}
	}
	if c := comparePrerelease(a.pre, b.pre); c != 0 {
		return c
	}
	return a.n - b.n
}

func hashicorpKind(cur, next hcVersion) dep.UpdateType {
	switch {
	case cur.parts[0] != next.parts[0]:
		return dep.Major
	case cur.parts[1] != next.parts[1]:
		return dep.Minor
	}
	return dep.Patch
}

func (h hcVersion) semver() *msemver.Version {
	return msemver.New(uint64(h.parts[0]), uint64(h.parts[1]), uint64(h.parts[2]), h.pre, "")
}

func (h hcVersion) major() string { return strconv.Itoa(h.parts[0]) }

// render writes h at cur's precision and "v" style: a candidate with more parts than cur is
// cut to cur's (5.31.2 → 5.31 for `~> 5.0`).
func (h hcVersion) render(cur hcVersion) string {
	var b strings.Builder
	if cur.v {
		b.WriteByte('v')
	}
	n := min(h.n, cur.n)
	for i := range n {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(h.parts[i]))
	}
	if h.pre != "" {
		b.WriteByte('-')
		b.WriteString(h.pre)
	}
	return b.String()
}

// hashicorpScheme keeps the precision and "v" style of the current value, as Terraform
// constraints (`~> 5.0`, `= 1.9.5`) need: candidates are rewritten to cur's form before they
// are ranked, so `5.0` with candidates `5.31.0` and `v5.31.2` yields `5.31`. Equal rewritten
// candidates are ranked once.
type hashicorpScheme struct{ impl[hcVersion] }

// fit rewrites cands to cur's form, dropping duplicates. Invalid candidates, and prereleases
// when cur is partial (cut, they would read as a release), are kept as they are, so they are
// rejected as invalid or prerelease.
func (s hashicorpScheme) fit(cur string, cands []string) []string {
	c, ok := parseHashicorp(cur)
	if !ok {
		return cands
	}
	out := make([]string, 0, len(cands))
	seen := make(map[string]bool, len(cands))
	for _, raw := range cands {
		v := raw
		if n, ok := parseHashicorp(raw); ok && (n.pre == "" || c.n == 3) {
			v = n.render(c)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (s hashicorpScheme) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	return s.impl.Best(cur, s.fit(cur, cands), accept, reject)
}

func (s hashicorpScheme) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) Ranking {
	return s.rank(cur, s.fit(cur, cands), accept, reject)
}

func (s hashicorpScheme) Compatible(cur, next string) bool {
	return s.impl.Compatible(cur, s.fit(cur, []string{next})[0])
}

// HashicorpClause is one `<op> <version>` of a Terraform version constraint; Off is the byte
// offset of Version in the constraint.
type HashicorpClause struct {
	Op, Version string
	Off         int
}

// ParseHashicorpConstraint splits a Terraform version constraint (`~> 5.0`, `>= 1.2, < 2.0`,
// `1.2.3`) into its comma-separated clauses. Operators are `=`, `!=`, `>`, `>=`, `<`, `<=`
// and `~>`; none means `=`.
func ParseHashicorpConstraint(c string) ([]HashicorpClause, error) {
	var out []HashicorpClause
	base := 0
	for part := range strings.SplitSeq(c, ",") {
		i := skipBlanks(part, 0)
		op := ""
		for _, o := range []string{"~>", ">=", "<=", "!=", "=", ">", "<"} {
			if strings.HasPrefix(part[i:], o) {
				op = o
				break
			}
		}
		i = skipBlanks(part, i+len(op))
		v := strings.TrimRight(part[i:], " \t")
		if _, ok := parseHashicorp(v); !ok {
			return nil, fmt.Errorf("invalid version constraint %q", c)
		}
		out = append(out, HashicorpClause{Op: op, Version: v, Off: base + i})
		base += len(part) + 1
	}
	return out, nil
}

func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// RangeMatcher is implemented by schemes with a range grammar of their own; ranges of other
// schemes are Masterminds semver constraints.
type RangeMatcher interface {
	// MatchRange returns whether a version of the scheme satisfies rng.
	MatchRange(rng string) (func(v string) bool, error)
}

// MatchRange implements RangeMatcher with Terraform's constraint semantics (go-version): all
// clauses must hold; `~> 1.2` is `>= 1.2, < 2.0` and `~> 1.2.3` is `>= 1.2.3, < 1.3.0`; a
// prerelease only satisfies an exact clause naming it.
func (hashicorpScheme) MatchRange(rng string) (func(v string) bool, error) {
	clauses, err := ParseHashicorpConstraint(rng)
	if err != nil {
		return nil, err
	}
	parsed := make([]hcVersion, len(clauses))
	for i, c := range clauses {
		parsed[i], _ = parseHashicorp(c.Version)
	}
	return func(s string) bool {
		v, ok := parseHashicorp(s)
		if !ok {
			return false
		}
		for i, c := range clauses {
			if !admits(c.Op, parsed[i], v) {
				return false
			}
		}
		return true
	}, nil
}

// admits reports whether v satisfies `op c`.
func admits(op string, c, v hcVersion) bool {
	cmp := compareValue(v, c)
	if v.pre != "" && (op != "" && op != "=" || cmp != 0) {
		return false
	}
	switch op {
	case "", "=":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	}
	// ~>: at least c, and the parts before c's last one are equal.
	if cmp < 0 {
		return false
	}
	for i := range c.n - 1 {
		if v.parts[i] != c.parts[i] {
			return false
		}
	}
	return true
}

// compareValue orders by the padded parts and the prerelease, ignoring the precision.
func compareValue(a, b hcVersion) int {
	a.n, b.n = 0, 0
	return compareHashicorp(a, b)
}

// FitHashicorp writes next at the precision and "v" style of cur (both hashicorp versions);
// next is returned unchanged when either is invalid.
func FitHashicorp(cur, next string) string {
	c, okC := parseHashicorp(cur)
	n, okN := parseHashicorp(next)
	if !okC || !okN || n.pre != "" && c.n < 3 {
		return next
	}
	return n.render(c)
}
