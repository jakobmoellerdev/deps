// Package flutterversion lists Flutter SDK releases from the Flutter release list of Flutter's
// storage bucket, the list `flutter upgrade`, FVM and the download page read.
package flutterversion

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "flutter-version"

// DefaultURL is the storage base Flutter downloads from; FLUTTER_STORAGE_BASE_URL, which the
// flutter tool reads for mirrors, overrides it.
const DefaultURL = "https://storage.googleapis.com"

// Repo is the source repository of the Flutter SDK, whose tags are the releases.
const Repo = "https://github.com/flutter/flutter"

// Source reads the release list once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	url    string

	once     sync.Once
	releases []datasource.Release
	times    map[string]time.Time
	err      error
}

// New returns the flutter-version datasource. sem bounds concurrent requests (nil =
// unbounded); getenv supplies FLUTTER_STORAGE_BASE_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	base := strings.TrimSuffix(cmp.Or(getenv("FLUTTER_STORAGE_BASE_URL"), DefaultURL), "/")
	return &Source{client: client, sem: sem, url: base + "/flutter_infra_release/releases/releases_linux.json"}
}

// Releases returns every release of the list, oldest first: stable releases (`3.24.3`, the
// `v`-prefixed old ones as listed) and betas (`3.26.0-0.1.pre`); a stable version listed in the
// beta or dev channel is skipped, as in Renovate. Every name is the dep's (`flutter`).
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	s.load(ctx)
	return s.releases, s.err
}

// ReleaseTime implements datasource.Timer with the `release_date` of version (the earliest,
// for a version listed twice); zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	s.load(ctx)
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.times[version], nil
}

// SourceRepo implements datasource.SourceRepo: every release is a tag of flutter/flutter.
func (*Source) SourceRepo(context.Context, dep.Dep, string) (string, error) { return Repo, nil }

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) load(ctx context.Context) {
	s.once.Do(func() {
		start := time.Now()
		s.err = datasource.GetStream(ctx, s.client, s.sem, s.url, s.read)
		if s.err != nil {
			s.releases, s.times = nil, nil
		}
		slog.DebugContext(ctx, "flutter releases", "url", s.url, "releases", len(s.releases), "duration", time.Since(start), "err", s.err)
	})
}

// read parses the release list: `releases` entries with version, channel and release_date.
func (s *Source) read(r io.Reader) error {
	var list struct {
		Releases []struct {
			Version     string `json:"version"`
			Channel     string `json:"channel"`
			ReleaseDate string `json:"release_date"`
		} `json:"releases"`
	}
	if err := json.UnmarshalRead(r, &list); err != nil {
		return err
	}
	s.times = map[string]time.Time{}
	var vs []*semver.Version
	for _, e := range list.Releases {
		v, err := semver.NewVersion(e.Version)
		if err != nil || v.Original() != e.Version || e.Channel != "stable" && v.Prerelease() == "" && !strings.HasPrefix(e.Version, "v") {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, e.ReleaseDate)
		if old, seen := s.times[e.Version]; seen {
			if !t.IsZero() && (old.IsZero() || t.Before(old)) {
				s.times[e.Version] = t.UTC()
			}
			continue
		}
		s.times[e.Version] = t.UTC()
		vs = append(vs, v)
	}
	if len(vs) == 0 {
		return errors.New("no releases")
	}
	slices.SortStableFunc(vs, (*semver.Version).Compare)
	s.releases = make([]datasource.Release, len(vs))
	for i, v := range vs {
		s.releases[i] = datasource.Release{Version: v.Original()}
	}
	return nil
}
