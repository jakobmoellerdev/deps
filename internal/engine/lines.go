package engine

import (
	"context"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// lining is the datasource of a partial tool version (toolversions.Partial: asdf/mise `1.55`
// of a tool whose datasource lists only full versions): Releases appends a derived "X.Y" and
// "X" (without "v") per release line of the stable `[v]X.Y.Z` and `[v]X.Y` releases, and the
// bare form of a `vX.Y`/`vX` release, unless the datasource lists them itself, so tag
// versioning finds candidates of the version's precision. Release times of a derived line
// are those of its first release. Only one resolver uses it, sequentially.
type lining struct {
	ds    Datasource
	first map[string]string // derived line -> release name of its lowest release
}

// liningTimer is a lining datasource whose inner datasource is a Timer.
type liningTimer struct {
	*lining
	timer datasource.Timer
}

// withLines wraps ds for a partial tool version.
func withLines(ds Datasource) Datasource {
	l := &lining{ds: ds}
	if t, ok := ds.(datasource.Timer); ok {
		return liningTimer{l, t}
	}
	return l
}

func (l *lining) Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) {
	rels, err := l.ds.Releases(ctx, d, dir)
	if err != nil {
		return nil, err
	}
	listed := make(map[string]bool, len(rels))
	for _, rel := range rels {
		listed[rel.Version] = true
	}
	lowest := map[string]versioning.Tag{}
	l.first = map[string]string{}
	var lines []string
	for _, rel := range rels {
		t, ok := versioning.ParseTag(rel.Version)
		if !ok {
			continue
		}
		v := strings.TrimPrefix(rel.Version, "v")
		var derived [3]string
		switch len(t.Parts) {
		case 3:
			derived[0], derived[1] = v[:strings.LastIndexByte(v, '.')], v[:strings.IndexByte(v, '.')]
		case 2:
			derived[0] = v[:strings.IndexByte(v, '.')]
		}
		if t.Prefix != "" && len(t.Parts) < 3 {
			derived[2] = v
		}
		for _, line := range derived {
			if line == "" || listed[line] {
				continue
			}
			low, seen := lowest[line]
			if !seen {
				lines = append(lines, line)
			}
			if !seen || versioning.CompareTag(t, low) < 0 {
				lowest[line], l.first[line] = t, rel.Version
			}
		}
	}
	if len(lines) == 0 {
		return rels, nil
	}
	out := make([]Release, 0, len(rels)+len(lines))
	out = append(out, rels...)
	for _, line := range lines {
		out = append(out, Release{Version: line})
	}
	return out, nil
}

// name returns the release a derived line stands for (its first release), else version.
func (l *lining) name(version string) string {
	if n, ok := l.first[version]; ok {
		return n
	}
	return version
}

func (l *lining) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	return l.ds.Digest(ctx, d, l.name(version))
}

func (l liningTimer) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	return l.timer.ReleaseTime(ctx, d, l.name(version))
}

// ReleasedBy forwards the inner bound.
func (l liningTimer) ReleasedBy(d dep.Dep) time.Time {
	if b, ok := l.timer.(datasource.ReleaseBound); ok {
		return b.ReleasedBy(d)
	}
	return time.Time{}
}
