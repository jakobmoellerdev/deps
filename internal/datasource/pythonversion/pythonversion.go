// Package pythonversion lists CPython releases from the python.org downloads API.
package pythonversion

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "python-version"

// DefaultURL is the python.org release list (Renovate's registry URL); DEPS_PYTHON_RELEASES_URL
// overrides it.
const DefaultURL = "https://www.python.org/api/v2/downloads/release/"

// nameRe is the name of a CPython release in the list (`Python 3.14.0`, `Python 3.15.0rc1`);
// other entries (`Python install manager 26.4`) are not Python versions.
var nameRe = regexp.MustCompile(`^Python (\d+\.\d+\.\d+(?:(?:a|b|rc)\d+)?)$`)

// Source fetches the release list once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	url    string

	once     sync.Once
	releases []datasource.Release
	times    map[string]time.Time
	err      error
}

// New returns the python-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_PYTHON_RELEASES_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{client: client, sem: sem, url: cmp.Or(getenv("DEPS_PYTHON_RELEASES_URL"), DefaultURL)}
}

// Releases returns every published release (`3.14.10`, prereleases such as `3.15.0rc1`
// included; schemes drop them for stable current versions), then a derived "X.Y" and "X" per
// stable release line, so partial values (`.python-version` `3.13`) have candidates of their
// precision under tag versioning. The derived entries come last, so a scheme that compares
// `3.14` equal to 3.14.0 (pep440) prefers the published `3.14.0` on a tie.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	s.load(ctx)
	return s.releases, s.err
}

// ReleaseTime implements datasource.Timer with the release date of version; a derived
// "X.Y"/"X" has the date of the line's first release. Zero when unknown.
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
		s.err = datasource.GetStream(ctx, s.client, s.sem, s.url, s.read)
		if s.err != nil {
			s.releases, s.times = nil, nil
		}
		slog.DebugContext(ctx, "python releases", "url", s.url, "releases", len(s.releases), "duration", time.Since(start), "err", s.err)
	})
}

// release is an element of the list, decoded one element at a time.
type release struct {
	Name        string `json:"name"`
	IsPublished bool   `json:"is_published"`
	PreRelease  bool   `json:"pre_release"`
	ReleaseDate string `json:"release_date"`
}

// read parses the release list, a JSON array in no particular order, so the first release of
// a line is the earliest date seen.
func (s *Source) read(r io.Reader) error {
	s.times = map[string]time.Time{}
	var derived []datasource.Release
	err := datasource.DecodeArray(r, func(rel release) {
		m := nameRe.FindStringSubmatch(rel.Name)
		if m == nil || !rel.IsPublished {
			return
		}
		v := m[1]
		s.releases = append(s.releases, datasource.Release{Version: v})
		t, err := time.Parse(time.RFC3339, rel.ReleaseDate)
		if err == nil {
			s.times[v] = t
		}
		if rel.PreRelease || strings.ContainsAny(v, "abr") {
			return // prereleases have no release line
		}
		minor := v[:strings.LastIndexByte(v, '.')]
		for _, p := range []string{minor, v[:strings.IndexByte(v, '.')]} {
			first, seen := s.times[p]
			if !seen {
				derived = append(derived, datasource.Release{Version: p})
			}
			if err == nil && (first.IsZero() || t.Before(first)) {
				s.times[p] = t
			} else if !seen {
				s.times[p] = time.Time{} // marks the line as seen
			}
		}
	})
	if errors.Is(err, datasource.ErrNotArray) {
		return errors.New("malformed release list")
	}
	if err != nil {
		return err
	}
	if len(s.releases) == 0 {
		return errors.New("no releases")
	}
	s.releases = append(s.releases, derived...)
	return nil
}
