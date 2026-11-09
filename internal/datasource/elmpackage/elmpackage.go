// Package elmpackage lists the releases of Elm packages from the Elm package registry
// (package.elm-lang.org): one request per package and run, shared by every elm.json.
package elmpackage

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "elm-package"

// DefaultURL is the Elm package registry; DEPS_ELM_PACKAGE_URL overrides it (a mirror serving
// its API).
const DefaultURL = "https://package.elm-lang.org"

// Source is the elm-package datasource.
type Source struct {
	getter httpx.Getter
	base   string // registry base URL without a trailing slash

	pkgs datasource.Memo[string, *pkg] // by package name
}

// pkg is the release list of a package and the publication time of each version.
type pkg struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the elm-package datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_ELM_PACKAGE_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_ELM_PACKAGE_URL"), DefaultURL), "/")}
}

// Releases lists every published version of the package d.Name (`author/project`), oldest
// first. The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the publication time the registry lists for
// version; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// SourceRepo implements datasource.SourceRepo: `elm publish` only publishes packages from the
// GitHub repository of their name, so the repository is github.com/<name>.
func (*Source) SourceRepo(_ context.Context, d dep.Dep, _ string) (string, error) {
	if _, _, ok := split(d.Name); !ok {
		return "", nil
	}
	return "https://github.com/" + d.Name, nil
}

// Digest is unsupported: Elm package versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// split returns the author and project of an Elm package name.
func split(name string) (author, project string, ok bool) {
	author, project, ok = strings.Cut(name, "/")
	return author, project, ok && author != "" && project != "" && !strings.Contains(project, "/")
}

// lookup fetches `GET <base>/packages/<author>/<project>/releases.json`, a JSON object of
// version → Unix publication time, once per package.
func (s *Source) lookup(ctx context.Context, name string) (*pkg, error) {
	author, project, ok := split(name)
	if !ok {
		return nil, fmt.Errorf("invalid Elm package name %q (want author/project)", name)
	}
	return s.pkgs.Do(ctx, name, func() (*pkg, error) {
		start := time.Now()
		u := s.base + "/packages/" + url.PathEscape(author) + "/" + url.PathEscape(project) + "/releases.json"
		p := &pkg{times: map[string]time.Time{}}
		err := s.getter.Stream(ctx, u, func(r io.Reader) error {
			var raw map[string]int64
			if err := json.UnmarshalRead(r, &raw); err != nil {
				return err
			}
			for v, ts := range raw {
				if _, err := semver.StrictNewVersion(v); err != nil {
					continue
				}
				p.rels = append(p.rels, datasource.Release{Version: v})
				if ts > 0 {
					p.times[v] = time.Unix(ts, 0).UTC()
				}
			}
			if len(p.rels) == 0 {
				return errors.New("no releases")
			}
			return nil
		})
		slog.DebugContext(ctx, "elm package releases", "name", name, "releases", len(p.rels), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(p.rels, func(a, b datasource.Release) int {
			return semver.MustParse(a.Version).Compare(semver.MustParse(b.Version))
		})
		return p, nil
	})
}
