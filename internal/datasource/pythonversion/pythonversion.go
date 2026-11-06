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

	index datasource.Once[index]
}

// index is the parsed release list.
type index struct {
	releases []datasource.Release
	times    map[string]time.Time
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
	ix, err := s.load(ctx)
	return ix.releases, err
}

// ReleaseTime implements datasource.Timer with the release date of version; a derived
// "X.Y"/"X" has the date of the line's first release. Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	ix, err := s.load(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return ix.times[version], nil
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) load(ctx context.Context) (index, error) {
	return s.index.Do(ctx, func() (index, error) {
		start := time.Now()
		var ix index
		err := datasource.GetStream(ctx, s.client, s.sem, s.url, ix.read)
		if err != nil {
			ix = index{}
		}
		slog.DebugContext(ctx, "python releases", "url", s.url, "releases", len(ix.releases), "duration", time.Since(start), "err", err)
		return ix, err
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
func (ix *index) read(r io.Reader) error {
	ix.times = map[string]time.Time{}
	var derived []datasource.Release
	err := datasource.DecodeArray(r, func(rel release) {
		m := nameRe.FindStringSubmatch(rel.Name)
		if m == nil || !rel.IsPublished {
			return
		}
		v := m[1]
		ix.releases = append(ix.releases, datasource.Release{Version: v})
		t, err := time.Parse(time.RFC3339, rel.ReleaseDate)
		if err == nil {
			ix.times[v] = t
		}
		if rel.PreRelease || strings.ContainsAny(v, "abr") {
			return // prereleases have no release line
		}
		minor := v[:strings.LastIndexByte(v, '.')]
		for _, p := range []string{minor, v[:strings.IndexByte(v, '.')]} {
			first, seen := ix.times[p]
			if !seen {
				derived = append(derived, datasource.Release{Version: p})
			}
			if err == nil && (first.IsZero() || t.Before(first)) {
				ix.times[p] = t
			} else if !seen {
				ix.times[p] = time.Time{} // marks the line as seen
			}
		}
	})
	if errors.Is(err, datasource.ErrNotArray) {
		return errors.New("malformed release list")
	}
	if err != nil {
		return err
	}
	if len(ix.releases) == 0 {
		return errors.New("no releases")
	}
	ix.releases = append(ix.releases, derived...)
	return nil
}
