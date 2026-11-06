// Package forge lists the releases and tags of GitLab, Gitea and Forgejo repositories through
// their REST APIs: the gitlab-releases, gitlab-tags, gitea-releases, gitea-tags,
// forgejo-releases and forgejo-tags datasources (Renovate's names and defaults).
package forge

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Datasource names.
const (
	GitLabReleases  = "gitlab-releases"
	GitLabTags      = "gitlab-tags"
	GiteaReleases   = "gitea-releases"
	GiteaTags       = "gitea-tags"
	ForgejoReleases = "forgejo-releases"
	ForgejoTags     = "forgejo-tags"
)

// Names are the forge datasource names.
var Names = []string{ForgejoReleases, ForgejoTags, GiteaReleases, GiteaTags, GitLabReleases, GitLabTags}

// Default registry URLs, used when a dep names neither a registry nor a repository URL.
const (
	DefaultGitLabURL  = "https://gitlab.com"
	DefaultGiteaURL   = "https://gitea.com"
	DefaultForgejoURL = "https://code.forgejo.org"
)

// maxPages bounds the pages of one list: 1000 GitLab entries (100 per page), 500 Gitea and
// Forgejo entries (50 per page, Gitea's default MAX_RESPONSE_ITEMS). Lists are newest first.
const maxPages = 10

// spec describes one datasource.
type spec struct {
	gitlab     bool // GitLab API v4, else Gitea/Forgejo API v1
	tags       bool // the tag list, else the release list
	defaultURL string
}

func specOf(name string) (spec, bool) {
	switch name {
	case GitLabReleases:
		return spec{gitlab: true, defaultURL: DefaultGitLabURL}, true
	case GitLabTags:
		return spec{gitlab: true, tags: true, defaultURL: DefaultGitLabURL}, true
	case GiteaReleases:
		return spec{defaultURL: DefaultGiteaURL}, true
	case GiteaTags:
		return spec{tags: true, defaultURL: DefaultGiteaURL}, true
	case ForgejoReleases:
		return spec{defaultURL: DefaultForgejoURL}, true
	case ForgejoTags:
		return spec{tags: true, defaultURL: DefaultForgejoURL}, true
	}
	return spec{}, false
}

// Repo is a repository of a forge datasource.
type Repo struct {
	Base string // registry URL without trailing slash, e.g. https://gitlab.example.com/gitlab
	Path string // project path: `owner/repo` (GitLab: `group/subgroup/project`)
}

// URL returns the repository's web URL.
func (r Repo) URL() string { return r.Base + "/" + r.Path }

// ParseRepo resolves the repository of a dep of datasource name (one of Names): name is the
// project path, or a repository URL `http(s)://host/path` whose host is the registry;
// registry is the first of the whitespace-separated registry URLs (dep.Dep.Source), else the
// datasource's default. Gitea and Forgejo paths are `owner/repo`, GitLab paths have two or more
// segments.
func ParseRepo(name, depName, registry string) (Repo, error) {
	sp, ok := specOf(name)
	if !ok {
		return Repo{}, fmt.Errorf("unknown forge datasource %q", name)
	}
	invalid := fmt.Errorf("invalid %s name %q", name, depName)
	base, path := sp.defaultURL, depName
	if f := strings.Fields(registry); len(f) > 0 {
		base = f[0]
	}
	if strings.Contains(depName, "://") {
		u, err := url.Parse(depName)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Repo{}, invalid
		}
		base, path = u.Scheme+"://"+u.Host, strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Repo{}, fmt.Errorf("invalid %s registry URL %q", name, base)
	}
	path = strings.Trim(path, "/")
	segs := strings.Split(path, "/")
	if len(segs) < 2 || !sp.gitlab && len(segs) != 2 {
		return Repo{}, invalid
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return Repo{}, invalid
		}
	}
	return Repo{Base: strings.TrimRight(base, "/"), Path: path}, nil
}

// Source is one forge datasource.
type Source struct {
	name   string
	spec   spec
	client *http.Client
	sem    chan struct{}
	auth   *httpx.Auth
	lists  datasource.Memo[Repo, *list]
}

// New returns the datasource name (one of Names) on client, holding a slot of sem per request
// and sending auth's credentials for the registry host.
func New(name string, client *http.Client, sem chan struct{}, auth *httpx.Auth) *Source {
	sp, ok := specOf(name)
	if !ok {
		panic("forge: unknown datasource " + name)
	}
	return &Source{name: name, spec: sp, client: client, sem: sem, auth: auth}
}

// list is a fetched release or tag list, newest first.
type list struct {
	rels    []datasource.Release
	times   map[string]time.Time
	digests map[string]string // tags: commit of each tag
}

// entry is one element of a release or tag list (GitLab v4, Gitea/Forgejo v1).
type entry struct {
	TagName     string    `json:"tag_name"`         // releases
	Name        string    `json:"name"`             // tags
	ReleasedAt  time.Time `json:"released_at"`      // GitLab releases
	Upcoming    bool      `json:"upcoming_release"` // GitLab releases
	PublishedAt time.Time `json:"published_at"`     // Gitea releases
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Commit      struct {
		ID        string    `json:"id"`         // GitLab
		CreatedAt time.Time `json:"created_at"` // GitLab
		SHA       string    `json:"sha"`        // Gitea
		Created   time.Time `json:"created"`    // Gitea
	} `json:"commit"`
}

// version returns the entry's tag, commit and time, or ok=false for an entry that is not a
// published stable release (a draft, prerelease or upcoming release).
func (s *Source) version(e entry) (tag, commit string, at time.Time, ok bool) {
	switch {
	case s.spec.tags && s.spec.gitlab:
		return e.Name, e.Commit.ID, e.Commit.CreatedAt, e.Name != ""
	case s.spec.tags:
		return e.Name, e.Commit.SHA, e.Commit.Created, e.Name != ""
	case s.spec.gitlab:
		return e.TagName, "", e.ReleasedAt, e.TagName != "" && !e.Upcoming
	}
	return e.TagName, "", e.PublishedAt, e.TagName != "" && !e.Draft && !e.Prerelease
}

// Releases lists the releases (tags) of d's repository (see ParseRepo), newest first; release
// lists drop drafts, prereleases and upcoming releases. Tag releases carry the tag's commit as
// Digest. The slice must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	l, err := s.list(ctx, d)
	if err != nil {
		return nil, err
	}
	return l.rels, nil
}

// ReleaseTime implements datasource.Timer: the release's release (GitLab `released_at`) or
// publication time (Gitea `published_at`), the commit time of a tag; zero when version is not
// listed.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	l, err := s.list(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return l.times[version], nil
}

// Digest returns the commit of tag version for the tags datasources, looking the tag up on its
// own when it is older than the listed pages; release lists carry no digest.
func (s *Source) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	if !s.spec.tags {
		return "", nil
	}
	l, err := s.list(ctx, d)
	if err != nil {
		return "", err
	}
	if c, ok := l.digests[version]; ok {
		return c, nil
	}
	r, err := ParseRepo(s.name, d.Name, d.Source)
	if err != nil {
		return "", err
	}
	var e entry
	status, _, err := s.get(ctx, s.api(r, "/"+url.PathEscape(version)), func(body io.Reader) error {
		return json.UnmarshalRead(body, &e)
	})
	if status == http.StatusNotFound {
		return "", fmt.Errorf("tag %s %w", version, datasource.ErrVersionNotFound)
	}
	if err != nil {
		return "", err
	}
	_, commit, _, _ := s.version(e)
	return commit, nil
}

func (s *Source) list(ctx context.Context, d dep.Dep) (*list, error) {
	r, err := ParseRepo(s.name, d.Name, d.Source)
	if err != nil {
		return nil, err
	}
	return s.lists.Do(r, func() (*list, error) { return s.fetch(ctx, r) })
}

// api returns the URL of the repository's release or tag list, plus suffix (a single tag).
func (s *Source) api(r Repo, suffix string) string {
	kind := "/releases"
	if s.spec.tags {
		kind = "/tags"
	}
	if s.spec.gitlab {
		if s.spec.tags {
			kind = "/repository/tags"
		}
		return r.Base + "/api/v4/projects/" + url.PathEscape(r.Path) + kind + suffix
	}
	owner, repo, _ := strings.Cut(r.Path, "/")
	return r.Base + "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + kind + suffix
}

// fetch reads up to maxPages pages of the list of r.
func (s *Source) fetch(ctx context.Context, r Repo) (*list, error) {
	l := &list{times: map[string]time.Time{}}
	if s.spec.tags {
		l.digests = map[string]string{}
	}
	query := "?limit=50&page="
	if s.spec.gitlab {
		query = "?per_page=100&page="
	} else if !s.spec.tags {
		query = "?draft=false&limit=50&page="
	}
	for page := 1; page <= maxPages; page++ {
		n := 0
		status, next, err := s.get(ctx, s.api(r, query+strconv.Itoa(page)), func(body io.Reader) error {
			return datasource.DecodeArray(body, func(e entry) {
				n++
				tag, commit, at, ok := s.version(e)
				if !ok {
					return
				}
				if _, dup := l.times[tag]; dup {
					return
				}
				l.rels = append(l.rels, datasource.Release{Version: tag, Digest: commit})
				l.times[tag] = at
				if l.digests != nil {
					l.digests[tag] = commit
				}
			})
		})
		if status == http.StatusNotFound {
			return nil, fmt.Errorf("%s: repository %s not found (or not visible with the credentials for its host)", s.name, r.URL())
		}
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", s.name, r.URL(), err)
		}
		if n == 0 || !next {
			break
		}
	}
	return l, nil
}

// get GETs target with the credentials of its host and passes a 200 body to read. next
// reports a next page (a Link rel="next"). Without static credentials (httpx.Auth.Static), a
// 401, 403 or 404 asks the git credential helper once per host and retries with its answer:
// forges answer 404 for private repositories. status is the final HTTP status, 0 when no
// response arrived.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) (status int, next bool, err error) {
	u, err := url.Parse(target)
	if err != nil {
		return 0, false, err
	}
	user, pass, authed := s.auth.Static(u.Hostname())
	for {
		status, next, err = s.do(ctx, target, user, pass, authed, read)
		if authed || status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusNotFound {
			return status, next, err
		}
		if user, pass, authed = s.auth.Helper(ctx, u.Host); !authed {
			return status, next, err
		}
	}
}

// do sends one GET of target, with user/pass when authed: GitLab gets the token as
// `Authorization: Bearer` (`JOB-TOKEN` for the CI job token user gitlab-ci-token), Gitea and
// Forgejo as Basic credentials.
func (s *Source) do(ctx context.Context, target, user, pass string, authed bool, read func(io.Reader) error) (int, bool, error) {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return 0, false, err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.Header.Set("Accept", "application/json")
	switch {
	case !authed:
	case !s.spec.gitlab:
		req.SetBasicAuth(user, pass)
	case user == "gitlab-ci-token":
		req.Header.Set("JOB-TOKEN", pass)
	default:
		req.Header.Set("Authorization", "Bearer "+pass)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false, fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	next := strings.Contains(resp.Header.Get("Link"), `rel="next"`)
	if err := read(resp.Body); err != nil {
		return resp.StatusCode, false, fmt.Errorf("GET %s: %w", target, err)
	}
	return resp.StatusCode, next, nil
}
