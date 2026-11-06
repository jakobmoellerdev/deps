package versioning

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// juliaScheme is the versioning of Julia packages (VersionNumber, as the General registry
// lists them) with Pkg's compat ranges (MatchRange). Versions are cargo's syntax (1–3 numeric
// parts, `-pre` and `+build` after three), so cargoVersion carries them; builds are ordered as
// Julia orders them (`1.2.11+1` > `1.2.11+0` > `1.2.11`, the JLL rebuilds).
type juliaScheme struct{ impl[cargoVersion] }

// juliaCompat accepts candidates of any precision (registry versions have three parts, a
// compat entry's anchor often one or two) and prereleases only from a prerelease.
func juliaCompat(cur, next cargoVersion) string {
	if next.pre() != "" && cur.pre() == "" {
		return "prerelease"
	}
	return ""
}

// compareJulia is compareCargo with build metadata compared as Julia's isless does: no build
// sorts below any build, identifiers compare numerically when both are numbers, else
// bytewise, a number below a string, and a prefix below the longer build.
func compareJulia(a, b cargoVersion) int {
	if c := compareCargo(a, b); c != 0 {
		return c
	}
	ab, bb := a.build(), b.build()
	switch {
	case ab == bb:
		return 0
	case ab == "":
		return -1
	case bb == "":
		return 1
	}
	as, bs := strings.Split(ab, "."), strings.Split(bb, ".")
	for i := range min(len(as), len(bs)) {
		x, errX := strconv.ParseUint(as[i], 10, 64)
		y, errY := strconv.ParseUint(bs[i], 10, 64)
		var c int
		switch {
		case errX == nil && errY == nil:
			c = cmpUint(x, y)
		case errX == nil:
			c = -1
		case errY == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}

func cmpUint(x, y uint64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// build is the build metadata without "+"; "" = none.
func (c cargoVersion) build() string {
	if c.v == nil {
		return ""
	}
	return c.v.Metadata()
}

// juliaKind is cargoKind: Pkg's caret semantics make the leftmost non-zero part the breaking
// one (0.21 → 0.22 is major); a build-only change (a JLL rebuild) is a patch.
func juliaKind(cur, next cargoVersion) dep.UpdateType { return cargoKind(cur, next) }

// JuliaBound is a version of a Julia compat entry as written: 1–3 numeric parts (N), the rest
// padded with 0. Off and End delimit its digits in the entry (after any `v`).
type JuliaBound struct {
	Parts    [3]uint64
	N        int
	Off, End int
}

// String writes the bound's N parts (`0.21`).
func (b JuliaBound) String() string {
	var s strings.Builder
	for i := range b.N {
		if i > 0 {
			s.WriteByte('.')
		}
		s.WriteString(strconv.FormatUint(b.Parts[i], 10))
	}
	return s.String()
}

// JuliaTerm is one comma-separated specifier of a Julia compat entry (Pkg's semver_spec).
// Op is "" or "^" (caret), "~" (tilde), "=", ">=", "≥", "<", or "-" for a hyphen range, whose
// upper bound is Hi. Start and End delimit the term in the entry, without blanks around it.
type JuliaTerm struct {
	Op         string
	Lo, Hi     JuliaBound
	Start, End int
}

// Floor reports whether the term has a lower bound to raise (every op but "<").
func (t JuliaTerm) Floor() bool { return t.Op != "<" }

// interval is the term's versions [lo, hi) on the numeric parts; inf = no upper bound.
func (t JuliaTerm) interval() (lo, hi [3]uint64, inf bool) {
	lo = t.Lo.Parts
	switch t.Op {
	case "", "^":
		switch {
		case t.Lo.Parts[0] != 0:
			hi = upperOf(t.Lo.Parts, 1)
		case t.Lo.Parts[1] != 0:
			hi = upperOf(t.Lo.Parts, 2)
		default:
			hi = upperOf(t.Lo.Parts, min(t.Lo.N, 3)) // 0 → <1, 0.0 → <0.1, 0.0.3 → =0.0.3
		}
	case "~":
		hi = upperOf(t.Lo.Parts, min(max(t.Lo.N, 1), 2)) // ~1 → <2, ~1.2 and ~1.2.3 → <1.3
	case "=":
		hi = upperOf(t.Lo.Parts, 3) // the version padded with 0: =1.2 is 1.2.0 only
	case ">=", "≥":
		return lo, hi, true
	case "<":
		return [3]uint64{}, t.Lo.Parts, false
	case "-":
		hi = upperOf(t.Hi.Parts, t.Hi.N)
	}
	return lo, hi, false
}

// upperOf is the exclusive upper bound of the versions whose first n parts equal p's
// (Pkg's inclusive VersionBound of n parts): p with part n-1 incremented and the rest 0.
func upperOf(p [3]uint64, n int) [3]uint64 {
	var u [3]uint64
	copy(u[:n], p[:n])
	u[n-1]++
	return u
}

// Admits reports whether the term admits the release parts v (prereleases and builds count
// as their release, as Pkg compares them).
func (t JuliaTerm) Admits(v [3]uint64) bool {
	lo, hi, inf := t.interval()
	return JuliaCompareParts(v, lo) >= 0 && (inf || JuliaCompareParts(v, hi) < 0)
}

// Below reports whether every version the term admits is below v.
func (t JuliaTerm) Below(v [3]uint64) bool {
	_, hi, inf := t.interval()
	return !inf && JuliaCompareParts(hi, v) <= 0
}

// Upper is the term's exclusive upper bound; ok is false for an unbounded term (`>= 1`).
func (t JuliaTerm) Upper() (hi [3]uint64, ok bool) {
	_, hi, inf := t.interval()
	return hi, !inf
}

// JuliaCompareParts orders two release-part triples (major, minor, patch): -1, 0 or +1.
func JuliaCompareParts(a, b [3]uint64) int {
	return slices.Compare(a[:], b[:])
}

// JuliaParts returns the release parts of a Julia version (`1.2.3+0` → 1, 2, 3); ok is false
// for an invalid one.
func JuliaParts(v string) (parts [3]uint64, ok bool) {
	c, ok := parseCargo(v)
	return c.parts, ok
}

// ParseJuliaCompat parses a Julia compat entry (`"0.21, 1"`, `"~1.0, ^1.1.1"`, `">= 1.2"`,
// `"1.2 - 2"`, `"=0.3.1"`) as Pkg's semver_spec does: comma-separated terms, each a caret
// (none or `^`) or tilde (`~`) specifier, an inequality (`>=`, `≥`, `<`, `=` with optional
// blanks before the version) or a hyphen range `a - b`; versions have 1–3 numeric parts and
// an optional `v`. `0.0.0` is invalid, as are `>`, `<=` and an empty entry.
func ParseJuliaCompat(s string) ([]JuliaTerm, error) {
	var terms []JuliaTerm
	off := 0
	for part := range strings.SplitSeq(s, ",") {
		start, end := off, off+len(part)
		off = end + 1
		for start < end && juliaBlank(s[start]) {
			start++
		}
		for end > start && juliaBlank(s[end-1]) {
			end--
		}
		t, err := parseJuliaTerm(s, start, end)
		if err != nil {
			return nil, fmt.Errorf("invalid compat entry %q: %w", s, err)
		}
		terms = append(terms, t)
	}
	return terms, nil
}

func juliaBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// parseJuliaTerm parses s[start:end], a trimmed term.
func parseJuliaTerm(s string, start, end int) (JuliaTerm, error) {
	t := JuliaTerm{Start: start, End: end}
	term := s[start:end]
	if term == "" {
		return t, errors.New("empty specifier")
	}
	// Hyphen range: `a - b` with blanks on both sides of the "-".
	if i := strings.Index(term, " - "); i >= 0 {
		lo, err := juliaBound(s, start, start+i, true)
		if err != nil {
			return t, err
		}
		j := start + i + 3
		hi, err := juliaBound(s, j, end, true)
		if err != nil {
			return t, err
		}
		t.Op, t.Lo, t.Hi = "-", lo, hi
		return t, nil
	}
	i := start
	for _, op := range []string{">=", "≥", "<", "=", "^", "~"} {
		if strings.HasPrefix(s[i:end], op) {
			t.Op, i = op, i+len(op)
			break
		}
	}
	if t.Op != "" && t.Op != "^" && t.Op != "~" {
		for i < end && juliaBlank(s[i]) {
			i++
		}
	}
	lo, err := juliaBound(s, i, end, false)
	if err != nil {
		return t, err
	}
	if lo.N == 3 && lo.Parts == [3]uint64{} {
		return t, errors.New("invalid version 0.0.0")
	}
	t.Lo = lo
	return t, nil
}

// juliaBound parses s[start:end] as `v?N(.N(.N)?)?`; with blanks, blanks around it are
// skipped (the halves of a hyphen range).
func juliaBound(s string, start, end int, blanks bool) (JuliaBound, error) {
	if blanks {
		for start < end && juliaBlank(s[start]) {
			start++
		}
		for end > start && juliaBlank(s[end-1]) {
			end--
		}
	}
	if start < end && s[start] == 'v' {
		start++
	}
	b := JuliaBound{Off: start, End: end}
	i := start
	for {
		j := i
		for j < end && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		if j == i || b.N == 3 {
			return b, fmt.Errorf("invalid version %q", s[b.Off:end])
		}
		p, err := strconv.ParseUint(s[i:j], 10, 64)
		if err != nil {
			return b, fmt.Errorf("invalid version %q", s[b.Off:end])
		}
		b.Parts[b.N], b.N, i = p, b.N+1, j
		if i == end {
			return b, nil
		}
		if s[i] != '.' {
			return b, fmt.Errorf("invalid version %q", s[b.Off:end])
		}
		i++
	}
}

// MatchRange implements RangeMatcher with Pkg's compat semantics for rng, a compat entry: a
// version is admitted when any term admits it. `1.2.3`/`^1.2.3` is [1.2.3, 2), `0.2.3` is
// [0.2.3, 0.3), `0.0.3` only 0.0.3, `0` is [0, 1), `0.0` [0, 0.1); `~1.2.3` and `~1.2` are
// [1.2.x, 1.3), `~1` is [1, 2); `=1.2` is 1.2.0 only; `>= 1.2` has no upper bound, `< 1.2` no lower
// one; `1.2 - 2` is [1.2, 3). A prerelease or build counts as its release.
func (juliaScheme) MatchRange(rng string) (func(v string) bool, error) {
	terms, err := ParseJuliaCompat(rng)
	if err != nil {
		return nil, err
	}
	return func(s string) bool {
		v, ok := parseCargo(s)
		if !ok {
			return false
		}
		for _, t := range terms {
			if t.Admits(v.parts) {
				return true
			}
		}
		return false
	}, nil
}
