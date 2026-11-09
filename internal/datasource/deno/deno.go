// Package deno lists the versions of modules of the deno.land/x registry and of the
// deno.land/std standard library (`https://deno.land/x/<module>@<version>/…` URLs in Deno
// configuration files) from the registry's CDN metadata: one request per module and run, and
// one per version whose upload time or source repository is asked for.
package deno

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
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "deno"

// DefaultURL is the deno.land registry's CDN; DEPS_DENO_URL overrides it.
const DefaultURL = "https://cdn.deno.land"

// Prefix starts the dep names: `https://deno.land/x/<module>` or `https://deno.land/std`, as
// in Renovate.
const Prefix = "https://deno.land/"

// moduleRe is a deno.land/x module name (or `std`).
var moduleRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// Module returns the registry module of the dep name (`https://deno.land/x/oak` → oak,
// `https://deno.land/std` → std); ok is false for any other name.
func Module(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, Prefix)
	if !ok {
		return "", false
	}
	if rest != "std" {
		if rest, ok = strings.CutPrefix(rest, "x/"); !ok {
			return "", false
		}
	}
	return rest, moduleRe.MatchString(rest)
}

// Source is the deno datasource.
type Source struct {
	getter httpx.Getter
	base   string // CDN base URL without a trailing slash

	mods  datasource.Memo[string, *module]      // by module
	metas datasource.Memo[string, *versionMeta] // by module + "\x00" + version
}

// module is the version list of a module (newest first, as the registry lists them) and the
// version the registry calls the latest.
type module struct {
	rels   []datasource.Release
	latest string
}

// versionMeta is the upload time and source repository of one version.
type versionMeta struct {
	at   time.Time
	repo string
}

// New returns the deno datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_DENO_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_DENO_URL"), DefaultURL), "/")}
}

// Releases lists every version of the module of d.Name as the registry lists them (newest
// first). The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	m, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return m.rels, nil
}

// Latest implements datasource.LatestSource with the registry's `latest` version.
func (s *Source) Latest(d dep.Dep) string {
	mod, _ := Module(d.Name)
	if m, ok := s.mods.Peek(mod); ok && m != nil {
		return m.latest
	}
	return ""
}

// ReleaseTime implements datasource.Timer with the `uploaded_at` time of version's metadata;
// zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	m, err := s.meta(ctx, d, version)
	if err != nil {
		return time.Time{}, err
	}
	return m.at, nil
}

// SourceRepo implements datasource.SourceRepo with the GitHub repository the latest version
// was published from (`upload_options`), "" for other upload types.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	m, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	meta, err := s.meta(ctx, d, m.latest)
	if err != nil {
		return "", err
	}
	return meta.repo, nil
}

// Digest is unsupported: deno.land versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lookup fetches `GET <base>/<module>/meta/versions.json` once per module.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*module, error) {
	mod, ok := Module(d.Name)
	if !ok {
		return nil, fmt.Errorf("invalid deno.land module %q", d.Name)
	}
	return s.mods.Do(ctx, mod, func() (*module, error) {
		start := time.Now()
		u := s.base + "/" + mod + "/meta/versions.json"
		m := &module{}
		err := s.getter.Stream(ctx, u, func(r io.Reader) error {
			var res struct {
				Latest   string   `json:"latest"`
				Versions []string `json:"versions"`
			}
			if err := json.UnmarshalRead(r, &res); err != nil {
				return err
			}
			for _, v := range res.Versions {
				if v != "" {
					m.rels = append(m.rels, datasource.Release{Version: v})
				}
			}
			if len(m.rels) == 0 {
				return errors.New("no versions")
			}
			m.latest = cmp.Or(res.Latest, m.rels[0].Version)
			return nil
		})
		slog.DebugContext(ctx, "deno.land module versions", "url", u, "versions", len(m.rels), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		return m, nil
	})
}

// meta fetches `GET <base>/<module>/versions/<version>/meta/meta.json` once per version.
func (s *Source) meta(ctx context.Context, d dep.Dep, version string) (*versionMeta, error) {
	mod, ok := Module(d.Name)
	if !ok {
		return nil, fmt.Errorf("invalid deno.land module %q", d.Name)
	}
	if version == "" || strings.ContainsAny(version, "/?#") {
		return nil, fmt.Errorf("invalid version %q of deno.land module %s", version, mod)
	}
	return s.metas.Do(ctx, mod+"\x00"+version, func() (*versionMeta, error) {
		u := s.base + "/" + mod + "/versions/" + url.PathEscape(version) + "/meta/meta.json"
		m := &versionMeta{}
		err := s.getter.Stream(ctx, u, func(r io.Reader) error {
			var res struct {
				UploadedAt    string `json:"uploaded_at"`
				UploadOptions struct {
					Type       string `json:"type"`
					Repository string `json:"repository"`
				} `json:"upload_options"`
			}
			if err := json.UnmarshalRead(r, &res); err != nil {
				return err
			}
			if t, err := time.Parse(time.RFC3339Nano, res.UploadedAt); err == nil {
				m.at = t.UTC()
			}
			if o := res.UploadOptions; o.Type == "github" && strings.Count(o.Repository, "/") == 1 {
				m.repo = "https://github.com/" + o.Repository
			}
			return nil
		})
		slog.DebugContext(ctx, "deno.land version metadata", "url", u, "err", err)
		if err != nil {
			return nil, err
		}
		return m, nil
	})
}
