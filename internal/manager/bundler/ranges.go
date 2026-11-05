package bundler

import (
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// ruby orders versions and matches requirements.
var (
	ruby, _    = versioning.Get(Versioning)
	rubyRanges = ruby.(versioning.RangeMatcher)
)

// reqString is one requirement string of a dep's Range (the strings as written).
type reqString struct {
	start, end int    // the string in the Range, quotes included
	quote      string // its quote
	clause     versioning.RubyClause
	spaced     bool // a blank separates operator and version (`~> 7.1`, not `~>7.1`)
}

// vs is the offset of the string's content in the Range.
func (s reqString) vs() int { return s.start + len(s.quote) }

// reqList is a parsed Range.
type reqList struct {
	strs  []reqString
	plain bool // only commas and blanks between the strings: pin may join them into one
	match func(string) bool
}

// reqLists caches the parsed Range of each dep (nil when it does not parse): the engine
// rewrites a dep's requirements for every candidate release.
var reqLists sync.Map // string -> *reqList

// parseReqs returns the parsed requirement strings rng (dep.Dep.Range), or nil.
func parseReqs(rng string) *reqList {
	if p, ok := reqLists.Load(rng); ok {
		return p.(*reqList)
	}
	p, _ := reqLists.LoadOrStore(rng, readReqs(rng))
	return p.(*reqList)
}

// readReqs is parseReqs without the cache: the literal strings of rng, each a Gem::Requirement.
func readReqs(rng string) *reqList {
	l := &reqList{plain: true}
	var parts []string
	prev := -1
	for _, t := range lex([]byte(rng)) {
		if t.kind != tString {
			continue // commas, brackets, line breaks between the strings
		}
		c, err := versioning.ParseRubyRequirement(t.text)
		if !t.literal || err != nil {
			return nil
		}
		if prev >= 0 && strings.Trim(rng[prev:t.start], ", \t\r\n") != "" {
			l.plain = false
		}
		prev = t.end
		spaced := c.Op == "" || c.Off > 0 && (t.text[c.Off-1] == ' ' || t.text[c.Off-1] == '\t')
		l.strs = append(l.strs, reqString{start: t.start, end: t.end, quote: rng[t.start:t.vs], clause: c, spaced: spaced})
		parts = append(parts, strings.TrimSpace(t.text))
	}
	if len(l.strs) == 0 {
		return nil
	}
	m, err := rubyRanges.MatchRange(strings.Join(parts, ", "))
	if err != nil {
		return nil
	}
	l.match = m
	return l
}

// exact reports whether the requirements are one exact version (`2.241.2`, `= 1.2`).
func (l *reqList) exact() bool {
	return len(l.strs) == 1 && (l.strs[0].clause.Op == "" || l.strs[0].clause.Op == "=")
}

// lowerBound reports whether a string is a `>=` or `>` requirement, the floor of a `~>` it
// accompanies (`"~> 7.1", ">= 7.1.2"`).
func (l *reqList) lowerBound() bool {
	for _, s := range l.strs {
		if s.clause.Op == ">=" || s.clause.Op == ">" {
			return true
		}
	}
	return false
}

// req is a requirement as rewritten: operator and version.
type req struct{ op, ver string }

func (r req) render(spaced bool) string {
	switch {
	case r.op == "":
		return r.ver
	case spaced:
		return r.op + " " + r.ver
	}
	return r.op + r.ver
}

// Rewrite modes of reqList.rewritten.
const (
	modeReplace = iota // v is outside the requirements: each string follows it
	modeBump           // v is admitted: the floors are raised to it
	modeWiden          // v is outside the requirements: only the upper bounds follow it
)

// rewrite returns the edits (offsets into rng, the Range l was parsed from) writing the
// requirements for v under strategy; ok is false when they stay unchanged or do not admit v.
//
// pin writes `= v`: an exact requirement takes v as its version, any other one becomes `= v`,
// and a list is joined into that one string (only when nothing but commas and blanks separate
// its strings). Otherwise, after Renovate's ruby strategies (bump, replace, widen): a version
// the requirements admit is bumped: `~>` takes it at its own precision (`~> 7.1` → `~> 7.2`
// for 7.2.3, versioning.FitRuby) plus a `>= v` string when that precision cannot hold it and
// no `>=` accompanies it (`"~> 7.1", ">= 7.2.3"`), `>=` and `>` become `>= v`; exact versions,
// upper bounds and exclusions stay. A version outside them replaces them: exact versions take
// it, `~>`, `>=` and `>` take it at their precision (`~> 7.1.2` → `~> 8.0.1` for 8.0.1, where
// Renovate writes `~> 8.0.0`; `>= 1.2` → `>= 2.5`; `>` becomes `>=`), an upper bound v reaches
// moves past it (upperBound: `< 2` → `< 3`, `<= 2.6` → `<= v`), exclusions stay. Under widen
// only the upper bounds move, a `~>` that v outgrows turning into `>= <its version>", "< <bound
// past v>` (`~> 7.1` → `">= 7.1", "< 9"` for 8.0.1; only `< 9` next to a `>=`); when that does
// not admit v, or the requirement is exact, widen replaces.
func (l *reqList) rewrite(rng, v, strategy string) ([]patch.Edit, bool) {
	if !ruby.Valid(v) {
		return nil, false
	}
	var out [][]req
	switch {
	case strategy == dep.RangePin && !l.exact() && len(l.strs) > 1:
		first, last := l.strs[0], l.strs[len(l.strs)-1]
		if !l.plain {
			return nil, false
		}
		s := first.quote + req{"=", v}.render(first.spaced) + first.quote
		return []patch.Edit{{Start: first.start, End: last.end, Old: rng[first.start:last.end], New: s}}, true
	case strategy == dep.RangePin && l.exact():
		out = [][]req{{{l.strs[0].clause.Op, v}}}
	case strategy == dep.RangePin:
		out = [][]req{{{"=", v}}}
	case l.match(v):
		out = l.rewritten(v, modeBump)
	case strategy == dep.RangeWiden && !l.exact():
		if out = l.rewritten(v, modeWiden); !admits(out, v) {
			out = l.rewritten(v, modeReplace)
		}
	default:
		out = l.rewritten(v, modeReplace)
	}
	if !admits(out, v) {
		return nil, false
	}
	var edits []patch.Edit
	for i, s := range l.strs {
		c, n := s.clause, out[i]
		switch {
		case len(n) == 1 && n[0] == req{c.Op, c.Version}:
		case len(n) == 1 && n[0].op == c.Op:
			at := s.vs() + c.Off
			edits = append(edits, patch.Edit{Start: at, End: at + len(c.Version), Old: c.Version, New: n[0].ver})
		default:
			parts := make([]string, len(n))
			for j, r := range n {
				parts[j] = r.render(s.spaced)
			}
			end := s.end - len(s.quote)
			edits = append(edits, patch.Edit{Start: s.vs(), End: end, Old: rng[s.vs():end], New: strings.Join(parts, s.quote+", "+s.quote)})
		}
	}
	return edits, len(edits) > 0
}

// rewritten returns the requirements of each string for v in mode (see rewrite).
func (l *reqList) rewritten(v string, mode int) [][]req {
	lower := l.lowerBound()
	out := make([][]req, len(l.strs))
	for i, s := range l.strs {
		op, ver := s.clause.Op, s.clause.Version
		out[i] = []req{{op, ver}}
		switch mode {
		case modeBump:
			switch op {
			case "~>":
				f := versioning.FitRuby(ver, v)
				out[i] = []req{{op, f}}
				if !lower && ruby.Compare(f, v) != 0 {
					out[i] = append(out[i], req{">=", v})
				}
			case ">=", ">":
				out[i] = []req{{">=", v}}
			}
		case modeWiden:
			switch {
			case op == "<" && ruby.Compare(v, ver) >= 0:
				out[i] = []req{{op, upperBound(ver, v)}}
			case op == "<=" && ruby.Compare(v, ver) > 0:
				out[i] = []req{{op, v}}
			case op == "~>" && ruby.Compare(v, ver) >= 0 && ruby.Compare(joinRelease(release(v)), pessimisticBound(ver)) >= 0:
				limit := req{"<", upperBound(pessimisticBound(ver), v)}
				out[i] = []req{{">=", ver}, limit}
				if lower {
					out[i] = []req{limit}
				}
			}
		default:
			switch op {
			case "", "=":
				out[i] = []req{{op, v}}
			case "~>":
				out[i] = []req{{op, versioning.FitRuby(ver, v)}}
			case ">=", ">":
				f := versioning.FitRuby(ver, v)
				if ruby.Compare(v, ver) < 0 {
					f = v
				}
				out[i] = []req{{">=", f}}
			case "<":
				if ruby.Compare(v, ver) >= 0 {
					out[i] = []req{{op, upperBound(ver, v)}}
				}
			case "<=":
				if ruby.Compare(v, ver) > 0 {
					out[i] = []req{{op, v}}
				}
			}
		}
	}
	return out
}

// admits reports whether the requirements out admit v.
func admits(out [][]req, v string) bool {
	var parts []string
	for _, n := range out {
		for _, r := range n {
			parts = append(parts, r.render(true))
		}
	}
	m, err := rubyRanges.MatchRange(strings.Join(parts, ", "))
	return err == nil && m(v)
}

// release returns the numeric release segments of a Ruby version (`7.1.0.rc1` → [7 1 0]).
func release(v string) []int {
	v, _, _ = strings.Cut(v, "-")
	var out []int
	for seg := range strings.SplitSeq(v, ".") {
		n, err := strconv.Atoi(seg)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func joinRelease(r []int) string {
	var b strings.Builder
	for i, n := range r {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// pessimisticBound is the upper bound of `~> ver`, which it excludes (Gem::Version#bump: `~>
// 7.1` → 8, `~> 7.1.2` → 7.2).
func pessimisticBound(ver string) string {
	r := release(ver)
	if len(r) > 1 {
		r = r[:len(r)-1]
	}
	r[len(r)-1]++
	return joinRelease(r)
}

// upperBound moves the exclusive upper bound past v at the bound's precision, its last
// non-zero segment: v's segments before it, v's one plus one there, zeros after (`< 2` → `< 3`
// and `< 2.0` → `< 3.0` for 2.5.0, `< 1.3` → `< 1.5` for 1.4.2).
func upperBound(bound, v string) string {
	b, nv := release(bound), release(v)
	p := 0
	for i, n := range b {
		if n != 0 {
			p = i
		}
	}
	out := make([]int, len(b))
	for i := range b {
		n := 0
		if i < len(nv) {
			n = nv[i]
		}
		switch {
		case i < p:
			out[i] = n
		case i == p:
			out[i] = n + 1
		}
	}
	return joinRelease(out)
}
