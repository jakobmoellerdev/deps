// Package orb lists the versions of CircleCI orbs from the CircleCI orb registry.
package orb

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "orb"

// DefaultURL is the CircleCI host; DEPS_CIRCLECI_URL overrides it (CircleCI server).
const DefaultURL = "https://circleci.com"

// Source looks up each orb once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	base   string // without trailing slash
	token  string

	orbs datasource.Memo[string, *orb]
}

// orb is the published versions of an orb: newest first as listed, then a derived "X.Y" and "X"
// per release line, so partial versions (`circleci/node@5`) have candidates of their precision
// under tag versioning.
type orb struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the orb datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_CIRCLECI_URL and DEPS_CIRCLECI_TOKEN (private orbs).
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{
		client: client, sem: sem, token: getenv("DEPS_CIRCLECI_TOKEN"),
		base: strings.TrimSuffix(cmp.Or(getenv("DEPS_CIRCLECI_URL"), DefaultURL), "/"),
	}
}

// Releases lists the versions of the orb d.Name (`<namespace>/<name>`).
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	o, err := s.lookup(ctx, d.Name)
	if err != nil {
		return nil, err
	}
	return o.rels, nil
}

// ReleaseTime implements datasource.Timer with the version's `created_at`; a derived "X.Y"/"X"
// has the time of the line's first release. Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	o, err := s.lookup(ctx, d.Name)
	if err != nil {
		return time.Time{}, err
	}
	return o.times[version], nil
}

// Digest is unsupported: orbs are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// packages is the response of GET /api/v3/orb/packages?filter[name]=<name>.
type packages struct {
	Data []struct {
		Attributes struct {
			Name string `json:"name"`
		} `json:"attributes"`
		References struct {
			OrbVersions []struct {
				Attributes struct {
					Version   string `json:"version"`
					CreatedAt string `json:"created_at"`
				} `json:"attributes"`
			} `json:"orb_versions"`
		} `json:"references"`
	} `json:"data"`
}

func (s *Source) lookup(ctx context.Context, name string) (*orb, error) {
	return s.orbs.Do(ctx, name, func() (*orb, error) {
		target := s.base + "/api/v3/orb/packages?" + url.Values{"filter[name]": {name}}.Encode()
		var resp packages
		if err := s.get(ctx, target, func(r io.Reader) error { return json.UnmarshalRead(r, &resp) }); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			if p.Attributes.Name != name {
				continue
			}
			o := &orb{times: map[string]time.Time{}}
			var derived []datasource.Release
			for _, v := range p.References.OrbVersions {
				ver := v.Attributes.Version
				if ver == "" {
					continue
				}
				o.rels = append(o.rels, datasource.Release{Version: ver})
				t, err := time.Parse(time.RFC3339, v.Attributes.CreatedAt)
				if err == nil {
					o.times[ver] = t
				}
				if strings.ContainsAny(ver, "-+") || strings.Count(ver, ".") != 2 {
					continue
				}
				for _, line := range []string{ver[:strings.LastIndexByte(ver, '.')], ver[:strings.IndexByte(ver, '.')]} {
					if _, seen := o.times[line]; !seen {
						derived = append(derived, datasource.Release{Version: line})
					}
					if err == nil || o.times[line].IsZero() {
						o.times[line] = t // newest first: the last write is the line's first release
					}
				}
			}
			if len(o.rels) == 0 {
				return nil, fmt.Errorf("orb %s has no versions", name)
			}
			o.rels = append(o.rels, derived...)
			return o, nil
		}
		return nil, fmt.Errorf("orb %s not found", name)
	})
}

// get GETs target with the CircleCI token, if any, and passes the body of a 200 response to
// read.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) error {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	if s.token != "" {
		// Circle-Token is CircleCI's API token header; Renovate sends the bare token as
		// Authorization (hostRules authType Token-Only).
		req.Header.Set("Circle-Token", s.token)
		req.Header.Set("Authorization", s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("GET %s: malformed response: %w", target, err)
	}
	return nil
}
