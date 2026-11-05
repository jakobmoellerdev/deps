// Package gradleversion lists Gradle releases from the services.gradle.org version index.
package gradleversion

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "gradle-version"

// DefaultURL is the version index; DEPS_GRADLE_VERSIONS_URL overrides it (mirrors).
const DefaultURL = "https://services.gradle.org/versions/all"

// buildTimeLayout is the layout of `buildTime` (`20241120161829+0000`).
const buildTimeLayout = "20060102150405-0700"

// Source fetches the version index once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	url    string

	once     sync.Once
	releases []datasource.Release
	times    map[string]time.Time
	err      error
}

// New returns the gradle-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_GRADLE_VERSIONS_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	u := DefaultURL
	if v := getenv("DEPS_GRADLE_VERSIONS_URL"); v != "" {
		u = v
	}
	return &Source{client: client, sem: sem, url: u}
}

// Releases returns the published versions (`8.10.2`, `8.11-rc-1`, `9.0-milestone-1`) without
// snapshots, nightlies and broken releases; the versioning scheme filters prereleases.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	s.load(ctx)
	return s.releases, s.err
}

// ReleaseTime implements datasource.Timer with the build time of version; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	s.load(ctx)
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.times[version], nil
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
		slog.DebugContext(ctx, "gradle releases", "url", s.url, "releases", len(s.releases), "duration", time.Since(start), "err", s.err)
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
	dec := jsontext.NewDecoder(resp.Body, jsontext.AllowDuplicateNames(true))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return fmt.Errorf("GET %s: malformed index", s.url)
	}
	s.times = map[string]time.Time{}
	for dec.PeekKind() != ']' {
		var r struct {
			Version        string `json:"version"`
			BuildTime      string `json:"buildTime"`
			Snapshot       bool   `json:"snapshot"`
			Nightly        bool   `json:"nightly"`
			ReleaseNightly bool   `json:"releaseNightly"`
			Broken         bool   `json:"broken"`
		}
		if err := json.UnmarshalDecode(dec, &r); err != nil {
			return fmt.Errorf("GET %s: %w", s.url, err)
		}
		if r.Version == "" || r.Snapshot || r.Nightly || r.ReleaseNightly || r.Broken {
			continue
		}
		s.releases = append(s.releases, datasource.Release{Version: r.Version})
		if t, err := time.Parse(buildTimeLayout, r.BuildTime); err == nil {
			s.times[r.Version] = t
		}
	}
	if len(s.releases) == 0 {
		return errors.New("GET " + s.url + ": no releases")
	}
	return nil
}
