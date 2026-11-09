// Package galaxy lists the releases of Ansible content on Ansible Galaxy: roles from the v1
// API (datasource galaxy) and collections from the v3 API of a Galaxy server, Galaxy NG or
// Automation Hub (datasource galaxy-collection). Lookups run once per package and run and are
// shared by every requirements file.
package galaxy

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
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Datasource names (Renovate's).
const (
	Name           = "galaxy"
	CollectionName = "galaxy-collection"
)

// DefaultURL is Ansible Galaxy; DEPS_GALAXY_URL overrides it (a Galaxy NG server or mirror).
const DefaultURL = "https://galaxy.ansible.com"

// pageSize is the page size of v3 version lists (Galaxy NG's maximum is 100).
const pageSize = 100

// maxPages caps the pages of one version list; a longer list is an error, not a silently
// truncated result.
const maxPages = 100

// Roles is the galaxy datasource: roles by `namespace.name`.
type Roles struct {
	getter httpx.Getter
	base   string // server URL without a trailing slash

	roles datasource.Memo[string, *pkg]
}

// Collections is the galaxy-collection datasource: collections by `namespace.name` on the
// server of the dep's Source ("" = DEPS_GALAXY_URL or Ansible Galaxy).
type Collections struct {
	getter httpx.Getter
	base   string // default server URL without a trailing slash

	apis       datasource.Memo[string, string]    // server → v3 API root URL (with trailing slash)
	colls      datasource.Memo[[2]string, *pkg]   // (server, name)
	repos      datasource.Memo[[3]string, string] // (server, name, version) → metadata.repository
	deprecated sync.Map                           // (server, name) → true for deprecated collections
}

// pkg is the release list of a role or collection with publication times.
type pkg struct {
	rels     []datasource.Release
	times    map[string]time.Time
	repo     string // source repository of a role
	versions string // v3 versions URL of a collection (with trailing slash)
}

// New returns the galaxy (role) datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_GALAXY_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Roles {
	return &Roles{getter: newGetter(client, sem), base: baseURL(getenv)}
}

// NewCollections returns the galaxy-collection datasource, as New.
func NewCollections(client *http.Client, sem chan struct{}, getenv func(string) string) *Collections {
	return &Collections{getter: newGetter(client, sem), base: baseURL(getenv)}
}

func baseURL(getenv func(string) string) string {
	return strings.TrimSuffix(cmp.Or(getenv("DEPS_GALAXY_URL"), DefaultURL), "/")
}

// split returns the namespace and name of `namespace.name`.
func split(name string) (ns, n string, ok bool) {
	ns, n, ok = strings.Cut(name, ".")
	return ns, n, ok && ns != "" && n != "" && !strings.ContainsAny(n, "./") && !strings.Contains(ns, "/")
}

// Releases lists the versions of the role d.Name (`namespace.name`) that are version tags,
// oldest first. The result is shared and must not be modified.
func (s *Roles) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the version's `release_date` (else
// `created`); zero when unknown.
func (s *Roles) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// SourceRepo implements datasource.SourceRepo with the role's `github_user`/`github_repo`.
func (s *Roles) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return "", err
	}
	return p.repo, nil
}

// Digest is unsupported: Galaxy role versions are not digest-pinned.
func (*Roles) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// roleV1 is the part of `GET /api/v1/roles/` deps reads.
type roleV1 struct {
	Results []roleResult `json:"results"`
}

type roleResult struct {
	GithubUser    string `json:"github_user"`
	GithubRepo    string `json:"github_repo"`
	Username      string `json:"username"`
	SummaryFields struct {
		Versions []struct {
			Name        string `json:"name"`
			ReleaseDate string `json:"release_date"`
			Created     string `json:"created"`
		} `json:"versions"`
	} `json:"summary_fields"`
}

// lookup fetches `GET <base>/api/v1/roles/?owner__username=<ns>&name=<name>` once per role.
func (s *Roles) lookup(ctx context.Context, name string) (*pkg, error) {
	ns, n, ok := split(name)
	if !ok {
		return nil, fmt.Errorf("invalid Galaxy role name %q (want namespace.name)", name)
	}
	return s.roles.Do(ctx, name, func() (*pkg, error) {
		start := time.Now()
		u := s.base + "/api/v1/roles/?owner__username=" + url.QueryEscape(ns) + "&name=" + url.QueryEscape(n)
		var body roleV1
		err := get(ctx, s.getter, u, &body)
		slog.DebugContext(ctx, "galaxy role", "name", name, "results", len(body.Results), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		res := body.Results
		if len(res) > 1 { // Renovate: several roles of that name, keep the namespace's own
			res = slices.DeleteFunc(res, func(r roleResult) bool {
				return !strings.EqualFold(r.GithubUser, ns) && !strings.EqualFold(r.Username, ns)
			})
		}
		if len(res) == 0 {
			return nil, &dep.SkipError{Reason: "role not found on " + s.base}
		}
		p := &pkg{times: map[string]time.Time{}}
		r := res[0]
		if r.GithubUser != "" && r.GithubRepo != "" {
			p.repo = "https://github.com/" + r.GithubUser + "/" + r.GithubRepo
		}
		tag, _ := versioning.Get("tag")
		for _, v := range r.SummaryFields.Versions {
			if !tag.Valid(v.Name) {
				continue
			}
			p.rels = append(p.rels, datasource.Release{Version: v.Name})
			if t := parseTime(cmp.Or(v.ReleaseDate, v.Created)); !t.IsZero() {
				p.times[v.Name] = t
			}
		}
		if len(p.rels) == 0 {
			return nil, errors.New("galaxy role " + name + ": no version tags")
		}
		sortReleases(p.rels, tag)
		return p, nil
	})
}

// Releases lists the versions of the collection d.Name (`namespace.name`) on d.Source's
// server, oldest first. The result is shared and must not be modified.
func (s *Collections) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the version's `created_at`; zero when unknown.
func (s *Collections) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// Deprecation implements datasource.DeprecationSource with the collection's `deprecated` flag,
// as Releases fetched it.
func (s *Collections) Deprecation(d dep.Dep, _ string) string {
	if _, ok := s.deprecated.Load([2]string{s.server(d), d.Name}); ok {
		return "deprecated"
	}
	return ""
}

// SourceRepo implements datasource.SourceRepo with the `metadata.repository` of the version,
// one request per version.
func (s *Collections) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	srv := s.server(d)
	return s.repos.Do(ctx, [3]string{srv, d.Name, version}, func() (string, error) {
		var v struct {
			Metadata struct {
				Repository string `json:"repository"`
			} `json:"metadata"`
		}
		err := get(ctx, s.getter, p.versions+url.PathEscape(version)+"/", &v)
		return v.Metadata.Repository, err
	})
}

// Digest is unsupported: collection versions are not digest-pinned in requirements files.
func (*Collections) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(CollectionName, d)
}

// server is the Galaxy server of d: its Source, else the default.
func (s *Collections) server(d dep.Dep) string {
	return cmp.Or(strings.TrimSuffix(d.Source, "/"), s.base)
}

// api returns the v3 API root of server, discovered like ansible-galaxy does: the
// `available_versions.v3` of the API root, `<server>/api/` first unless server already names
// an API path (`…/api/automation-hub/content/published`), then server itself.
func (s *Collections) api(ctx context.Context, server string) (string, error) {
	return s.apis.Do(ctx, server, func() (string, error) {
		cands := []string{server + "/"}
		if !strings.Contains(server+"/", "/api/") {
			cands = []string{server + "/api/", server + "/"}
		}
		var errs []error
		for _, root := range cands {
			var body struct {
				Versions map[string]string `json:"available_versions"`
			}
			err := get(ctx, s.getter, root, &body)
			if err == nil && body.Versions["v3"] == "" {
				err = fmt.Errorf("GET %s: no v3 API", root)
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			return resolve(root, body.Versions["v3"])
		}
		return "", fmt.Errorf("galaxy server %s: %w", server, errors.Join(errs...))
	})
}

// lookup fetches the collection (`<v3>collections/<ns>/<name>/`, deprecation) and its version
// list (`…/versions/?limit=100`, following `links.next`) once per (server, collection).
func (s *Collections) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	ns, n, ok := split(d.Name)
	if !ok {
		return nil, fmt.Errorf("invalid Galaxy collection name %q (want namespace.name)", d.Name)
	}
	srv := s.server(d)
	return s.colls.Do(ctx, [2]string{srv, d.Name}, func() (*pkg, error) {
		start := time.Now()
		api, err := s.api(ctx, srv)
		if err != nil {
			return nil, err
		}
		base := api + "collections/" + url.PathEscape(ns) + "/" + url.PathEscape(n) + "/"
		p := &pkg{times: map[string]time.Time{}, versions: base + "versions/"}
		var coll struct {
			Deprecated bool `json:"deprecated"`
		}
		if err := get(ctx, s.getter, base, &coll); err != nil {
			if errors.Is(err, errNotFound) {
				return nil, &dep.SkipError{Reason: "collection not found on " + srv}
			}
			return nil, err
		}
		if coll.Deprecated {
			s.deprecated.Store([2]string{srv, d.Name}, true)
		}
		sv, _ := versioning.Get("semver")
		next := p.versions + fmt.Sprintf("?limit=%d", pageSize)
		seen := map[string]bool{}
		for next != "" {
			if seen[next] {
				return nil, fmt.Errorf("galaxy collection %s: pagination loop at %s", d.Name, next)
			}
			if len(seen) == maxPages {
				return nil, fmt.Errorf("galaxy collection %s: version listing exceeded %d pages", d.Name, maxPages)
			}
			seen[next] = true
			var page struct {
				Links struct {
					Next string `json:"next"`
				} `json:"links"`
				Data []struct {
					Version   string `json:"version"`
					CreatedAt string `json:"created_at"`
				} `json:"data"`
			}
			if err := get(ctx, s.getter, next, &page); err != nil {
				return nil, err
			}
			for _, v := range page.Data {
				if !sv.Valid(v.Version) {
					continue
				}
				p.rels = append(p.rels, datasource.Release{Version: v.Version})
				if t := parseTime(v.CreatedAt); !t.IsZero() {
					p.times[v.Version] = t
				}
			}
			if next = ""; page.Links.Next != "" {
				if next, err = resolve(api, page.Links.Next); err != nil {
					return nil, err
				}
			}
		}
		slog.DebugContext(ctx, "galaxy collection versions", "name", d.Name, "server", srv, "releases", len(p.rels), "duration", time.Since(start))
		if len(p.rels) == 0 {
			return nil, errors.New("galaxy collection " + d.Name + ": no versions")
		}
		sortReleases(p.rels, sv)
		return p, nil
	})
}

// errNotFound marks an HTTP 404.
var errNotFound = errors.New("not found")

// newGetter returns the Getter of the galaxy datasources: JSON, bodies up to maxBody.
func newGetter(client *http.Client, sem chan struct{}) httpx.Getter {
	accept := func(req *http.Request) error {
		req.Header.Set("Accept", "application/json")
		return nil
	}
	return httpx.Getter{Client: client, Sem: sem, Prepare: accept, MaxBody: maxBody}
}

// get GETs target and decodes the JSON body into v; a 404 is errNotFound.
func get(ctx context.Context, g httpx.Getter, target string, v any) error {
	err := g.Stream(ctx, target, func(r io.Reader) error { return json.UnmarshalRead(r, v) })
	if errors.Is(err, httpx.ErrNotFound) {
		return fmt.Errorf("GET %s: %w", httpx.Redact(target), errNotFound)
	}
	return err
}

// maxBody bounds a response body (a role's version list, a page of 100 collection versions).
const maxBody = 32 << 20

// resolve resolves ref (relative to the API root's host, as Galaxy's links are) against base.
func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

func sortReleases(rels []datasource.Release, s versioning.Scheme) {
	slices.SortStableFunc(rels, func(a, b datasource.Release) int { return s.Compare(a.Version, b.Version) })
}

// parseTime parses an RFC 3339 time; zero when malformed.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
