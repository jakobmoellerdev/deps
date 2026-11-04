// Package nodeversion lists Node.js releases from the nodejs.org distribution index.
package nodeversion

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
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
	return "", fmt.Errorf("%s: digests are not supported for %s", Name, d.Name)
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

func (s *Source) fetch(ctx context.Context) error {
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", s.url, resp.StatusCode)
	}
	// The index is an array of objects with large file lists; decode one element at a time.
	dec := jsontext.NewDecoder(resp.Body)
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return fmt.Errorf("GET %s: malformed index", s.url)
	}
	s.times = map[string]time.Time{}
	var derived []datasource.Release
	for dec.PeekKind() != ']' {
		var r struct {
			Version string `json:"version"`
			Date    string `json:"date"`
		}
		if err := json.UnmarshalDecode(dec, &r); err != nil {
			return fmt.Errorf("GET %s: %w", s.url, err)
		}
		if r.Version == "" {
			continue
		}
		s.releases = append(s.releases, datasource.Release{Version: r.Version})
		t, err := time.Parse(time.DateOnly, r.Date)
		v := strings.TrimPrefix(r.Version, "v")
		if err == nil {
			s.times[v] = t
		}
		if strings.ContainsAny(v, "-+") || strings.Count(v, ".") != 2 {
			continue // prereleases have no release line; partial versions are not published
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
	}
	if len(s.releases) == 0 {
		return errors.New("GET " + s.url + ": no releases")
	}
	s.releases = append(s.releases, derived...)
	return nil
}
