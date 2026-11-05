// Package nodeversion lists Node.js releases from the nodejs.org distribution index.
package nodeversion

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's; the alias node maps to it).
const Name = "node-version"

// DefaultDistURL is the distribution base URL; DEPS_NODE_DIST_URL overrides it (mirrors).
const DefaultDistURL = "https://nodejs.org/dist"

// Source fetches <dist>/index.json once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	url    string

	once     sync.Once
	releases []datasource.Release
	times    map[string]time.Time // by version without "v"
	err      error
}

// New returns the node-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_NODE_DIST_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	base := DefaultDistURL
	if v := getenv("DEPS_NODE_DIST_URL"); v != "" {
		base = v
	}
	return &Source{client: client, sem: sem, url: strings.TrimSuffix(base, "/") + "/index.json"}
}

// Releases returns every release as published (`v26.5.0`, prereleases included; schemes
// drop them for stable current versions), then a derived "X.Y" and "X" per stable release
// line without "v", so partial values (`node-version: 26`, engines `^26`) have candidates of
// their precision under tag versioning. The derived entries come last, so a scheme that
// parses `27` as 27.0.0 (semver) prefers the published `v27.0.0` on a tie. Versions without
// "v" in files are written back without it by the semver scheme.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	s.load(ctx)
	return s.releases, s.err
}

// ReleaseTime implements datasource.Timer with the release date of version (with or without
// "v"); a derived "X.Y"/"X" has the date of the line's first release. Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	s.load(ctx)
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.times[strings.TrimPrefix(version, "v")], nil
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) load(ctx context.Context) {
	s.once.Do(func() {
		start := time.Now()
		s.err = s.fetch(ctx)
		if s.err != nil {
			s.releases, s.times = nil, nil
		}
		slog.DebugContext(ctx, "node releases", "url", s.url, "releases", len(s.releases), "duration", time.Since(start), "err", s.err)
	})
}

// release is an element of the index, an array of objects with large file lists that is
// decoded one element at a time.
type release struct {
	Version string `json:"version"`
	Date    string `json:"date"`
}

func (s *Source) fetch(ctx context.Context) error {
	return datasource.GetStream(ctx, s.client, s.sem, s.url, func(r io.Reader) error {
		s.times = map[string]time.Time{}
		var derived []datasource.Release
		err := datasource.DecodeArray(r, func(rel release) {
			if rel.Version == "" {
				return
			}
			s.releases = append(s.releases, datasource.Release{Version: rel.Version})
			t, err := time.Parse(time.DateOnly, rel.Date)
			v := strings.TrimPrefix(rel.Version, "v")
			if err == nil {
				s.times[v] = t
			}
			if strings.ContainsAny(v, "-+") || strings.Count(v, ".") != 2 {
				return // prereleases have no release line; partial versions are not published
			}
			minor := v[:strings.LastIndexByte(v, '.')]
			for _, p := range []string{minor, v[:strings.IndexByte(v, '.')]} {
				if _, seen := s.times[p]; !seen {
					derived = append(derived, datasource.Release{Version: p})
				}
				if err == nil || s.times[p].IsZero() {
					s.times[p] = t // the index is newest first: the last write is the line's first release
				}
			}
		})
		if errors.Is(err, datasource.ErrNotArray) {
			return errors.New("malformed index")
		}
		if err != nil {
			return err
		}
		if len(s.releases) == 0 {
			return errors.New("no releases")
		}
		s.releases = append(s.releases, derived...)
		return nil
	})
}
