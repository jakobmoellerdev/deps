package versioning

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// condaVersion is a conda package version as conda's VersionOrder reads it:
// `[<epoch>!]<version>[+<local>]`, case-insensitive, segments separated by "." or "_" ("-" too
// when the version has no "_"), each segment a run of numbers and words (`1.1.1w`, `2024a`,
// `9e`, `1.0.0rc1`). Parsing allocates nothing for lowercase versions: segments are iterated
// over the strings on comparison.
type condaVersion struct {
	raw     string // as written
	epoch   int64
	main    string // lowercased, "-" normalized to "_"
	local   string // after "+", "" = none
	nseg    int    // segments of main
	mainOff int    // offset of main in raw
	pre     bool
}

// condaMaxDigits bounds a number component so it fits an int64.
const condaMaxDigits = 18

// parseConda validates s as conda's VersionOrder does (characters `[0-9a-z._+!]` after
// lowercasing and the "-" rewrite, at most one epoch and local part, no empty segments, no
// `*`), and additionally requires the version to start with a digit, so words such as
// `latest` are not versions.
func parseConda(s string) (condaVersion, bool) {
	v := condaVersion{raw: s}
	low := strings.ToLower(s) // returns s unchanged when it has no upper-case letter
	if strings.IndexByte(low, '-') >= 0 && strings.IndexByte(low, '_') < 0 {
		low = strings.ReplaceAll(low, "-", "_")
	}
	for i := range len(low) {
		c := low[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c == '.' || c == '_' || c == '+' || c == '!') {
			return condaVersion{}, false
		}
	}
	rest := low
	if e, r, ok := strings.Cut(rest, "!"); ok {
		n, err := strconv.ParseInt(e, 10, 64)
		if err != nil || n < 0 || !allDigits(e) {
			return condaVersion{}, false
		}
		v.epoch, rest, v.mainOff = n, r, len(e)+1
	}
	main, local, hasLocal := strings.Cut(rest, "+")
	if strings.ContainsAny(local, "+!") || strings.IndexByte(main, '!') >= 0 || hasLocal && !validCondaParts(local) {
		return condaVersion{}, false
	}
	if !validCondaParts(main) || main[0] < '0' || main[0] > '9' {
		return condaVersion{}, false
	}
	v.main, v.local = main, local
	for i := 0; ; {
		seg, next, ok := condaSeg(main, i)
		if !ok {
			break
		}
		v.nseg++
		v.pre = v.pre || condaPreSeg(seg)
		i = next
	}
	return v, true
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// validCondaParts reports whether s is non-empty, has no empty segment and its number runs fit
// an int64.
func validCondaParts(s string) bool {
	if s == "" {
		return false
	}
	run, prevSep := 0, true
	for i := range len(s) {
		c := s[i]
		sep := c == '.' || c == '_'
		if sep && prevSep {
			return false
		}
		prevSep = sep
		if c >= '0' && c <= '9' {
			run++
			if run > condaMaxDigits {
				return false
			}
		} else {
			run = 0
		}
	}
	return !prevSep
}

// condaSeg returns the segment of s starting at i and the start of the next one; ok is false
// at the end.
func condaSeg(s string, i int) (seg string, next int, ok bool) {
	if i >= len(s) {
		return "", i, false
	}
	j := i
	for j < len(s) && s[j] != '.' && s[j] != '_' {
		j++
	}
	return s[i:j], j + 1, true
}

// condaPreSeg reports whether a segment marks a development or prerelease: a `dev`, `rc`,
// `alpha`, `beta`, `pre` or `preview` word, or an `a`, `b` or `c` followed by a number
// (`0a1`, `2b3`). Other words are part of the release (`1.1.1w`, `2024a`, `9e`).
func condaPreSeg(seg string) bool {
	it := condaComps{s: seg, first: true}
	prevWord := ""
	for {
		c, ok := it.next()
		if !ok {
			return false
		}
		switch {
		case c.kind == compStr && c.s == condaDev:
			return true
		case c.kind == compStr:
			switch c.s {
			case "rc", "alpha", "beta", "pre", "preview":
				return true
			}
			prevWord = c.s
			continue
		case c.kind == compNum && (prevWord == "a" || prevWord == "b" || prevWord == "c"):
			return true
		}
		prevWord = ""
	}
}

// Component kinds in ascending order: a word sorts below a number, `post` above every number.
const (
	compStr = iota
	compNum
	compInf
)

// condaDev is how conda stores `dev`: upper case, so it sorts below every other word.
const condaDev = "DEV"

type condaComp struct {
	kind int
	n    int64
	s    string
}

// condaComps iterates the components of one segment: runs of digits (numbers) and of other
// characters (words). A segment starting with a word gets an implicit leading 0, as in conda.
type condaComps struct {
	s     string
	i     int
	first bool
}

func (it *condaComps) next() (condaComp, bool) {
	if it.first {
		it.first = false
		if it.s == "" || it.s[0] < '0' || it.s[0] > '9' {
			return condaComp{kind: compNum}, true
		}
	}
	if it.i >= len(it.s) {
		return condaComp{}, false
	}
	j := it.i
	digit := it.s[j] >= '0' && it.s[j] <= '9'
	for it.i < len(it.s) && (it.s[it.i] >= '0' && it.s[it.i] <= '9') == digit {
		it.i++
	}
	w := it.s[j:it.i]
	switch {
	case digit:
		n, _ := strconv.ParseInt(w, 10, 64) // validCondaParts bounds the digits
		return condaComp{kind: compNum, n: n}, true
	case w == "post":
		return condaComp{kind: compInf}, true
	case w == "dev":
		return condaComp{kind: compStr, s: condaDev}, true
	}
	return condaComp{kind: compStr, s: w}, true
}

func compareCondaComp(a, b condaComp) int {
	switch {
	case a.kind == compStr && b.kind == compStr:
		return strings.Compare(a.s, b.s)
	case a.kind == compStr:
		return -1
	case b.kind == compStr:
		return 1
	case a.kind != b.kind:
		return cmp.Compare(a.kind, b.kind)
	}
	return cmp.Compare(a.n, b.n)
}

// compareCondaSeg compares two segments component by component, a missing component counting
// as 0.
func compareCondaSeg(a, b string) int {
	x, y := condaComps{s: a, first: true}, condaComps{s: b, first: true}
	for {
		ca, okA := x.next()
		cb, okB := y.next()
		if !okA && !okB {
			return 0
		}
		if !okA {
			ca = condaComp{kind: compNum}
		}
		if !okB {
			cb = condaComp{kind: compNum}
		}
		if c := compareCondaComp(ca, cb); c != 0 {
			return c
		}
	}
}

// compareCondaParts compares dot-separated parts segment by segment, a missing segment
// counting as 0.
func compareCondaParts(a, b string) int {
	i, j := 0, 0
	for {
		sa, ni, okA := condaSeg(a, i)
		sb, nj, okB := condaSeg(b, j)
		if !okA && !okB {
			return 0
		}
		if !okA {
			sa = "0"
		}
		if !okB {
			sb = "0"
		}
		if c := compareCondaSeg(sa, sb); c != 0 {
			return c
		}
		i, j = ni, nj
	}
}

// compareConda is conda's VersionOrder ordering: epoch, then the version, then the local part,
// segment by segment; within a segment a word sorts below a number (`1.0a` < `1.0` < `1.0.1`),
// words compare bytewise with `dev` below every other word, and `post` above every number.
// Trailing zero segments do not count (`1.2` = `1.2.0`).
func compareConda(a, b condaVersion) int {
	if c := cmp.Compare(a.epoch, b.epoch); c != 0 {
		return c
	}
	if c := compareCondaParts(a.main, b.main); c != 0 {
		return c
	}
	return compareCondaParts(a.local, b.local)
}

// condaCompat rejects prereleases unless cur is one, local versions unless cur has one, and
// candidates with non-zero segments beyond cur's precision (as pep440 does).
func condaCompat(cur, next condaVersion) string {
	switch {
	case next.pre && !cur.pre:
		return "prerelease"
	case next.local != "" && cur.local == "":
		return "suffix"
	}
	i := 0
	for range cur.nseg {
		_, i, _ = condaSeg(next.main, i)
	}
	for {
		seg, n, ok := condaSeg(next.main, i)
		if !ok {
			return ""
		}
		if compareCondaSeg(seg, "0") != 0 {
			return "precision"
		}
		i = n
	}
}

// condaKind: a new epoch or first segment is major, a new second segment minor, anything
// else patch.
func condaKind(cur, next condaVersion) dep.UpdateType {
	c0, ci, _ := condaSeg(cur.main, 0)
	n0, ni, _ := condaSeg(next.main, 0)
	if cur.epoch != next.epoch || compareCondaSeg(c0, n0) != 0 {
		return dep.Major
	}
	c1, _, okC := condaSeg(cur.main, ci)
	n1, _, okN := condaSeg(next.main, ni)
	if !okC {
		c1 = "0"
	}
	if !okN {
		n1 = "0"
	}
	if compareCondaSeg(c1, n1) != 0 {
		return dep.Minor
	}
	return dep.Patch
}

// semver maps the leading numbers of the first three segments to MAJOR.MINOR.PATCH; a
// prerelease gets the semver prerelease "pre".
func (v condaVersion) semver() *msemver.Version {
	var parts [3]uint64
	i := 0
	for k := range parts {
		seg, next, ok := condaSeg(v.main, i)
		if !ok {
			break
		}
		it := condaComps{s: seg, first: true}
		if c, _ := it.next(); c.kind == compNum {
			parts[k] = uint64(c.n) // numbers are never negative
		}
		i = next
	}
	pre := ""
	if v.pre {
		pre = "pre"
	}
	return msemver.New(parts[0], parts[1], parts[2], pre, "")
}

func (v condaVersion) major() string {
	seg, _, _ := condaSeg(v.main, 0)
	it := condaComps{s: seg, first: true}
	c, _ := it.next()
	return strconv.FormatInt(c.n, 10)
}

// segEnd returns the length of the first n segments of v's main part.
func (v condaVersion) segEnd(n int) int {
	end := 0
	for range n {
		_, end, _ = condaSeg(v.main, end)
	}
	return min(end, len(v.main)+1) - 1
}

// cut writes v with only its first n segments (and its local part), as published otherwise.
func (v condaVersion) cut(n int) string {
	if v.nseg <= n {
		return v.raw
	}
	out := v.raw[:v.mainOff+v.segEnd(n)]
	if v.local != "" {
		out += v.raw[v.mainOff+len(v.main):]
	}
	return out
}

// FitConda writes next with at most as many segments as cur has, for the version of a fuzzy
// conda spec (`=1.26`, `1.26.*`), whose precision is its meaning: `=1.26` → `=2.3` for
// 2.3.4. Either version invalid returns next unchanged.
func FitConda(cur, next string) string {
	c, okC := parseConda(cur)
	n, okN := parseConda(next)
	if !okC || !okN {
		return next
	}
	return n.cut(c.nseg)
}

// condaScheme is the versioning of conda packages (Renovate's and Dependabot's `conda`).
// Best and Rank keep cur's precision as pep440 does: a candidate with more segments than cur
// (only zeros beyond it, others fail "precision") is cut to cur's segments, so `=1.26` with
// candidate 2.3.0 yields 2.3. Ranges are conda version specs (MatchRange).
type condaScheme struct{ impl[condaVersion] }

func (s condaScheme) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	best, ok := s.impl.Best(cur, cands, accept, reject)
	if !ok {
		return "", false
	}
	c, _ := parseConda(cur)
	b, _ := parseConda(best)
	return b.cut(c.nseg), true
}

func (s condaScheme) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) Ranking {
	r := s.rank(cur, cands, accept, reject)
	r.style = func(v string) string {
		p, _ := parseConda(v)
		return p.cut(r.cur.nseg)
	}
	return r
}

// CondaClause is one clause of a conda version spec: Op is `==`, `!=`, `>=`, `<=`, `>`, `<`,
// `~=`, `=` (fuzzy: the versions starting with Version, as `Version.*`) or "" (exact, or fuzzy
// when Glob); Glob reports a trailing `*` or `.*`, which Version omits. Start and End are the
// byte offsets of the clause in the spec, VerStart that of Version.
type CondaClause struct {
	Op, Version          string
	Glob                 bool
	Start, VerStart, End int
}

// Fuzzy reports whether the clause matches the versions starting with Version (`=1.2`,
// `1.2.*`, `==1.2.*`).
func (c CondaClause) Fuzzy() bool {
	return c.Op == "=" || c.Glob && (c.Op == "" || c.Op == "==")
}

var condaOps = []string{"==", "!=", ">=", "<=", "~=", ">", "<", "="}

// ParseCondaSpec parses a conda version spec (the version part of a match spec: `1.2.*`,
// `>=1.2,<2`, `==1.2.3`, `=1.2`): clauses joined by `,`, each an optional operator and a conda
// version, optionally ending in `*` or `.*`. `|` alternatives and `*` alone are not supported.
func ParseCondaSpec(spec string) ([]CondaClause, error) {
	var out []CondaClause
	start := 0
	for {
		end := strings.IndexByte(spec[start:], ',')
		if end < 0 {
			end = len(spec)
		} else {
			end += start
		}
		c, err := parseCondaClause(spec, start, end)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
		if end == len(spec) {
			return out, nil
		}
		start = end + 1
	}
}

func parseCondaClause(spec string, start, end int) (CondaClause, error) {
	i := skipBlanks(spec, start)
	c := CondaClause{Start: i}
	for _, o := range condaOps {
		if strings.HasPrefix(spec[i:end], o) {
			c.Op = o
			break
		}
	}
	i = skipBlanks(spec, i+len(c.Op))
	j := end
	for j > i && (spec[j-1] == ' ' || spec[j-1] == '\t') {
		j--
	}
	c.VerStart, c.End = i, j
	v := spec[i:j]
	if t, ok := strings.CutSuffix(v, "*"); ok {
		v, c.Glob = strings.TrimSuffix(t, "."), true
	}
	c.Version = v
	if _, ok := parseConda(v); !ok || c.Glob && c.Op != "" && c.Op != "==" && c.Op != "!=" && c.Op != "=" {
		return CondaClause{}, fmt.Errorf("invalid conda version spec %q", spec)
	}
	return c, nil
}

// MatchRange implements RangeMatcher with conda's VersionSpec semantics for the spec rng
// (ParseCondaSpec): all clauses must hold; a fuzzy clause matches the versions whose segments
// start with its version (`1.2` matches 1.2, 1.2.0 and 1.2.5, not 1.20), `~=1.4.5` is
// `>=1.4.5,1.4.*`. Prereleases match like any version.
func (condaScheme) MatchRange(rng string) (func(v string) bool, error) {
	clauses, err := ParseCondaSpec(rng)
	if err != nil {
		return nil, err
	}
	parsed := make([]condaVersion, len(clauses))
	for i, c := range clauses {
		parsed[i], _ = parseConda(c.Version)
	}
	return func(s string) bool {
		v, ok := parseConda(s)
		if !ok {
			return false
		}
		for i, c := range clauses {
			if !condaAdmits(c, parsed[i], v) {
				return false
			}
		}
		return true
	}, nil
}

// condaAdmits reports whether v satisfies the clause c with the version r.
func condaAdmits(c CondaClause, r, v condaVersion) bool {
	if c.Fuzzy() {
		return condaStartsWith(v, r)
	}
	n := compareConda(v, r)
	switch c.Op {
	case "", "==":
		return n == 0
	case "!=":
		if c.Glob {
			return !condaStartsWith(v, r)
		}
		return n != 0
	case ">":
		return n > 0
	case ">=":
		return n >= 0
	case "<":
		return n < 0
	case "<=":
		return n <= 0
	}
	// ~=: at least r, and starting with r without its last segment
	if n < 0 || r.nseg < 2 {
		return n >= 0
	}
	p := r
	p.main, p.local = r.main[:r.segEnd(r.nseg-1)], ""
	p.nseg--
	return condaStartsWith(v, p)
}

// condaStartsWith is conda's VersionOrder.startswith: v's segments equal prefix's up to its
// last one, whose components must equal v's except the last, which must be equal (a number)
// or a prefix of v's word.
func condaStartsWith(v, prefix condaVersion) bool {
	if v.epoch != prefix.epoch {
		return false
	}
	i, j := 0, 0
	for k := range prefix.nseg {
		ps, nj, _ := condaSeg(prefix.main, j)
		vs, ni, ok := condaSeg(v.main, i)
		if !ok {
			vs = ""
		}
		if k < prefix.nseg-1 {
			if compareCondaSeg(cmp.Or(vs, "0"), ps) != 0 {
				return false
			}
			i, j = ni, nj
			continue
		}
		return condaSegStartsWith(vs, ps)
	}
	return true
}

// condaSegStartsWith compares the last segment of a startswith prefix: every component but
// the last must equal v's, the last must equal v's number or prefix v's word.
func condaSegStartsWith(vs, ps string) bool {
	x, y := condaComps{s: vs, first: true}, condaComps{s: ps, first: true}
	cp, _ := y.next()
	for {
		cv, okV := x.next()
		if !okV {
			cv = condaComp{kind: compNum} // conda's fill value
		}
		next, more := y.next()
		if !more {
			if cp.kind == compStr {
				return cv.kind == compStr && strings.HasPrefix(cv.s, cp.s)
			}
			return compareCondaComp(cv, cp) == 0
		}
		if compareCondaComp(cv, cp) != 0 {
			return false
		}
		cp = next
	}
}
