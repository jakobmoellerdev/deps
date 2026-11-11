// Package forge lists the releases and tags of GitLab, Gitea and Forgejo repositories through
// their REST APIs: the gitlab-releases, gitlab-tags, gitea-releases, gitea-tags,
// forgejo-releases and forgejo-tags datasources (Renovate's names and defaults).
package forge

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	invalid := func() error { return fmt.Errorf("invalid %s name %q", name, depName) }
	base, path := sp.defaultURL, depName
	if f := strings.Fields(registry); len(f) > 0 {
		base = f[0]
	}
	if strings.Contains(depName, "://") {
		u, err := url.Parse(depName)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Repo{}, invalid()
		}
		p := strings.Trim(u.Path, "/")
		if sp.gitlab {
			// A copied browser URL (…/group/proj/-/releases): GitLab reserves "-" as the
			// separator between the project path and its pages.
			if i := strings.Index(p+"/", "/-/"); i >= 0 {
				p = p[:i]
			}
		}
		base, path = u.Scheme+"://"+u.Host, strings.TrimSuffix(p, ".git")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Repo{}, fmt.Errorf("invalid %s registry URL %q", name, base)
	}
	path = strings.Trim(path, "/")
	segs := strings.Split(path, "/")
	if len(segs) < 2 || !sp.gitlab && len(segs) != 2 {
		return Repo{}, invalid()
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return Repo{}, invalid()
		}
	}
	return Repo{Base: strings.TrimRight(base, "/"), Path: path}, nil
}

// Source is one forge datasource.
type Source struct {
	name   string
	spec   spec
	getter httpx.Getter
	auth   *httpx.Auth
	lists  datasource.Memo[Repo, *list]
	repos  datasource.Memo[Repo, bool] // repository -> archived
}

// New returns the datasource name (one of Names) on client, holding a slot of sem per request
// and sending auth's credentials for the registry host.
func New(name string, client *http.Client, sem chan struct{}, auth *httpx.Auth) *Source {
	sp, ok := specOf(name)
	if !ok {
		panic("forge: unknown datasource " + name)
	}
	return &Source{name: name, spec: sp, getter: httpx.Getter{Client: client, Sem: sem}, auth: auth}
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

// Deprecation implements datasource.DeprecationSource: d's repository is archived, as the
// repository request Releases sent alongside the list reported (see Archived); no request.
func (s *Source) Deprecation(d dep.Dep, _ string) string {
	r, err := ParseRepo(s.name, d.Name, d.Source)
	if err != nil {
		return ""
	}
	if archived, _ := s.repos.Peek(r); archived {
		return "repository archived"
	}
	return ""
}

// Archived reports whether d's repository is archived: GitLab
// `GET /api/graphql?query={project(fullPath:<path>){archived}}` (the REST project of an
// anonymous request has no `archived`), Gitea/Forgejo `GET /api/v1/repos/<owner>/<repo>`,
// `archived`. One request per repository, shared with Releases (which sends it alongside the
// list) and Deprecation; a project GitLab does not show is not archived.
func (s *Source) Archived(ctx context.Context, d dep.Dep) (bool, error) {
	r, err := ParseRepo(s.name, d.Name, d.Source)
	if err != nil {
		return false, err
	}
	return s.archived(ctx, r)
}

// gitlabArchivedQuery asks GitLab's GraphQL API whether the project $p is archived.
const gitlabArchivedQuery = `query($p:ID!){project(fullPath:$p){archived}}`

func (s *Source) archived(ctx context.Context, r Repo) (bool, error) {
	return s.repos.Do(ctx, r, func() (bool, error) {
		var target string
		if s.spec.gitlab {
			vars, err := json.Marshal(map[string]string{"p": r.Path})
			if err != nil {
				return false, err
			}
			target = r.Base + "/api/graphql?query=" + url.QueryEscape(gitlabArchivedQuery) + "&variables=" + url.QueryEscape(string(vars))
		} else {
			owner, repo, _ := strings.Cut(r.Path, "/")
			target = r.Base + "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
		}
		var meta struct {
			Archived bool `json:"archived"` // Gitea/Forgejo
			Data     struct {
				Project struct {
					Archived bool `json:"archived"`
				} `json:"project"`
			} `json:"data"` // GitLab
		}
		_, err := s.get(ctx, target, func(body io.Reader) error { return json.UnmarshalRead(body, &meta) })
		archived := meta.Archived || meta.Data.Project.Archived
		slog.DebugContext(ctx, "forge repository", "datasource", s.name, "repo", r.URL(), "archived", archived, "err", err)
		return archived, err
	})
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
	_, err = s.get(ctx, s.api(r, "/"+url.PathEscape(version)), func(body io.Reader) error {
		return json.UnmarshalRead(body, &e)
	})
	if errors.Is(err, httpx.ErrNotFound) {
		return "", fmt.Errorf("tag %s %w", version, datasource.ErrVersionNotFound)
	}
	if err != nil {
		return "", err
	}
	_, commit, _, _ := s.version(e)
	return commit, nil
}

// CommitTime returns the committer date of commit in d's repository (GitLab
// `GET /projects/<path>/repository/commits/<sha>`, `committed_date`; Gitea/Forgejo
// `GET /repos/<owner>/<repo>/git/commits/<sha>`, `created`); zero when the commit is not found.
// Callers dedupe.
func (s *Source) CommitTime(ctx context.Context, d dep.Dep, commit string) (time.Time, error) {
	r, err := ParseRepo(s.name, d.Name, d.Source)
	if err != nil {
		return time.Time{}, err
	}
	var target string
	if s.spec.gitlab {
		target = r.Base + "/api/v4/projects/" + url.PathEscape(r.Path) + "/repository/commits/" + url.PathEscape(commit)
	} else {
		owner, repo, _ := strings.Cut(r.Path, "/")
		target = r.Base + "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git/commits/" +
			url.PathEscape(commit) + "?stat=false&verification=false&files=false"
	}
	var c struct {
		CommittedDate time.Time `json:"committed_date"` // GitLab
		Created       time.Time `json:"created"`        // Gitea
	}
	_, err = s.get(ctx, target, func(body io.Reader) error { return json.UnmarshalRead(body, &c) })
	if errors.Is(err, httpx.ErrNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if s.spec.gitlab {
		return c.CommittedDate, nil
	}
	return c.Created, nil
}

func (s *Source) list(ctx context.Context, d dep.Dep) (*list, error) {
	r, err := ParseRepo(s.name, d.Name, d.Source)
	if err != nil {
		return nil, err
	}
	return s.lists.Do(ctx, r, func() (*list, error) {
		// The repository's archived flag (Deprecation) is asked alongside the first page.
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = s.archived(ctx, r) })
		l, err := s.fetch(ctx, r)
		wg.Wait()
		return l, err
	})
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
		next, err := s.get(ctx, s.api(r, query+strconv.Itoa(page)), func(body io.Reader) error {
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
		if errors.Is(err, httpx.ErrNotFound) {
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

// get GETs target with the credentials of its host and passes the body to read. next reports
// a next page (a Link rel="next"). Without static credentials (httpx.Auth.Static), a 401, 403
// or 404 asks the git credential helper once per host and retries with its answer: forges
// answer 404 for private repositories.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) (next bool, err error) {
	u, err := url.Parse(target)
	if err != nil {
		return false, err
	}
	user, pass, authed := s.auth.Static(u.Hostname())
	for {
		next, err = s.do(ctx, target, user, pass, authed, read)
		if authed || !denied(err) {
			return next, err
		}
		if user, pass, authed = s.auth.Helper(ctx, u.Host); !authed {
			return next, err
		}
	}
}

// denied reports whether err is an HTTP 401, 403 or 404.
func denied(err error) bool {
	se, ok := errors.AsType[*httpx.StatusError](err)
	return ok && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden || se.Code == http.StatusNotFound)
}

// do sends one GET of target, with user/pass when authed: GitLab gets the token as
// `Authorization: Bearer` (`JOB-TOKEN` for the CI job token user gitlab-ci-token), Gitea and
// Forgejo as Basic credentials.
func (s *Source) do(ctx context.Context, target, user, pass string, authed bool, read func(io.Reader) error) (bool, error) {
	g := s.getter
	g.Prepare = func(req *http.Request) error {
		req.Header.Set("Accept", "application/json")
		switch {
		case !authed:
		case !s.spec.gitlab:
			httpx.SetAuth(req, user, pass)
		case user == "gitlab-ci-token":
			req.Header.Set("JOB-TOKEN", pass)
		default:
			req.Header.Set("Authorization", "Bearer "+pass)
		}
		return nil
	}
	resp, err := g.Open(ctx, target)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	next := strings.Contains(resp.Header.Get("Link"), `rel="next"`)
	if err := read(resp.Body); err != nil {
		return false, fmt.Errorf("GET %s: %w", httpx.Redact(target), err)
	}
	return next, nil
}
