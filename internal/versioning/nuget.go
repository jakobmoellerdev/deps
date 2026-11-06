package versioning

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// nugetVersion is a NuGet version (NuGetVersion): one to four dot-separated numeric parts
// (missing ones are 0; `1.2` = `1.2.0.0`), an optional `-<prerelease>` of dot-separated
// [0-9A-Za-z-] labels and an optional `+<metadata>`, which does not count. Leading zeros are
// allowed (`1.01` = `1.1`), as NuGet reads legacy versions.
type nugetVersion struct {
	parts [4]uint64
	n     int    // numeric parts as written
	pre   string // without "-"; "" = a release
}

// parseNuGet parses s as NuGetVersion.TryParse does (without surrounding blanks).
func parseNuGet(s string) (nugetVersion, bool) {
	var v nugetVersion
	if s == "" {
		return v, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !nugetLabels(s[i+1:]) {
			return v, false
		}
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !nugetLabels(s[i+1:]) {
			return v, false
		}
		v.pre, s = s[i+1:], s[:i]
	}
	for {
		if v.n == len(v.parts) {
			return v, false
		}
		j := 0
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		if j == 0 {
			return v, false
		}
		n, err := strconv.ParseUint(s[:j], 10, 63)
		if err != nil {
			return v, false
		}
		v.parts[v.n] = n
		v.n++
		if j == len(s) {
			return v, true
		}
		if s[j] != '.' || j+1 == len(s) {
			return v, false
		}
		s = s[j+1:]
	}
}

// nugetLabels reports whether s is a non-empty dot-separated list of non-empty [0-9A-Za-z-]
// labels.
func nugetLabels(s string) bool {
	if s == "" {
		return false
	}
	for l := range strings.SplitSeq(s, ".") {
		if l == "" {
			return false
		}
		for i := range len(l) {
			if c := l[i]; !(isLetter(c) || '0' <= c && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// compareNuGet is NuGet's VersionComparer.Default: the four numeric parts, then a release
// above its prereleases, whose labels compare pairwise as SemVer 2.0 orders them (numeric
// labels numerically and below alphanumeric ones, alphanumeric ones case-insensitively, a
// shorter list below a longer one it starts).
func compareNuGet(a, b nugetVersion) int {
	for i := range a.parts {
		if a.parts[i] != b.parts[i] {
			if a.parts[i] < b.parts[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	x, y := a.pre, b.pre
	for x != "" && y != "" {
		var l, m string
		l, x, _ = strings.Cut(x, ".")
		m, y, _ = strings.Cut(y, ".")
		if c := compareNuGetLabel(l, m); c != 0 {
			return c
		}
	}
	switch {
	case x == "" && y == "":
		return 0
	case x == "":
		return -1
	}
	return 1
}

func compareNuGetLabel(a, b string) int {
	na, errA := strconv.ParseUint(a, 10, 64)
	nb, errB := strconv.ParseUint(b, 10, 64)
	switch {
	case errA == nil && errB == nil:
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
		return 0
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

// nugetCompat rejects prereleases unless cur is one.
func nugetCompat(cur, next nugetVersion) string {
	if next.pre != "" && cur.pre == "" {
		return "prerelease"
	}
	return ""
}

// nugetKind: a change of the first part is major, of the second minor, anything else (the
// third and fourth parts, the prerelease) patch.
func nugetKind(cur, next nugetVersion) dep.UpdateType {
	switch {
	case cur.parts[0] != next.parts[0]:
		return dep.Major
	case cur.parts[1] != next.parts[1]:
		return dep.Minor
	}
	return dep.Patch
}

func (v nugetVersion) semver() *msemver.Version {
	return msemver.New(v.parts[0], v.parts[1], v.parts[2], v.pre, "")
}

func (v nugetVersion) major() string { return strconv.FormatUint(v.parts[0], 10) }

// nugetScheme is the versioning of NuGet packages (Renovate's `nuget`), with NuGet version
// ranges (MatchRange).
type nugetScheme struct{ impl[nugetVersion] }

// NuGetRange is a parsed NuGet version range (VersionRange): an interval such as `[1.0,2.0)`,
// `(,2.0]` or `[1.0,)`, or a floating version (`1.*`, `1.2.*`, `1.0.0-*`, `1.0.0-rc*`, `*`,
// `*-*`). A bare version (`1.2`) is `[1.2,)`; `[1.2]` is exactly 1.2.
type NuGetRange struct {
	// Interval bounds as written (blanks trimmed), "" = unbounded; Min/MaxSpan are their byte
	// offsets in the range string, Min/MaxIncl whether they are included.
	Min, Max         string
	MinSpan, MaxSpan dep.Span
	MinIncl, MaxIncl bool
	Bare             bool // a bare version, written without brackets (`1.2.3`)
	// Float is the floating version, "" for an interval: Prefix is its numeric parts before
	// the `*` (`1.2.*` → [1 2]; `*` → none), Star whether the numeric part floats (`1.*`) and
	// PrePrefix the prerelease prefix of a floating prerelease (`1.0.0-rc*` → "rc"; `-*` →
	// ""), FloatPre whether the prerelease floats.
	Float     string
	Prefix    []uint64
	Star      bool
	PrePrefix string
	FloatPre  bool

	lo, hi nugetVersion // Min and Max parsed
}

// Exact reports whether the range is `[v]`; `[v,v]`, an interval with equal bounds, is not:
// its bounds are two spans to rewrite.
func (r NuGetRange) Exact() bool {
	return r.Float == "" && !r.Bare && r.MinIncl && r.MaxIncl && r.Min != "" && r.MinSpan == r.MaxSpan
}

// ParseNuGetRange parses s as VersionRange.TryParse does: an interval with inclusive `[ ]` or
// exclusive `( )` bounds around `min,max` (one side may be empty), `[v]` for exactly v, a bare
// version for `[v,)`, or a floating version. Floating bounds inside an interval are rejected.
func ParseNuGetRange(s string) (NuGetRange, error) {
	var r NuGetRange
	t := strings.TrimSpace(s)
	off := strings.Index(s, t)
	if t == "" {
		return r, errors.New("empty version range")
	}
	if strings.Contains(t, "*") {
		return parseNuGetFloat(t)
	}
	if t[0] != '[' && t[0] != '(' {
		v, ok := parseNuGet(t)
		if !ok {
			return r, fmt.Errorf("invalid version %q", t)
		}
		r.Min, r.MinIncl, r.MinSpan, r.Bare, r.lo = t, true, dep.Span{Start: off, End: off + len(t)}, true, v
		return r, nil
	}
	last := t[len(t)-1]
	if last != ']' && last != ')' {
		return r, fmt.Errorf("invalid version range %q", t)
	}
	r.MinIncl, r.MaxIncl = t[0] == '[', last == ']'
	inner := t[1 : len(t)-1]
	base := off + 1
	lo, hi, comma := strings.Cut(inner, ",")
	bound := func(raw string, at int) (string, nugetVersion, dep.Span, error) {
		v := strings.TrimSpace(raw)
		if v == "" {
			return "", nugetVersion{}, dep.Span{}, nil
		}
		p, ok := parseNuGet(v)
		if !ok {
			return "", nugetVersion{}, dep.Span{}, fmt.Errorf("invalid version %q in range %q", v, t)
		}
		st := at + strings.Index(raw, v)
		return v, p, dep.Span{Start: st, End: st + len(v)}, nil
	}
	var err error
	if r.Min, r.lo, r.MinSpan, err = bound(lo, base); err != nil {
		return r, err
	}
	if !comma {
		// `[v]` is exactly v; `(v)`, `[v)` and `[]` are invalid.
		if r.Min == "" || !r.MinIncl || !r.MaxIncl {
			return r, fmt.Errorf("invalid version range %q", t)
		}
		r.Max, r.hi, r.MaxSpan = r.Min, r.lo, r.MinSpan
		return r, nil
	}
	if strings.Contains(hi, ",") {
		return r, fmt.Errorf("invalid version range %q", t)
	}
	if r.Max, r.hi, r.MaxSpan, err = bound(hi, base+len(lo)+1); err != nil {
		return r, err
	}
	if r.Min == "" && r.Max == "" {
		return r, fmt.Errorf("invalid version range %q", t)
	}
	if r.Min != "" && r.Max != "" {
		if c := compareNuGet(r.lo, r.hi); c > 0 || c == 0 && (!r.MinIncl || !r.MaxIncl) {
			return r, fmt.Errorf("invalid version range %q", t)
		}
	}
	return r, nil
}

// parseNuGetFloat parses a floating version (FloatRange): `*`, `<n>.*` up to three numeric
// parts before the star, `<version>-*` or `<version>-<prefix>*`, and `*-*` / `<n>.*-*`.
func parseNuGetFloat(t string) (NuGetRange, error) {
	r := NuGetRange{Float: t}
	bad := fmt.Errorf("invalid floating version %q", t)
	num, pre, hasPre := strings.Cut(t, "-")
	if hasPre {
		if !strings.HasSuffix(pre, "*") || strings.Count(pre, "*") != 1 {
			return r, bad
		}
		r.FloatPre, r.PrePrefix = true, strings.TrimSuffix(pre, "*")
		if p := strings.TrimSuffix(r.PrePrefix, "."); p != "" && !nugetLabels(p) {
			return r, bad
		}
	}
	switch {
	case num == "*":
		r.Star = true
	case strings.HasSuffix(num, ".*"):
		r.Star = true
		v, ok := parseNuGet(strings.TrimSuffix(num, ".*"))
		if !ok || v.n > 3 {
			return r, bad
		}
		r.Prefix = v.parts[:v.n]
	default:
		v, ok := parseNuGet(num)
		if !ok || !hasPre {
			return r, bad
		}
		r.Prefix = v.parts[:v.n]
	}
	if r.Star && hasPre && r.PrePrefix != "" {
		return r, bad // `1.*-rc*` is not a NuGet float
	}
	return r, nil
}

// Admits reports whether the range admits version v of the nuget scheme. Intervals admit a
// prerelease only when a bound is one, as NuGet resolves without -prerelease; a floating range
// admits the versions it floats over: `1.*` the stable 1.x, `1.2.3-*` 1.2.3 and its
// prereleases, `1.2.3-rc*` the prereleases of 1.2.3 starting with rc (case-insensitive),
// `1.*-*` every 1.x, `*` every stable version, `*-*` every version.
func (r NuGetRange) Admits(s string) bool {
	v, ok := parseNuGet(s)
	if !ok {
		return false
	}
	if r.Float != "" {
		for i, p := range r.Prefix {
			if v.parts[i] != p {
				return false
			}
		}
		if !r.Star { // a fixed release with a floating prerelease
			for i := len(r.Prefix); i < len(v.parts); i++ {
				if v.parts[i] != 0 {
					return false
				}
			}
		}
		switch {
		case v.pre == "":
			return r.Star || r.PrePrefix == ""
		case !r.FloatPre:
			return false
		}
		return strings.HasPrefix(strings.ToLower(v.pre), strings.ToLower(r.PrePrefix))
	}
	pre := false
	if r.Min != "" {
		c := compareNuGet(v, r.lo)
		if c < 0 || c == 0 && !r.MinIncl {
			return false
		}
		pre = r.lo.pre != ""
	}
	if r.Max != "" {
		c := compareNuGet(v, r.hi)
		if c > 0 || c == 0 && !r.MaxIncl {
			return false
		}
		pre = pre || r.hi.pre != ""
	}
	return v.pre == "" || pre
}

// MatchRange implements RangeMatcher with NuGet version range semantics (NuGetRange.Admits).
func (nugetScheme) MatchRange(rng string) (func(v string) bool, error) {
	r, err := ParseNuGetRange(rng)
	if err != nil {
		return nil, err
	}
	return r.Admits, nil
}

// NuGetParts returns the numeric parts of the nuget-scheme version v as written (`1.2` → [1
// 2]) and its prerelease, or ok false when v is invalid.
func NuGetParts(v string) (parts []uint64, pre string, ok bool) {
	p, ok := parseNuGet(v)
	if !ok {
		return nil, "", false
	}
	return p.parts[:p.n], p.pre, true
}
