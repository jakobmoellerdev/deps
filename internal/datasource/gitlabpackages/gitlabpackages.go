// Package gitlabpackages lists the versions of packages in a GitLab project's package registry
// (Renovate's gitlab-packages datasource): one paginated packages API listing per project
// package and run.
package gitlabpackages

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
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "gitlab-packages"

// maxPages bounds the pages read per package (100 packages each).
const maxPages = 100

// Source is the gitlab-packages datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	auth   *httpx.Auth
	base   string // default GitLab instance URL without a trailing slash

	pkgs datasource.Memo[string, *pkg] // by API base + project + package
}

// pkg is the release list of a package and the creation time of each version.
type pkg struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the gitlab-packages datasource. base is the GitLab instance of deps without a
// registry URL (the session's GitLab URL); sem bounds concurrent requests (nil = unbounded);
// auth supplies the instance token (a `gitlab-ci-token` user is sent as JOB-TOKEN, others as
// PRIVATE-TOKEN).
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth, base string) *Source {
	return &Source{client: client, sem: sem, auth: auth, base: strings.TrimSuffix(cmp.Or(base, "https://gitlab.com"), "/")}
}

// Releases lists the versions of d.Name (`<project path or id>:<package name>`), oldest
// first, from the first registry of d.Source (whitespace-separated GitLab instance URLs,
// optionally ending in /api/v4; "" = the default instance) that has the package. The result
// is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the package's created_at; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// Digest is unsupported: registry packages are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// split returns the project and package of a `<project>:<package>` name.
func split(name string) (project, pkgName string, ok bool) {
	project, pkgName, ok = strings.Cut(name, ":")
	return project, pkgName, ok && project != "" && pkgName != ""
}

// apiBase is the /api/v4 URL of registry ("" = the default instance), as Renovate's
// getApiBaseUrl: a URL already ending in api/v4 is kept.
func (s *Source) apiBase(registry string) string {
	b := strings.TrimSuffix(cmp.Or(registry, s.base), "/")
	if strings.HasSuffix(b, "/api/v4") {
		return b
	}
	return b + "/api/v4"
}

// entry is one package of the packages API. Conan packages carry their name apart.
type entry struct {
	Name      string `json:"name"`
	ConanName string `json:"conan_package_name"`
	Version   string `json:"version"`
	CreatedAt string `json:"created_at"`
}

// lookup returns the package d.Name of the first registry of d.Source that lists a version
// of it; the errors of all registries when none does.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	project, name, ok := split(d.Name)
	if !ok {
		return nil, fmt.Errorf("invalid gitlab-packages name %q (want <project>:<package>)", d.Name)
	}
	registries := strings.Fields(d.Source)
	if len(registries) == 0 {
		registries = []string{""}
	}
	var errs []error
	for _, r := range registries {
		p, err := s.fetch(ctx, s.apiBase(r), project, name)
		if err == nil {
			return p, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// fetch lists `GET <api>/projects/<project>/packages?package_name=<name>&per_page=100` page
// by page (X-Next-Page), once per registry and package. package_name is a fuzzy match, so
// only entries whose name equals name are kept (as Renovate).
func (s *Source) fetch(ctx context.Context, api, project, name string) (*pkg, error) {
	return s.pkgs.Do(api+"\x00"+project+"\x00"+name, func() (*pkg, error) {
		start := time.Now()
		p := &pkg{times: map[string]time.Time{}}
		u := api + "/projects/" + url.PathEscape(project) + "/packages?package_name=" + url.QueryEscape(name) + "&per_page=100"
		var err error
		pages := 0
		for page := "1"; page != "" && err == nil && pages < maxPages; pages++ {
			page, err = s.page(ctx, u+"&page="+page, name, p)
		}
		slog.DebugContext(ctx, "gitlab packages", "registry", api, "project", project, "name", name, "pages", pages, "releases", len(p.rels), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		if len(p.rels) == 0 {
			return nil, fmt.Errorf("%s: no package %q in project %q", api, name, project)
		}
		slices.SortStableFunc(p.rels, func(a, b datasource.Release) int {
			return p.times[a.Version].Compare(p.times[b.Version])
		})
		return p, nil
	})
}

// page reads one page of u into p and returns the next page number ("" = last page). Without
// static credentials (httpx.Auth.Static), a 401, 403 or 404 asks the git credential helper once
// per host and retries with its answer: GitLab answers 404 for private projects.
func (s *Source) page(ctx context.Context, u, name string, p *pkg) (string, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	user, pass, authed := s.auth.Static(pu.Hostname())
	for {
		status, next, err := s.do(ctx, u, user, pass, authed, name, p)
		if authed || status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusNotFound {
			return next, err
		}
		if user, pass, authed = s.auth.Helper(ctx, pu.Host); !authed {
			return next, err
		}
	}
}

// do sends one GET of u, with the token as `Authorization: Bearer` when authed (`JOB-TOKEN`
// for the CI job token user gitlab-ci-token), and reads a 200 page into p. status is the HTTP
// status, 0 when no response arrived.
func (s *Source) do(ctx context.Context, u, user, pass string, authed bool, name string, p *pkg) (status int, next string, err error) {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return 0, "", err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.Header.Set("Accept", "application/json")
	switch {
	case !authed:
	case user == "gitlab-ci-token":
		req.Header.Set("JOB-TOKEN", pass)
	default:
		req.Header.Set("Authorization", "Bearer "+pass)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, "", fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	var entries []entry
	if err := json.UnmarshalRead(resp.Body, &entries); err != nil {
		return resp.StatusCode, "", fmt.Errorf("GET %s: %w", u, err)
	}
	for _, e := range entries {
		if cmp.Or(e.ConanName, e.Name) != name || e.Version == "" {
			continue
		}
		if _, seen := p.times[e.Version]; seen {
			continue
		}
		t, _ := time.Parse(time.RFC3339, e.CreatedAt)
		p.times[e.Version] = t.UTC()
		p.rels = append(p.rels, datasource.Release{Version: e.Version})
	}
	next = resp.Header.Get("X-Next-Page")
	if _, err := strconv.Atoi(next); next != "" && err != nil {
		return resp.StatusCode, "", errors.New("invalid X-Next-Page " + strconv.Quote(next))
	}
	return resp.StatusCode, next, nil
}
