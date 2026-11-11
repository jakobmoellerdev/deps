package versioning

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// MavenInterval is one restriction of a Maven version range: `[1.0,2.0)`, `(,1.0]`,
// `[1.5,)` or the exact `[1.0]` (Min == Max, both inclusive). An empty bound is unbounded;
// the spans locate the bounds within the range string.
type MavenInterval struct {
	Min, Max         string
	MinIncl, MaxIncl bool
	MinSpan, MaxSpan dep.Span
	min, max         mavenVersion // the parsed bounds, which admits compares per candidate
}

// Exact reports whether the interval is a single version (`[1.0]`).
func (i MavenInterval) Exact() bool {
	return i.MinIncl && i.MaxIncl && i.Min != "" && i.Min == i.Max && i.MinSpan == i.MaxSpan
}

func (i *MavenInterval) admits(v mavenVersion) bool {
	if i.Min != "" {
		if c := compareMaven(v, i.min); c < 0 || c == 0 && !i.MinIncl {
			return false
		}
	}
	if i.Max != "" {
		if c := compareMaven(v, i.max); c > 0 || c == 0 && !i.MaxIncl {
			return false
		}
	}
	return true
}

// MavenRange is a Maven version range: comma separated restrictions, any of which admits a
// version (https://maven.apache.org/pom.html#Dependency_Version_Requirement_Specification).
type MavenRange []MavenInterval

// IsMavenRange reports whether s is written as a Maven range (starts with `[` or `(`).
func IsMavenRange(s string) bool {
	return strings.HasPrefix(s, "[") || strings.HasPrefix(s, "(")
}

// mavenRanges caches the parsed form of each range string ParseMavenRange is asked for: the
// engine checks and rewrites the range of a dep for every candidate release, and the maven,
// ant, clojure and sbt managers parse it again each time. The cached range is shared, so
// callers read it and do not write to it.
var mavenRanges sync.Map // string -> mavenParsed

// mavenParsed is what ParseMavenRange returned for one range string.
type mavenParsed struct {
	r   MavenRange
	err error
}

// ParseMavenRange parses a Maven version range as Maven's VersionRange.createFromVersionSpec
// does: every restriction is bracketed, a single version must be inclusive (`[1.0]`), at least
// one bound is set and the lower bound does not exceed the upper one. The result is cached
// (mavenRanges) and must not be modified.
func ParseMavenRange(s string) (MavenRange, error) {
	if p, ok := mavenRanges.Load(s); ok {
		e := p.(mavenParsed)
		return e.r, e.err
	}
	r, err := parseMavenRange(s)
	p, _ := mavenRanges.LoadOrStore(s, mavenParsed{r, err})
	e := p.(mavenParsed)
	return e.r, e.err
}

// parseMavenRange is ParseMavenRange without the cache, for the ranges Extend builds: one per
// candidate release, which the cache would keep for nothing.
func parseMavenRange(s string) (MavenRange, error) {
	var r MavenRange
	i := 0
	for {
		i = skipBlanks(s, i)
		if i >= len(s) || s[i] != '[' && s[i] != '(' {
			return nil, errors.New("expected [ or ( at " + strconv.Itoa(i))
		}
		end := strings.IndexAny(s[i:], "])")
		if end < 0 {
			return nil, errors.New("unterminated restriction")
		}
		end += i
		in, err := interval(s, i, end)
		if err != nil {
			return nil, err
		}
		r = append(r, in)
		i = skipBlanks(s, end+1)
		if i == len(s) {
			return r, nil
		}
		if s[i] != ',' {
			return nil, errors.New("expected , at " + strconv.Itoa(i))
		}
		i++
	}
}

// interval parses the restriction s[open:closer+1].
func interval(s string, open, closer int) (MavenInterval, error) {
	in := MavenInterval{MinIncl: s[open] == '[', MaxIncl: s[closer] == ']'}
	body := s[open+1 : closer]
	bound := func(start int, text string) (string, dep.Span, error) {
		t := strings.TrimSpace(text)
		if t == "" {
			return "", dep.Span{}, nil
		}
		if !validMaven(t) {
			return "", dep.Span{}, errors.New("invalid version " + strconv.Quote(t))
		}
		off := start + strings.Index(text, t)
		return t, dep.Span{Start: off, End: off + len(t)}, nil
	}
	lo, hi, comma := strings.Cut(body, ",")
	var err error
	if in.Min, in.MinSpan, err = bound(open+1, lo); err != nil {
		return in, err
	}
	if !comma {
		if !in.MinIncl || !in.MaxIncl || in.Min == "" {
			return in, errors.New("a single version must be [v]")
		}
		in.Max, in.MaxSpan = in.Min, in.MinSpan
		in.min, _ = parseMaven(in.Min)
		in.max = in.min
		return in, nil
	}
	if in.Max, in.MaxSpan, err = bound(open+1+len(lo)+1, hi); err != nil {
		return in, err
	}
	if in.Min == "" && in.Max == "" {
		return in, errors.New("a restriction needs a bound")
	}
	in.min, _ = parseMaven(in.Min)
	in.max, _ = parseMaven(in.Max)
	if in.Min != "" && in.Max != "" {
		if c := compareMaven(in.min, in.max); c > 0 || c == 0 && !(in.MinIncl && in.MaxIncl) {
			return in, errors.New("empty range")
		}
	}
	return in, nil
}

// Admits reports whether one of the restrictions admits the version v.
func (r MavenRange) Admits(v string) bool {
	mv, ok := parseMaven(v)
	if !ok {
		return false
	}
	for i := range r {
		if r[i].admits(mv) {
			return true
		}
	}
	return false
}

// Extend returns the range s moved to admit v as Renovate's autoExtendMavenRange does: an
// exact `[1.0]` becomes `[v]`; otherwise the last restriction v is at or past the upper bound
// of (or the one without an upper bound) changes: a range one step wide (`[1,2)`,
// `[1,2-alpha)`) shifts to v at the lower bound's precision (`[4,5)` for 4.3.2), an inclusive
// upper bound becomes v (at the bound's precision when it ends in a number), an exclusive one
// v at its precision incremented (`[1.0,2.0)` → `[1.0,2.6)` for 2.5.1, `(,2)` → `(,3)`), and a range
// without an upper bound gets v's lower bound. false when s does not parse, admits v already
// or the result would not admit v.
func (r MavenRange) Extend(s, v string) (string, bool) {
	if !validMaven(v) || r.Admits(v) {
		return "", false
	}
	nv, _ := parseMaven(v)
	if len(r) == 1 && r[0].Exact() {
		return s[:r[0].MinSpan.Start] + v + s[r[0].MinSpan.End:], true
	}
	idx := -1
	for i, in := range slices.Backward(r) {
		if in.Max == "" {
			idx = i
			break
		}
		hi, _ := parseMaven(in.Max)
		if c := compareMaven(hi, nv); c < 0 || c == 0 && !in.MaxIncl {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false
	}
	in := r[idx]
	type repl struct {
		span dep.Span
		text string
	}
	var reps []repl
	switch {
	case in.Min != "" && in.Max != "" && (incrementBound(in.Min) == in.Max || incrementBound(in.Min)+"-alpha" == in.Max):
		lo := coerceBound(in.Min, v)
		hi := incrementBound(lo)
		if strings.HasSuffix(in.Max, "-alpha") {
			hi += "-alpha"
		}
		reps = []repl{{in.MinSpan, lo}, {in.MaxSpan, hi}}
	case in.Max == "":
		reps = []repl{{in.MinSpan, coerceBound(in.Min, v)}}
	case in.MaxIncl:
		hi := v
		if last := in.Max[strings.LastIndexAny(in.Max, ".-")+1:]; isDigits(last) {
			hi = coerceBound(in.Max, v)
		}
		reps = []repl{{in.MaxSpan, hi}}
	default:
		reps = []repl{{in.MaxSpan, incrementBound(coerceBound(in.Max, v))}}
	}
	out := s
	for _, rp := range slices.Backward(reps) { // the upper bound first: spans stay valid
		out = out[:rp.span.Start] + rp.text + out[rp.span.End:]
	}
	nr, err := parseMavenRange(out)
	if out == s || err != nil || !nr.Admits(v) {
		return "", false
	}
	return out, true
}

// coerceBound is next at the precision of prev (Renovate's coerceRangeValue): next's first
// dot-separated components, as many as prev has, padded with prev's when next has fewer
// (`1.0` and 4.3.2 → `4.3`; `1.0.0` and 4.3 → `4.3.0`). A qualifier of next is dropped when
// prev has none.
func coerceBound(prev, next string) string {
	if !strings.Contains(prev, "-") {
		next, _, _ = strings.Cut(next, "-")
	}
	p, n := strings.Split(prev, "."), strings.Split(next, ".")
	out := n[:min(len(p), len(n))]
	out = append(slices.Clip(out), p[len(out):]...)
	return strings.Join(out, ".")
}

// incrementBound increments the last dot-separated component of v when it is a number
// (`2.0` → `2.1`, `1` → `2`); else v is returned unchanged.
func incrementBound(v string) string {
	i := strings.LastIndexByte(v, '.') + 1
	n, err := strconv.ParseUint(v[i:], 10, 64)
	if err != nil || !isDigits(v[i:]) {
		return v
	}
	return v[:i] + strconv.FormatUint(n+1, 10)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
