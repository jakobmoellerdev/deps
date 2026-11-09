// Package jsr lists the versions of JSR packages (jsr.io, `jsr:@scope/name` specifiers of Deno
// configuration files) from the registry's package metadata: one request per package and run.
package jsr

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "jsr"

// DefaultURL is the JSR registry; JSR_URL, which the deno CLI reads too, overrides it.
const DefaultURL = "https://jsr.io"

// untimed is the release time of a version the registry lists without `createdAt`: JSR
// started recording publication times on 2025-09-18 and returns them for every version of a
// package published to since, so a package without any was last published before that day.
// Renovate assumes the same date.
var untimed = time.Date(2025, 9, 18, 0, 0, 0, 0, time.UTC)

// nameRe is a JSR package name: `@<scope>/<name>`, lower-case letters, digits and dashes.
var nameRe = regexp.MustCompile(`^@[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)

// ValidName reports whether name is a JSR package name (`@std/assert`).
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Source is the jsr datasource.
type Source struct {
	getter httpx.Getter
	base   string // registry base URL without a trailing slash

	pkgs datasource.Memo[string, *pkg] // by package name
}

// pkg is the version list of a package (oldest first), the publication time of each version,
// the yanked versions, the version the registry recommends and the package's GitHub repository.
type pkg struct {
	rels   []datasource.Release
	times  map[string]time.Time
	yanked map[string]bool
	latest string
	repo   string
}

// New returns the jsr datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies JSR_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, base: strings.TrimSuffix(cmp.Or(getenv("JSR_URL"), DefaultURL), "/")}
}

// Releases lists every version of the package d.Name that is not yanked, oldest first. The
// result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the `createdAt` time of version, or 2025-09-18
// for a version listed without one (untimed); zero for a version the registry does not list.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// Latest implements datasource.LatestSource with the registry's `latest` version.
func (s *Source) Latest(d dep.Dep) string {
	if p, ok := s.pkgs.Peek(d.Name); ok && p != nil {
		return p.latest
	}
	return ""
}

// Deprecation implements datasource.DeprecationSource: version is yanked.
func (s *Source) Deprecation(d dep.Dep, version string) string {
	if p, ok := s.pkgs.Peek(d.Name); ok && p != nil && p.yanked[version] {
		return "version " + version + " yanked"
	}
	return ""
}

// SourceRepo implements datasource.SourceRepo with the package's linked GitHub repository
// (`githubRepository`), "" without one.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	return p.repo, nil
}

// Digest is unsupported: JSR versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// response is the package metadata (`/@<scope>/<name>/meta.json`); other fields are skipped.
type response struct {
	Latest           string `json:"latest"`
	GitHubRepository *struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"githubRepository"`
	Versions map[string]struct {
		CreatedAt string `json:"createdAt"`
		Yanked    bool   `json:"yanked"`
	} `json:"versions"`
}

// lookup fetches `GET <base>/<name>/meta.json` once per package.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	if !ValidName(d.Name) {
		return nil, fmt.Errorf("invalid JSR package name %q", d.Name)
	}
	return s.pkgs.Do(ctx, d.Name, func() (*pkg, error) {
		start := time.Now()
		u := s.base + "/" + d.Name + "/meta.json"
		p := &pkg{times: map[string]time.Time{}, yanked: map[string]bool{}}
		err := s.getter.Stream(ctx, u, func(r io.Reader) error {
			var res response
			if err := json.UnmarshalRead(r, &res); err != nil {
				return err
			}
			for v, meta := range res.Versions {
				p.times[v] = untimed
				if t, err := time.Parse(time.RFC3339Nano, meta.CreatedAt); err == nil {
					p.times[v] = t.UTC()
				}
				if meta.Yanked {
					p.yanked[v] = true
					continue
				}
				p.rels = append(p.rels, datasource.Release{Version: v})
			}
			if len(p.rels) == 0 {
				return errors.New("no versions")
			}
			slices.SortFunc(p.rels, func(a, b datasource.Release) int { return compare(a.Version, b.Version) })
			p.latest = res.Latest
			if g := res.GitHubRepository; g != nil && g.Owner != "" && g.Name != "" {
				p.repo = "https://github.com/" + g.Owner + "/" + g.Name
			}
			return nil
		})
		slog.DebugContext(ctx, "jsr package versions", "url", u, "versions", len(p.rels), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		return p, nil
	})
}

// compare orders versions as semver, ones that are no semver after the others by text.
func compare(a, b string) int {
	va, ea := semver.StrictNewVersion(a)
	vb, eb := semver.StrictNewVersion(b)
	switch {
	case ea == nil && eb == nil:
		return va.Compare(vb)
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}
