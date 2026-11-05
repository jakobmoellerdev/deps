package engine

import (
	"context"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// extracting is the datasource of one dep with an extractVersion: Releases keeps the release
// names the expression matches, as their extracted versions, and the other lookups map a
// version back to its release name. Only one resolver uses it, sequentially.
type extracting struct {
	ds    Datasource
	x     *versioning.Extractor
	names map[string]string // extracted version -> release name; first match wins
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
	e := &extracting{ds: ds, x: x}
	if t, ok := ds.(datasource.Timer); ok {
		return extractingTimer{e, t}
	}
	return e
}

func (e *extracting) Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) {
	rels, err := e.ds.Releases(ctx, d, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(rels))
	e.names = make(map[string]string, len(rels))
	for _, rel := range rels {
		v, ok := e.x.Version(rel.Version)
		if !ok {
			continue
		}
		if _, dup := e.names[v]; dup {
			continue
		}
		e.names[v] = rel.Version
		out = append(out, Release{Version: v, Digest: rel.Digest})
	}
	return out, nil
}

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
