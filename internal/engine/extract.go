package engine

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"time"
	"weak"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// extracting is the datasource of one dep with an extractVersion: Releases keeps the release
// names the expression matches, as their extracted versions, and the other lookups map a
// version back to its release name. With fallback, a datasource none of whose release names
// match keeps all of them unchanged. Only one resolver uses it, sequentially.
type extracting struct {
	ds       Datasource
	x        *versioning.Extractor
	fallback bool
	names    map[string]string // extracted version -> release name; first match wins
}

// releaseNamer maps a version back to its release name (implemented by extracting).
type releaseNamer interface{ name(version string) string }

// extractingTimer is an extracting datasource whose inner datasource is a Timer, so the
// resolver's Timer check sees the same capability as without extractVersion.
type extractingTimer struct {
	*extracting
	timer datasource.Timer
}

// withExtract wraps ds for a dep with extractVersion x.
func withExtract(ds Datasource, x *versioning.Extractor) Datasource {
	return wrapExtract(&extracting{ds: ds, x: x})
}

// wrapExtract returns e, as an extractingTimer when its datasource is a Timer.
func wrapExtract(e *extracting) Datasource {
	if t, ok := e.ds.(datasource.Timer); ok {
		return extractingTimer{e, t}
	}
	return e
}

func (e *extracting) Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) {
	rels, err := e.ds.Releases(ctx, d, dir)
	if err != nil {
		return nil, err
	}
	var out []Release
	out, e.names = extract(rels, e.x)
	if len(out) == 0 && e.fallback {
		return rels, nil
	}
	return out, nil
}

// extractions shares one extraction between the deps of a run: datasources return one shared
// release list per package, so a monorepo whose manifests pin the same package under the
// same extractVersion would otherwise run the expression over every release name once per
// dep. Lists are keyed by their first element and length as in releaseNames, weakly and with
// a cleanup, so a datasource returning fresh lists does not keep extractions alive;
// expressions are interned by versioning.CompileExtract, so their pointer identifies them.
// The cached releases and names are shared and must not be modified.
var extractions sync.Map // extractKey -> extraction

type extractKey struct {
	first weak.Pointer[Release]
	n     int
	x     *versioning.Extractor
}

type extraction struct {
	rels  []Release
	names map[string]string // extracted version -> release name; first match wins
}

// extract returns the releases of rels x matches, as their extracted versions, and the names
// they were extracted from.
func extract(rels []Release, x *versioning.Extractor) ([]Release, map[string]string) {
	if len(rels) == 0 {
		return nil, nil
	}
	k := extractKey{weak.Make(&rels[0]), len(rels), x}
	if v, ok := extractions.Load(k); ok {
		e := v.(extraction)
		return e.rels, e.names
	}
	e := extraction{make([]Release, 0, len(rels)), make(map[string]string, len(rels))}
	for _, rel := range rels {
		v, ok := x.Version(rel.Version)
		if !ok {
			continue
		}
		if _, dup := e.names[v]; dup {
			continue
		}
		e.names[v] = rel.Version
		e.rels = append(e.rels, Release{Version: v, Digest: rel.Digest})
	}
	e.rels = slices.Clip(e.rels) // shared: an append by a caller must not write into it
	if prev, loaded := extractions.LoadOrStore(k, e); loaded {
		e = prev.(extraction) // a concurrent extraction of the same list came first
	} else {
		runtime.AddCleanup(&rels[0], dropExtraction, k)
	}
	return e.rels, e.names
}

// dropExtraction forgets the extraction of a collected list.
func dropExtraction(k extractKey) { extractions.Delete(k) }

// name returns the release name of version: the one it was extracted from, else version
// itself (the current version before Releases ran, e.g. a digest-only refresh).
func (e *extracting) name(version string) string {
	if n, ok := e.names[version]; ok {
		return n
	}
	return version
}

func (e *extracting) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	return e.ds.Digest(ctx, d, e.name(version))
}

func (e extractingTimer) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	return e.timer.ReleaseTime(ctx, d, e.name(version))
}

// ReleasedBy forwards the inner bound; it does not depend on version names.
func (e extractingTimer) ReleasedBy(d dep.Dep) time.Time {
	if b, ok := e.timer.(datasource.ReleaseBound); ok {
		return b.ReleasedBy(d)
	}
	return time.Time{}
}
