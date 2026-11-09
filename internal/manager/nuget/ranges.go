package nuget

import (
	"cmp"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// setVersion sets the version of d from the value v written at span (span is zero when the
// value cannot be located byte for byte, e.g. with XML entities): a bare version (`13.0.3`,
// NuGet's minimum version, updated as the pinned version it is in practice) and an exact range
// (`[13.0.3]`) get Version and a RefSpan on the version alone; a floating version (`13.*`,
// `1.0.0-rc*`) or an interval (`[1.0,2.0)`) becomes the dep's Range, RefSpan the whole value,
// Version its anchor (the floating prefix, the lower bound, else the upper one).
func setVersion(d *dep.Dep, v string, span dep.Span) {
	t := strings.TrimSpace(v)
	d.Version = t
	r, err := versioning.ParseNuGetRange(v)
	switch {
	case t == "":
		d.Skip = skipNoVersion
		return
	case err != nil:
		d.Skip = skipInvalid + " " + strconv.Quote(t)
		return
	case span == (dep.Span{}):
		d.Skip = skipLocate
		return
	case r.Float != "" && len(r.Prefix) == 0 && r.Star:
		d.Skip = skipFloatAll + " " + t
		return
	}
	off := span.Start + strings.Index(v, t) // t's offset in the file
	switch {
	case r.Bare || r.Exact():
		d.Version = r.Min
		d.RefSpan = dep.Span{Start: span.Start + r.MinSpan.Start, End: span.Start + r.MinSpan.End}
	case r.Float != "":
		d.Range, d.Version = t, floatAnchor(r)
		d.RefSpan = dep.Span{Start: off, End: off + len(t)}
	default:
		d.Range, d.Version = t, cmp.Or(r.Min, r.Max)
		d.RefSpan = dep.Span{Start: off, End: off + len(t)}
	}
}

// floatAnchor is the version a floating range is ranked from: its numeric prefix (`1.2.*` →
// 1.2), with its prerelease prefix, or 0, when the prerelease floats (`1.0.0-rc*` →
// 1.0.0-rc, `1.*-*` → 1-0), so prerelease candidates are compatible.
func floatAnchor(r versioning.NuGetRange) string {
	v := joinParts(r.Prefix)
	if r.FloatPre {
		v += "-" + cmp.Or(strings.TrimSuffix(r.PrePrefix, "."), "0")
	}
	return v
}

func joinParts(parts []uint64) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatUint(p, 10))
	}
	return b.String()
}

// ranges caches parseRange by range string (nil when it does not parse): the engine checks and
// rewrites a dep's range for every candidate release.
var ranges sync.Map // string -> *versioning.NuGetRange

// parseRange returns the parsed range rng, or nil.
func parseRange(rng string) *versioning.NuGetRange {
	if p, ok := ranges.Load(rng); ok {
		return p.(*versioning.NuGetRange)
	}
	var p *versioning.NuGetRange
	if r, err := versioning.ParseNuGetRange(rng); err == nil {
		p = &r
	}
	got, _ := ranges.LoadOrStore(rng, p)
	return got.(*versioning.NuGetRange)
}

// InRange implements manager.RangeUpdater: whether d's range admits v
// (versioning.NuGetRange.Admits).
func (rangeEditor) InRange(d dep.Dep, v string) bool {
	r := parseRange(d.Range)
	return r != nil && r.Admits(v)
}

// NewRange implements manager.RangeUpdater for floating versions and intervals. pin writes v
// as a bare version. A floating version v leaves is moved to v at its precision (`1.*` → `2.*`
// for 2.1.0, `1.2.*` → `1.4.*`, `1.0.0-rc*` → `1.1.0-rc*` for 1.1.0-rc.2); one it admits stays.
// An interval v exceeds gets a new upper bound: v when inclusive, else the next value at the
// position where the bounds differ (`[1.0,2.0)` → `[1.0,3.0)` for 2.5.1; `(,2.0)` → `(,3.0)`),
// the lower bound kept; for a v the interval admits the lower bound is raised to v (`[1.0,2.0)`
// → `[1.5.0,2.0)`), which the engine only writes under bump.
func (rangeEditor) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	r := parseRange(d.Range)
	parts, pre, ok := versioning.NuGetParts(v)
	if r == nil || !ok {
		return "", false
	}
	var out string
	switch {
	case strategy == dep.RangePin:
		out = v
	case r.Admits(v) && r.Float != "":
		return "", false
	case r.Float != "":
		out = floatAt(*r, parts, pre)
	case r.Admits(v):
		if r.Min == "" || r.Bare {
			return "", false
		}
		out = d.Range[:r.MinSpan.Start] + v + d.Range[r.MinSpan.End:]
	case r.Max == "":
		return "", false // below the lower bound, or a prerelease it does not admit
	default:
		out = d.Range[:r.MaxSpan.Start] + upperBound(*r, parts, v) + d.Range[r.MaxSpan.End:]
	}
	if out == d.Range {
		return "", false
	}
	if nr, err := versioning.ParseNuGetRange(out); err != nil || !nr.Admits(v) {
		return "", false
	}
	return out, true
}

// floatAt is the floating range r moved to v (numeric parts, prerelease) at r's precision.
func floatAt(r versioning.NuGetRange, parts []uint64, pre string) string {
	if !r.Star { // `1.0.0-rc*`: v's release with the floating prerelease
		if pre == "" && r.PrePrefix != "" {
			return "" // a release is not among `-rc*` prereleases
		}
		return joinParts(parts) + "-" + r.PrePrefix + "*"
	}
	at := make([]uint64, len(r.Prefix))
	copy(at, parts)
	out := joinParts(at) + ".*"
	if r.FloatPre {
		out += "-*"
	}
	return out
}

// upperBound returns the upper bound of r moved past v: v when inclusive, else v's parts up to
// the bound's precision with the last one incremented, padded to the bound's part count.
func upperBound(r versioning.NuGetRange, parts []uint64, v string) string {
	if r.MaxIncl {
		return v
	}
	maxParts, _, _ := versioning.NuGetParts(r.Max)
	p := len(maxParts) - 1
	for p > 0 && maxParts[p] == 0 {
		p--
	}
	if r.Min != "" {
		minParts, _, _ := versioning.NuGetParts(r.Min)
		for i := range maxParts {
			var lo uint64
			if i < len(minParts) {
				lo = minParts[i]
			}
			if lo != maxParts[i] {
				p = i
				break
			}
		}
	}
	out := make([]uint64, len(maxParts))
	copy(out, parts[:min(len(parts), p+1)])
	out[p]++
	return joinParts(out)
}
