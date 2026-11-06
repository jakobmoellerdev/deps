// Package dartversion lists Dart SDK releases from the Dart archive (the dart-archive bucket of
// Google Cloud Storage the SDK downloads come from).
package dartversion

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "dart-version"

// DefaultURL is the Google Cloud Storage endpoint serving the dart-archive bucket;
// DEPS_DART_ARCHIVE_URL overrides it (a mirror serving the JSON API).
const DefaultURL = "https://storage.googleapis.com"

// Repo is the source repository of the Dart SDK, whose tags are the releases.
const Repo = "https://github.com/dart-lang/sdk"

// Channels are the release channels listed, as Renovate's dart-version does.
var Channels = []string{"stable", "beta", "dev"}

// Source lists the archive once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	base   string

	once     sync.Once
	releases []datasource.Release
	err      error
}

// New returns the dart-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_DART_ARCHIVE_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{client: client, sem: sem, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_DART_ARCHIVE_URL"), DefaultURL), "/")}
}

// Releases returns the SDK versions of every channel, oldest first: stable releases
// (`3.5.4`), betas (`3.6.0-216.1.beta`) and dev releases (`3.7.0-1.0.dev`); schemes drop the
// latter two for a stable current version. Old Subversion revision numbers, `latest` and a
// stable version listed in another channel are skipped. Every name is the dep's (`dart`).
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	s.once.Do(func() { s.releases, s.err = s.load(ctx) })
	return s.releases, s.err
}

// SourceRepo implements datasource.SourceRepo: every release is a tag of dart-lang/sdk.
func (*Source) SourceRepo(context.Context, dep.Dep, string) (string, error) { return Repo, nil }

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// load lists every channel concurrently.
func (s *Source) load(ctx context.Context) ([]datasource.Release, error) {
	start := time.Now()
	lists := make([][]*semver.Version, len(Channels))
	errs := make([]error, len(Channels))
	var wg sync.WaitGroup
	for i, ch := range Channels {
		wg.Go(func() { lists[i], errs[i] = s.channel(ctx, ch) })
	}
	wg.Wait()
	var all []*semver.Version
	for _, l := range lists {
		all = append(all, l...)
	}
	err := errors.Join(errs...)
	if err == nil && len(all) == 0 {
		err = errors.New("no Dart releases")
	}
	slog.DebugContext(ctx, "dart releases", "url", s.base, "releases", len(all), "duration", time.Since(start), "err", err)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(all, (*semver.Version).Compare)
	all = slices.CompactFunc(all, func(a, b *semver.Version) bool { return a.Original() == b.Original() }) // listed in two channels
	rels := make([]datasource.Release, len(all))
	for i, v := range all {
		rels[i] = datasource.Release{Version: v.Original()}
	}
	return rels, nil
}

// listing is a page of the Cloud Storage objects list with delimiter "/": the release
// directories in prefixes.
type listing struct {
	Prefixes      []string `json:"prefixes"`
	NextPageToken string   `json:"nextPageToken"`
}

// channel lists `GET <base>/storage/v1/b/dart-archive/o?delimiter=/&prefix=channels/<ch>/release/`
// page by page: one prefix `channels/<ch>/release/<version>/` per release.
func (s *Source) channel(ctx context.Context, ch string) ([]*semver.Version, error) {
	prefix := "channels/" + ch + "/release/"
	var out []*semver.Version
	token := ""
	for {
		q := url.Values{"delimiter": {"/"}, "prefix": {prefix}, "alt": {"json"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page listing
		err := datasource.GetStream(ctx, s.client, s.sem, s.base+"/storage/v1/b/dart-archive/o?"+q.Encode(), func(r io.Reader) error {
			return json.UnmarshalRead(r, &page)
		})
		if err != nil {
			return nil, err
		}
		for _, p := range page.Prefixes {
			v, err := semver.StrictNewVersion(strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/"))
			if err != nil || ch != "stable" && v.Prerelease() == "" {
				continue // latest, a Subversion revision, or a stable release listed as non-stable
			}
			out = append(out, v)
		}
		if token = page.NextPageToken; token == "" {
			return out, nil
		}
	}
}
