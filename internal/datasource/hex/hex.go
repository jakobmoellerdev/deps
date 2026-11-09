// Package hex lists the releases of Hex packages (Elixir, Erlang and Gleam) from the hex.pm
// API: one request per package and run, shared by every mix.exs and gleam.toml.
package hex

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
const Name = "hex"

// DefaultURL is the hex.pm API; DEPS_HEX_URL overrides it (a mirror serving its API).
const DefaultURL = "https://hex.pm"

// nameRe matches a Hex package name, optionally with the organization of a private package
// (`name:organization`, Renovate's packageName form).
var nameRe = regexp.MustCompile(`^([a-z][a-z0-9_]*)(?::([a-z0-9_]+))?$`)

// Source is the hex datasource.
type Source struct {
	getter httpx.Getter
	base   string // API base URL without a trailing slash

	pkgs datasource.Memo[string, *pkg] // by package name (with organization)
}

// pkg is the release list of a package with the publication time and retirement of each
// version.
type pkg struct {
	rels    []datasource.Release
	times   map[string]time.Time
	retired map[string]string // version → why
	repo    string            // the GitHub link of the package metadata
}

// New returns the hex datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_HEX_URL and HEX_API_KEY.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	key := getenv("HEX_API_KEY")
	prepare := func(req *http.Request) error {
		req.Header.Set("Accept", "application/json")
		if key != "" {
			req.Header.Set("Authorization", key) // hex.pm takes API keys as is
		}
		return nil
	}
	return &Source{getter: httpx.Getter{Client: client, Sem: sem, Prepare: prepare}, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_HEX_URL"), DefaultURL), "/")}
}

// Releases lists every published version of the package d.Name (`name`, or
// `name:organization` for a package of a private organization), oldest first; retired versions
// are listed (Deprecation names them). The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the `inserted_at` time of version; zero when
// unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// Deprecation implements datasource.DeprecationSource: version is retired (`mix hex.retire`)
// in the package Releases fetched.
func (s *Source) Deprecation(d dep.Dep, version string) string {
	p, err := s.lookup(context.Background(), d.Name) // answered by the earlier Releases call
	if err != nil {
		return ""
	}
	if why, ok := p.retired[version]; ok {
		return "version " + version + " retired" + why
	}
	return ""
}

// SourceRepo implements datasource.SourceRepo with the package's `GitHub` link (any case).
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return "", err
	}
	return p.repo, nil
}

// Digest is unsupported: Hex package versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// response is the part of `GET /api/packages/<name>` deps reads.
type response struct {
	Meta struct {
		Links map[string]string `json:"links"`
	} `json:"meta"`
	Releases []struct {
		Version    string `json:"version"`
		InsertedAt string `json:"inserted_at"`
	} `json:"releases"`
	Retirements map[string]struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"retirements"`
}

// lookup fetches `GET <base>/api/packages/<name>` (`/api/repos/<organization>/packages/<name>`
// for a private package) once per package.
func (s *Source) lookup(ctx context.Context, name string) (*pkg, error) {
	m := nameRe.FindStringSubmatch(name)
	if m == nil {
		return nil, fmt.Errorf("invalid Hex package name %q (want name or name:organization)", name)
	}
	return s.pkgs.Do(ctx, name, func() (*pkg, error) {
		start := time.Now()
		u := s.base + "/api/packages/" + url.PathEscape(m[1])
		if m[2] != "" {
			u = s.base + "/api/repos/" + url.PathEscape(m[2]) + "/packages/" + url.PathEscape(m[1])
		}
		var r response
		err := s.get(ctx, u, func(body io.Reader) error { return json.UnmarshalRead(body, &r) })
		slog.DebugContext(ctx, "hex package releases", "name", name, "releases", len(r.Releases), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		p := &pkg{times: map[string]time.Time{}, retired: map[string]string{}}
		vs := map[string]*semver.Version{}
		for _, rel := range r.Releases {
			v, err := semver.StrictNewVersion(rel.Version)
			if err != nil || vs[rel.Version] != nil {
				continue
			}
			vs[rel.Version] = v
			p.rels = append(p.rels, datasource.Release{Version: rel.Version})
			if t, err := time.Parse(time.RFC3339Nano, rel.InsertedAt); err == nil {
				p.times[rel.Version] = t.UTC()
			}
		}
		if len(p.rels) == 0 {
			return nil, fmt.Errorf("GET %s: no releases", u)
		}
		slices.SortFunc(p.rels, func(a, b datasource.Release) int { return vs[a.Version].Compare(vs[b.Version]) })
		for v, ret := range r.Retirements {
			why := ""
			if ret.Reason != "" {
				why = " (" + ret.Reason
				if ret.Message != "" {
					why += ": " + ret.Message
				}
				why += ")"
			}
			p.retired[v] = why
		}
		for k, v := range r.Meta.Links {
			if strings.EqualFold(k, "github") {
				p.repo = v
			}
		}
		return p, nil
	})
}

// errNotFound is the error of a package the API does not have (or the key cannot see).
var errNotFound = errors.New("package not found")

// get GETs target and passes the body to read; a 404 is errNotFound.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) error {
	err := s.getter.Stream(ctx, target, read)
	if errors.Is(err, httpx.ErrNotFound) {
		return fmt.Errorf("GET %s: %w", httpx.Redact(target), errNotFound)
	}
	return err
}
