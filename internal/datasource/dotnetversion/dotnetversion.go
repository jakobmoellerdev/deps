// Package dotnetversion lists .NET SDK and runtime releases from the .NET release metadata
// (the releases-index.json the dotnet-install scripts read, and each channel's releases.json).
package dotnetversion

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "dotnet-version"

// Dep names: the .NET SDK (global.json `sdk.version`) and the .NET runtime.
const (
	SDK     = "dotnet-sdk"
	Runtime = "dotnet-runtime"
)

// DefaultIndexURL is the release metadata index; DEPS_DOTNET_RELEASES_URL overrides it.
const DefaultIndexURL = "https://builds.dotnet.microsoft.com/dotnet/release-metadata/releases-index.json"

// Source fetches the index once per run and each channel's releases at most once.
type Source struct {
	client *http.Client
	sem    chan struct{}
	url    string

	once     sync.Once
	channels []channel
	err      error

	releases datasource.Memo[string, *releases] // by releases.json URL
}

// channel is an entry of the index: a major.minor line (`8.0`) and its releases.json.
type channel struct {
	version      [2]int
	releasesJSON string
}

// releases is what a channel lists per dep name: versions newest first, with release dates.
type releases struct {
	sdk, runtime []datasource.Release
	times        map[string]time.Time
}

// New returns the dotnet-version datasource. sem bounds concurrent requests (nil =
// unbounded); getenv supplies DEPS_DOTNET_RELEASES_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{client: client, sem: sem, url: cmp.Or(getenv("DEPS_DOTNET_RELEASES_URL"), DefaultIndexURL)}
}

// Releases lists the versions of d (dotnet-sdk: every SDK feature band of every release;
// dotnet-runtime: the runtime of every release), previews included, newest channel first.
// Channels older than d's version (its major.minor) cannot hold an update and are not
// fetched.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	if d.Name != SDK && d.Name != Runtime {
		return nil, fmt.Errorf("dotnet-version: unknown name %q (want %s or %s)", d.Name, SDK, Runtime)
	}
	lists, err := s.lists(ctx, d.Version)
	if err != nil {
		return nil, err
	}
	var out []datasource.Release
	for _, l := range lists {
		if d.Name == SDK {
			out = append(out, l.sdk...)
		} else {
			out = append(out, l.runtime...)
		}
	}
	return out, nil
}

// ReleaseTime implements datasource.Timer with the `release-date` of the release that shipped
// version; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	lists, err := s.lists(ctx, version)
	if err != nil {
		return time.Time{}, err
	}
	for _, l := range lists {
		if t, ok := l.times[version]; ok {
			return t, nil
		}
	}
	return time.Time{}, nil
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lists returns the releases of the channels at or above the major.minor of version (every
// channel when it has none), fetched concurrently.
func (s *Source) lists(ctx context.Context, version string) ([]*releases, error) {
	s.once.Do(func() { s.err = s.index(ctx) })
	if s.err != nil {
		return nil, s.err
	}
	floor, hasFloor := majorMinor(version)
	var want []channel
	for _, c := range s.channels {
		if !hasFloor || c.version[0] > floor[0] || c.version[0] == floor[0] && c.version[1] >= floor[1] {
			want = append(want, c)
		}
	}
	out := make([]*releases, len(want))
	errs := make([]error, len(want))
	var wg sync.WaitGroup
	for i, c := range want {
		wg.Go(func() {
			out[i], errs[i] = s.releases.Do(c.releasesJSON, func() (*releases, error) { return s.channel(ctx, c.releasesJSON) })
		})
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

// majorMinor returns the first two numeric parts of v (`8.0.100` → 8, 0).
func majorMinor(v string) ([2]int, bool) {
	a, rest, _ := strings.Cut(v, ".")
	b, _, _ := strings.Cut(rest, ".")
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	return [2]int{x, y}, errA == nil && errB == nil
}

// index reads the channel list, newest first as published.
func (s *Source) index(ctx context.Context) error {
	start := time.Now()
	var ix struct {
		Index []struct {
			Channel  string `json:"channel-version"`
			Releases string `json:"releases.json"`
		} `json:"releases-index"`
	}
	err := datasource.GetStream(ctx, s.client, s.sem, s.url, func(r io.Reader) error { return json.UnmarshalRead(r, &ix) })
	for _, c := range ix.Index {
		v, ok := majorMinor(c.Channel + ".")
		if ok && c.Releases != "" {
			s.channels = append(s.channels, channel{version: v, releasesJSON: c.Releases})
		}
	}
	if err == nil && len(s.channels) == 0 {
		err = errors.New("dotnet-version: no channels in " + s.url)
	}
	slog.DebugContext(ctx, "dotnet release index", "url", s.url, "channels", len(s.channels), "duration", time.Since(start), "err", err)
	return err
}

// channel reads one releases.json: per release its date, runtime version and SDK versions
// (`sdk` and every `sdks` entry, one per feature band).
func (s *Source) channel(ctx context.Context, u string) (*releases, error) {
	start := time.Now()
	type version struct {
		Version string `json:"version"`
	}
	var doc struct {
		Releases []struct {
			Date    string    `json:"release-date"`
			Runtime *version  `json:"runtime"`
			SDK     *version  `json:"sdk"`
			SDKs    []version `json:"sdks"`
		} `json:"releases"`
	}
	err := datasource.GetStream(ctx, s.client, s.sem, u, func(r io.Reader) error { return json.UnmarshalRead(r, &doc) })
	slog.DebugContext(ctx, "dotnet channel releases", "url", u, "releases", len(doc.Releases), "duration", time.Since(start), "err", err)
	if err != nil {
		return nil, err
	}
	out := &releases{times: map[string]time.Time{}}
	add := func(list *[]datasource.Release, v string, t time.Time) {
		if _, seen := out.times[v]; v == "" || seen {
			return
		}
		*list = append(*list, datasource.Release{Version: v})
		out.times[v] = t
	}
	for _, r := range doc.Releases {
		t, _ := time.Parse(time.DateOnly, r.Date)
		if r.Runtime != nil {
			add(&out.runtime, r.Runtime.Version, t)
		}
		if r.SDK != nil {
			add(&out.sdk, r.SDK.Version, t)
		}
		for _, v := range r.SDKs {
			add(&out.sdk, v.Version, t)
		}
	}
	return out, nil
}
