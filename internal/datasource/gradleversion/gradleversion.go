// Package gradleversion lists Gradle releases from the services.gradle.org version index.
package gradleversion

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
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

// release is an element of the version index.
type release struct {
	Version        string `json:"version"`
	BuildTime      string `json:"buildTime"`
	Snapshot       bool   `json:"snapshot"`
	Nightly        bool   `json:"nightly"`
	ReleaseNightly bool   `json:"releaseNightly"`
	Broken         bool   `json:"broken"`
}

func (s *Source) fetch(ctx context.Context) error {
	return datasource.GetStream(ctx, s.client, s.sem, s.url, func(r io.Reader) error {
		s.times = map[string]time.Time{}
		err := datasource.DecodeArray(r, func(rel release) {
			if rel.Version == "" || rel.Snapshot || rel.Nightly || rel.ReleaseNightly || rel.Broken {
				return
			}
			s.releases = append(s.releases, datasource.Release{Version: rel.Version})
			if t, err := time.Parse(buildTimeLayout, rel.BuildTime); err == nil {
				s.times[rel.Version] = t
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
		return nil
	})
}
