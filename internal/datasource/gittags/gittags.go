// Package gittags lists a remote repository's tags and the commit of its HEAD (the default
// branch), or on request its branches, over the git smart-HTTP protocol, without a git process.
//
// Protocol v2 `ls-refs` with `ref-prefix refs/tags/`, `ref-prefix HEAD` and `peel` (or
// `ref-prefix HEAD` and `ref-prefix refs/heads/` for branches) is used when the server
// advertises it, so only those refs cross the wire; otherwise the v0 ref advertisement is
// parsed. Responses are parsed as a pkt-line stream.
package gittags

import (
	"bufio"
	"bytes"
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
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	forgeds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Tag is a remote tag and the commit it points to (peeled for annotated tags). Branch listings
// reuse it: Name is the branch, or HEAD for the default branch.
type Tag struct{ Name, Commit string }

// Lister fetches tags, and branches when asked, once per repository URL per run; concurrent
// callers share the result.
type Lister struct {
	client *http.Client
	auth   *httpx.Auth
	sem    chan struct{}
	gh     *githubapi.Client // release times of github.com repositories
	gitlab *GitLab           // release times of GitLab repositories; nil: none

	times    datasource.Memo[string, time.Time]  // repository URL + "@" + tag, for tags not in the release lists
	tags     datasource.Memo[string, *entry]     // repository URL -> tags
	heads    datasource.Memo[string, *entry]     // repository URL -> branches and HEAD
	lists    datasource.Memo[string, *listEntry] // Gitea/Forgejo repository URL -> first page of releases
	archived datasource.Memo[string, bool]       // repository URL -> archived (see Archived)

	planMu  sync.Mutex
	planned map[string]githubapi.Repo        // lower-case owner/name -> github.com repository of a planned dep
	ghBatch datasource.Once[map[string]bool] // lower-case owner/name -> archived, of the planned repositories
}

// Plan implements datasource.Planner: the github.com repositories of deps are asked together
// whether they are archived (one GraphQL query per 100 repositories) by the first lookup of
// one of them.
func (l *Lister) Plan(deps []dep.Dep) {
	l.planMu.Lock()
	defer l.planMu.Unlock()
	for _, d := range deps {
		if r, ok := githubRepo(strings.TrimSuffix(d.Source, "/")); ok {
			if l.planned == nil {
				l.planned = map[string]githubapi.Repo{}
			}
			if key := strings.ToLower(r.Path()); l.planned[key] == (githubapi.Repo{}) {
				l.planned[key] = r
			}
		}
	}
}

// listEntry is one page of a Gitea/Forgejo repository's releases.
type listEntry struct {
	times    map[string]time.Time // tag -> published_at; drafts are omitted
	complete bool                 // the page holds every release: absent tags have none
	api      bool                 // the host answered the releases API
}

type entry struct {
	tags []Tag
	head string // commit of HEAD, "" when the server reported none (an empty repository)
	rels []datasource.Release
}

// New returns a Lister. sem bounds concurrent requests and is shared with other datasources;
// auth may be nil for anonymous access. Release times of github.com repositories come from gh,
// whose release lists the github-releases datasource shares.
func New(client *http.Client, auth *httpx.Auth, sem chan struct{}, gh *githubapi.Client) *Lister {
	return &Lister{client: client, auth: auth, sem: sem, gh: gh}
}

// GitLab names the GitLab instances whose repositories' tags are dated through the GitLab API
// rather than probed for Gitea's: by the gitlab-releases and gitlab-tags datasources, whose
// lists their own deps share.
type GitLab struct {
	Bases          []string        // instance URLs: https://gitlab.com, a self-managed `https://host[/path]`
	Releases, Tags *forgeds.Source // the gitlab-releases and gitlab-tags datasources
}

// WithGitLab dates the tags of repositories under gl.Bases with gl's datasources (see
// ReleaseTime); it returns l. Call it before the first lookup.
func (l *Lister) WithGitLab(gl GitLab) *Lister {
	l.gitlab = &gl
	return l
}

// Tags returns the tags of the repository at repoURL (e.g. https://github.com/actions/checkout).
// The returned slice is shared between callers and must not be modified.
func (l *Lister) Tags(ctx context.Context, repoURL string) ([]Tag, error) {
	e, err := l.get(ctx, repoURL, false)
	return e.tags, err
}

// Releases implements datasource.Datasource: the tags of d.Source, each with its commit as
// digest. Whether the repository is archived (Deprecation) is asked alongside, once per
// repository (see Archived). The slice is shared between callers and must not be modified.
func (l *Lister) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = l.Archived(ctx, d.Source) })
	e, err := l.get(ctx, d.Source, false)
	wg.Wait()
	return e.rels, err
}

// Deprecation implements datasource.DeprecationSource: d.Source is archived, as Releases
// found out (see Archived); no request.
func (l *Lister) Deprecation(d dep.Dep, _ string) string {
	if archived, _ := l.archived.Peek(strings.TrimSuffix(d.Source, "/")); archived {
		return "repository archived"
	}
	return ""
}

// Archived reports whether the repository at repoURL is archived, once per repository: on
// github.com from GraphQL `isArchived` (only with a token: anonymous REST requests are scarce),
// asked for every planned repository in one query (see Plan) and for an unplanned one on its
// own; on the GitLab instances (WithGitLab) from the gitlab-tags datasource (GraphQL
// `project{archived}`); elsewhere from the Gitea/Forgejo API (`GET /api/v1/repos/<repo>`,
// `archived`). A host without any of them, or a repository the API does not show, is not
// archived.
func (l *Lister) Archived(ctx context.Context, repoURL string) (bool, error) {
	repoURL = strings.TrimSuffix(repoURL, "/")
	return l.archived.Do(ctx, repoURL, func() (archived bool, err error) {
		defer func() {
			slog.DebugContext(ctx, "repository archived checked", "repo", repoURL, "archived", archived, "err", err)
		}()
		if r, isGH := githubRepo(repoURL); isGH {
			if !l.gh.GraphQL(r) {
				return false, nil
			}
			return l.githubArchived(ctx, r)
		}
		if fd, isGL := l.gitlabRepo(repoURL); isGL {
			return l.gitlab.Tags.Archived(ctx, fd)
		}
		u, repo, err := apiRepo(repoURL)
		if err != nil {
			return false, err
		}
		var meta struct {
			Archived bool `json:"archived"`
		}
		found, err := l.getJSON(ctx, u, "/repos/"+repo, &meta)
		return found && meta.Archived, err
	})
}

// githubArchived answers from the batch query of the planned repositories (plus r), sent by
// the first caller; a repository missing from it is asked on its own.
func (l *Lister) githubArchived(ctx context.Context, r githubapi.Repo) (bool, error) {
	key := strings.ToLower(r.Path())
	batch, err := l.ghBatch.Do(ctx, func() (map[string]bool, error) {
		l.planMu.Lock()
		repos := make([]githubapi.Repo, 0, len(l.planned)+1)
		for _, p := range l.planned {
			repos = append(repos, p)
		}
		if _, ok := l.planned[key]; !ok {
			repos = append(repos, r)
		}
		l.planMu.Unlock()
		slices.SortFunc(repos, func(a, b githubapi.Repo) int { return strings.Compare(a.Path(), b.Path()) })
		res, err := l.gh.Archived(ctx, repos)
		m := make(map[string]bool, len(res))
		for r, a := range res {
			m[strings.ToLower(r.Path())] = a
		}
		return m, err
	})
	if archived, asked := batch[key]; asked || err != nil {
		return archived, err
	}
	res, err := l.gh.Archived(ctx, []githubapi.Repo{r})
	return res[r], err
}

// Digest returns the commit of tag version in d.Source: the tag equal to version, else to
// version with a leading "v" added or removed.
func (l *Lister) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	e, err := l.get(ctx, d.Source, false)
	if err != nil {
		return "", err
	}
	toggled, ok := strings.CutPrefix(version, "v")
	if !ok {
		toggled = "v" + version
	}
	for _, v := range [...]string{version, toggled} {
		for _, t := range e.tags {
			if t.Name == v {
				return t.Commit, nil
			}
		}
	}
	return "", fmt.Errorf("tag %s %w", version, datasource.ErrVersionNotFound)
}

// Head implements datasource.HeadSource: the commit of d.Source's HEAD (its default branch),
// from the same ls-refs request as the tags. Version is "": the head is no tag. An empty
// repository has no head (zero Revision). The time is left unknown; see CommitTime.
func (l *Lister) Head(ctx context.Context, d dep.Dep, _ string) (datasource.Revision, error) {
	e, err := l.get(ctx, d.Source, false)
	return datasource.Revision{Digest: e.head}, err
}

// RefsName is the name of the git-refs datasource.
const RefsName = "git-refs"

// Refs is the git-refs datasource: the tags of a repository as releases (as git-tags), and the
// commit of a branch (HEAD: the default branch) as digest. Under nixpkgs versioning its
// releases are the repository's branches instead (the release branches of NixOS/nixpkgs). It
// shares the Lister's requests and cache.
type Refs struct{ l *Lister }

// Refs returns the git-refs datasource of l.
func (l *Lister) Refs() Refs { return Refs{l} }

// Releases implements datasource.Datasource with the tags of d.Source, each with its commit as
// digest. A dep under nixpkgs versioning opts in to the branches instead (HEAD left out), from
// the branch listing Digest shares (once per repository); other deps never list branches for
// their releases. The slice is shared between callers and must not be modified.
func (r Refs) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	if d.Versioning != versioning.Nixpkgs {
		return r.l.Releases(ctx, d, dir)
	}
	e, err := r.l.get(ctx, d.Source, true)
	return e.rels, err
}

// Deprecation implements datasource.DeprecationSource as git-tags does.
func (r Refs) Deprecation(d dep.Dep, version string) string { return r.l.Deprecation(d, version) }

// Plan implements datasource.Planner as git-tags does; both share one batch.
func (r Refs) Plan(deps []dep.Dep) { r.l.Plan(deps) }

// Digest returns the commit of branch ref in d.Source (HEAD is the default branch), else of
// the tag named ref (an action ref such as `@stable` may be either, as for GitHub's
// commits/<ref>); neither is datasource.ErrVersionNotFound. The tags are listed only when ref
// is no branch.
func (r Refs) Digest(ctx context.Context, d dep.Dep, ref string) (string, error) {
	for _, heads := range [...]bool{true, false} {
		e, err := r.l.get(ctx, d.Source, heads)
		if err != nil {
			return "", err
		}
		for _, h := range e.tags {
			if h.Name == ref {
				return h.Commit, nil
			}
		}
	}
	return "", fmt.Errorf("branch or tag %s %w", ref, datasource.ErrVersionNotFound)
}

// CommitTime implements datasource.CommitTimer: the committer date of commit in d.Source, via
// the GitHub API for github.com, the GitLab API for the GitLab instances (WithGitLab) and the
// Gitea/Forgejo API (`<server>/api/v1`) for other hosts; zero when unknown (a host without
// any of them, a missing commit). Once per repository and commit.
func (l *Lister) CommitTime(ctx context.Context, d dep.Dep, commit string) (time.Time, error) {
	repoURL := strings.TrimSuffix(d.Source, "/")
	return l.times.Do(ctx, repoURL+"#"+commit, func() (time.Time, error) {
		var t time.Time
		var err error
		if r, isGH := githubRepo(repoURL); isGH {
			t, err = l.gh.CommitTime(ctx, r, commit)
		} else if fd, isGL := l.gitlabRepo(repoURL); isGL {
			t, err = l.gitlab.Tags.CommitTime(ctx, fd, commit)
		} else {
			t, err = l.giteaCommitTime(ctx, repoURL, commit)
		}
		slog.DebugContext(ctx, "commit time", "repo", repoURL, "commit", commit, "time", t, "err", err)
		return t, err
	})
}

// ReleaseTime implements datasource.Timer for tag version in d.Source, via the GitHub API for
// github.com, the GitLab API for the GitLab instances (WithGitLab; see gitlabTime) and the
// Gitea/Forgejo API (`<server>/api/v1`) for other hosts; on a host without any of them it is
// unknown (zero). The time is, in this order:
//   - the publication time of the tag's release;
//   - for a tag without a release of its own (a floating `v25` next to the `v25.0.0` release,
//     or a repository that only tags), the earliest publication time of a listed release
//     whose tag points to the same commit;
//   - else the date of the tag (see githubapi.Client.TagTime and giteaTagTime).
//
// The first page of the repository's releases is fetched once per repository and serves every
// tag on it (for github.com it is the list shared with github-releases, possibly with more
// pages). A tag without a listed release costs at most two requests, once per tag: its own
// release when the list is incomplete, and its date when no listed release shares its commit.
func (l *Lister) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	repoURL := strings.TrimSuffix(d.Source, "/")
	r, isGH := githubRepo(repoURL)
	if fd, isGL := l.gitlabRepo(repoURL); isGL && !isGH {
		return l.tagTime(ctx, repoURL, version, func() (time.Time, error) { return l.gitlabTime(ctx, repoURL, fd, version) })
	}
	var (
		t        time.Time
		complete bool // the list decides whether the tag has a release
	)
	if isGH {
		err := l.gh.Releases(ctx, r, nil, func(gl *githubapi.List) {
			rel, listed := gl.Find(version)
			t, complete = rel.Published, listed || gl.Complete()
		})
		switch {
		case errors.Is(err, githubapi.ErrNotFound):
			return time.Time{}, nil // the repository is not visible to the API
		case err != nil || !t.IsZero():
			return t, err
		}
	} else {
		le, err := l.giteaList(ctx, repoURL)
		switch {
		case err != nil:
			return time.Time{}, err
		case !le.api:
			return time.Time{}, nil
		}
		if t = le.times[version]; !t.IsZero() {
			return t, nil
		}
		complete = le.complete
	}
	return l.tagTime(ctx, repoURL, version, func() (time.Time, error) {
		if !complete {
			var own time.Time
			var err error
			if isGH {
				own, err = l.gh.ReleaseTime(ctx, r, version)
			} else {
				own, err = l.releaseTime(ctx, repoURL, version)
			}
			if err != nil || !own.IsZero() {
				return own, err
			}
		}
		return l.unreleasedTime(ctx, repoURL, r, isGH, version)
	})
}

// unreleasedTime dates a tag without a release of its own: the earliest publication time of
// a release whose tag points to the same commit, among the releases already listed (no further
// pages are fetched), else the date of the tag.
func (l *Lister) unreleasedTime(ctx context.Context, repoURL string, r githubapi.Repo, isGH bool, tag string) (time.Time, error) {
	tags, err := l.Tags(ctx, repoURL)
	if err != nil {
		return time.Time{}, err
	}
	var t time.Time
	commit := commitOf(tags, tag)
	if isGH {
		err = l.gh.Releases(ctx, r, nil, func(gl *githubapi.List) {
			t = earliestRelease(tags, commit, func(name string) time.Time { rel, _ := gl.Find(name); return rel.Published })
		})
		if err != nil {
			return time.Time{}, err
		}
	} else {
		le, _ := l.giteaList(ctx, repoURL)
		t = earliestRelease(tags, commit, func(name string) time.Time { return le.times[name] })
	}
	if !t.IsZero() {
		slog.DebugContext(ctx, "tag dated by a release of its commit", "repo", repoURL, "tag", tag, "commit", commit, "time", t)
		return t, nil
	}
	if isGH {
		return l.gh.TagTime(ctx, r, tag, commit)
	}
	return l.giteaTagTime(ctx, repoURL, tag)
}

// commitOf returns the commit of tag among tags, "" when it is not listed.
func commitOf(tags []Tag, tag string) string {
	if i := slices.IndexFunc(tags, func(t Tag) bool { return t.Name == tag }); i >= 0 {
		return tags[i].Commit
	}
	return ""
}

// earliestRelease returns the earliest non-zero publication time, by published, of the tags
// pointing to commit; zero for an empty commit.
func earliestRelease(tags []Tag, commit string, published func(tag string) time.Time) time.Time {
	var t time.Time
	if commit == "" {
		return t
	}
	for _, o := range tags {
		if o.Commit != commit {
			continue
		}
		if p := published(o.Name); !p.IsZero() && (t.IsZero() || p.Before(t)) {
			t = p
		}
	}
	return t
}

// gitlabTime dates tag of a GitLab repository as ReleaseTime does elsewhere, from the lists
// of the gitlab-releases and gitlab-tags datasources (up to 10 pages each, fetched once per
// repository and shared with their own deps): the tag's release (`released_at`), else the
// earliest release of a tag of the same commit, else the tag's commit date
// (`commit.created_at`). A failed release list falls through to the tag list.
func (l *Lister) gitlabTime(ctx context.Context, repoURL string, fd dep.Dep, tag string) (time.Time, error) {
	rels := l.gitlab.Releases
	t, err := rels.ReleaseTime(ctx, fd, tag)
	if err == nil && t.IsZero() {
		var tags []Tag
		if tags, err = l.Tags(ctx, repoURL); err == nil {
			commit := commitOf(tags, tag)
			t = earliestRelease(tags, commit, func(name string) time.Time { p, _ := rels.ReleaseTime(ctx, fd, name); return p })
			if !t.IsZero() {
				slog.DebugContext(ctx, "tag dated by a release of its commit", "repo", repoURL, "tag", tag, "commit", commit, "time", t)
			}
		}
	}
	if !t.IsZero() {
		return t, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "gitlab releases unavailable, dating the tag by its commit", "repo", repoURL, "tag", tag, "err", err)
	}
	return l.gitlab.Tags.ReleaseTime(ctx, fd, tag)
}

// gitlabRepo returns the gitlab-releases/gitlab-tags dep of repoURL when it lies under one of
// the GitLab instances (WithGitLab): the project path as name, the instance as registry.
func (l *Lister) gitlabRepo(repoURL string) (dep.Dep, bool) {
	if l.gitlab == nil {
		return dep.Dep{}, false
	}
	for _, base := range l.gitlab.Bases {
		base = strings.TrimRight(base, "/")
		if len(repoURL) > len(base)+1 && repoURL[len(base)] == '/' && strings.EqualFold(repoURL[:len(base)], base) {
			return dep.Dep{Name: strings.TrimSuffix(repoURL[len(base)+1:], ".git"), Source: base}, true
		}
	}
	return dep.Dep{}, false
}

// githubRepo returns the github.com repository of repoURL.
func githubRepo(repoURL string) (githubapi.Repo, bool) {
	u, err := url.Parse(repoURL)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return githubapi.Repo{}, false
	}
	r, err := githubapi.ParseRepo(repoURL)
	return r, err == nil
}

// tagTime runs lookup once per repository and tag.
func (l *Lister) tagTime(ctx context.Context, repoURL, tag string, lookup func() (time.Time, error)) (time.Time, error) {
	return l.times.Do(ctx, repoURL+"@"+tag, func() (time.Time, error) {
		t, err := lookup()
		slog.DebugContext(ctx, "tag release time", "repo", repoURL, "tag", tag, "time", t, "err", err)
		return t, err
	})
}

// giteaListSize is the page size of the releases list: Gitea/Forgejo cap at MAX_RESPONSE_ITEMS
// (default 50).
const giteaListSize = 50

// giteaList returns the first page of releases of repoURL, fetched once per repository. The
// entry is never nil.
func (l *Lister) giteaList(ctx context.Context, repoURL string) (*listEntry, error) {
	return l.lists.Do(ctx, repoURL, func() (*listEntry, error) {
		le := &listEntry{}
		var err error
		le.times, le.complete, le.api, err = l.releaseList(ctx, repoURL)
		slog.DebugContext(ctx, "releases listed", "repo", repoURL, "releases", len(le.times), "complete", le.complete, "api", le.api, "err", err)
		return le, err
	})
}

// releaseList fetches the first page of releases of repoURL. A host without the API (404 or a
// non-JSON answer) has api false: every tag is unknown without further requests. A page
// shorter than requested is complete.
func (l *Lister) releaseList(ctx context.Context, repoURL string) (times map[string]time.Time, complete, api bool, err error) {
	u, repo, err := apiRepo(repoURL)
	if err != nil {
		return nil, false, false, fmt.Errorf("releases: %w", err)
	}
	path, size := "/repos/"+repo+"/releases?limit="+strconv.Itoa(giteaListSize), giteaListSize
	var rels []struct {
		TagName     string     `json:"tag_name"`
		Draft       bool       `json:"draft"`
		PublishedAt *time.Time `json:"published_at"`
	}
	found, err := l.getJSON(ctx, u, path, &rels)
	if err != nil {
		return nil, false, false, fmt.Errorf("releases of %s: %w", repo, err)
	}
	if !found {
		return nil, true, false, nil
	}
	times = make(map[string]time.Time, len(rels))
	for _, r := range rels {
		if !r.Draft && r.PublishedAt != nil && r.TagName != "" {
			times[r.TagName] = *r.PublishedAt
		}
	}
	return times, len(rels) < size, true, nil
}

func (l *Lister) releaseTime(ctx context.Context, repoURL, tag string) (time.Time, error) {
	u, repo, err := apiRepo(repoURL)
	if err != nil {
		return time.Time{}, fmt.Errorf("release %s: %w", tag, err)
	}
	var rel struct {
		PublishedAt *time.Time `json:"published_at"`
	}
	found, err := l.getJSON(ctx, u, "/repos/"+repo+"/releases/tags/"+platform.EscapePath(tag), &rel)
	if err != nil {
		return time.Time{}, fmt.Errorf("release %s of %s: %w", tag, repo, err)
	}
	if !found || rel.PublishedAt == nil {
		return time.Time{}, nil
	}
	return *rel.PublishedAt, nil
}

// giteaTagTime returns the date Gitea/Forgejo report for tag (`GET /repos/{repo}/tags/{tag}`,
// `commit.created`): the tagger date of an annotated tag, else the committer date of its
// commit, as Renovate's github-tags datasource dates tags. A missing tag is zero.
func (l *Lister) giteaTagTime(ctx context.Context, repoURL, tag string) (time.Time, error) {
	u, repo, err := apiRepo(repoURL)
	if err != nil {
		return time.Time{}, fmt.Errorf("tag %s: %w", tag, err)
	}
	var tg struct {
		Commit struct {
			Created *time.Time `json:"created"`
		} `json:"commit"`
	}
	found, err := l.getJSON(ctx, u, "/repos/"+repo+"/tags/"+platform.EscapePath(tag), &tg)
	if err != nil {
		return time.Time{}, fmt.Errorf("tag %s of %s: %w", tag, repo, err)
	}
	if !found || tg.Commit.Created == nil {
		return time.Time{}, nil
	}
	return *tg.Commit.Created, nil
}

// giteaCommitTime returns the committer date Gitea/Forgejo report for commit
// (`GET /repos/{repo}/git/commits/{sha}`, `created`). A missing commit is zero.
func (l *Lister) giteaCommitTime(ctx context.Context, repoURL, commit string) (time.Time, error) {
	u, repo, err := apiRepo(repoURL)
	if err != nil {
		return time.Time{}, fmt.Errorf("commit %s: %w", commit, err)
	}
	var c struct {
		Created *time.Time `json:"created"`
	}
	found, err := l.getJSON(ctx, u, "/repos/"+repo+"/git/commits/"+url.PathEscape(commit)+"?stat=false&verification=false&files=false", &c)
	if err != nil {
		return time.Time{}, fmt.Errorf("commit %s of %s: %w", commit, repo, err)
	}
	if !found || c.Created == nil {
		return time.Time{}, nil
	}
	return *c.Created, nil
}

// apiRepo splits repoURL into its URL and the "owner/name" path of the forge API.
func apiRepo(repoURL string) (*url.URL, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("invalid repository %q", repoURL)
	}
	return u, strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), nil
}

// getJSON GETs path from the Gitea/Forgejo API of repository u into v. found is false on 404
// or when the body is not JSON (a host without the API may answer with a web page): unknown,
// not an error.
func (l *Lister) getJSON(ctx context.Context, u *url.URL, path string, v any) (found bool, err error) {
	_, token, authed := l.auth.Static(u.Hostname())
	target := u.Scheme + "://" + u.Host + "/api/v1" + path
	release, err := httpx.Acquire(ctx, l.sem)
	if err != nil {
		return false, err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", httpx.UserAgent())
	if authed {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return false, err
	}
	defer closeBody(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if json.UnmarshalRead(io.LimitReader(resp.Body, 16<<20), v) != nil {
		return false, nil
	}
	return true, nil
}

// get returns the tags of repoURL, or with heads its branches and HEAD, fetching them once.
// The entry is never nil.
func (l *Lister) get(ctx context.Context, repoURL string, heads bool) (*entry, error) {
	repoURL = strings.TrimSuffix(repoURL, "/")
	memo := &l.tags
	if heads {
		memo = &l.heads
	}
	return memo.Do(ctx, repoURL, func() (*entry, error) {
		e := &entry{}
		var err error
		e.tags, e.head, err = l.fetch(ctx, repoURL, heads)
		e.rels = make([]datasource.Release, 0, len(e.tags))
		for _, t := range e.tags {
			if !heads || t.Name != "HEAD" {
				e.rels = append(e.rels, datasource.Release{Version: t.Name, Digest: t.Commit})
			}
		}
		return e, err
	})
}

// session carries the credentials of one repository fetch across its requests.
type session struct {
	l           *Lister
	url         string
	host        string // host[:port] for the credential helper
	user, pass  string
	hasCreds    bool
	triedHelper bool
	head        string // commit of HEAD, set while parsing the refs
	heads       bool   // list HEAD and branches (as Tags) instead of tags
}

func (l *Lister) fetch(ctx context.Context, repoURL string, heads bool) (tags []Tag, head string, err error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return nil, "", fmt.Errorf("ls-refs %s: %w", repoURL, err)
	}
	start, protocol := time.Now(), "v0"
	s := &session{l: l, url: repoURL, host: u.Host, heads: heads}
	defer func() {
		if heads {
			slog.DebugContext(ctx, "branches listed", "repo", repoURL, "protocol", protocol, "refs", len(tags), "duration", time.Since(start), "err", err)
			return
		}
		slog.DebugContext(ctx, "tags listed", "repo", repoURL, "protocol", protocol, "tags", len(tags), "head", s.head, "duration", time.Since(start), "err", err)
	}()
	s.user, s.pass, s.hasCreds = l.auth.Static(u.Hostname())

	tags, caps, err := s.advertisement(ctx)
	if err == nil && caps != nil {
		protocol = "v2"
		tags, err = s.lsRefs(ctx, caps)
	}
	if err != nil {
		s.head = ""
	}
	return tags, s.head, err
}

// refName returns the offset of the name in ref when the session lists it: after refs/tags/,
// or with heads after refs/heads/, and HEAD whole.
func (s *session) refName(ref string) (int, bool) {
	prefix := "refs/tags/"
	if s.heads {
		if ref == "HEAD" {
			return 0, true
		}
		prefix = "refs/heads/"
	}
	return len(prefix), strings.HasPrefix(ref, prefix)
}

// v2caps is what deps needs from a protocol v2 capability advertisement.
type v2caps struct {
	agent        bool
	objectFormat string
}

// advertisement performs the info/refs request. For a v2 server it returns the capabilities;
// for a v0 server it returns the tags parsed from the ref advertisement.
func (s *session) advertisement(ctx context.Context) ([]Tag, *v2caps, error) {
	release, err := httpx.Acquire(ctx, s.l.sem)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	resp, err := s.do(ctx, http.MethodGet, s.url+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return nil, nil, err
	}
	defer closeBody(resp)
	// Like git, keep the redirected base URL (GitLab redirects `/<repo>` to `/<repo>.git`) for
	// the ls-refs POST, which is not redirected; only within the host, so credentials stay put.
	if resp.Request != nil && resp.Request.URL.Host == s.host {
		ru := resp.Request.URL
		if base, ok := strings.CutSuffix(ru.Scheme+"://"+ru.Host+ru.Path, "/info/refs"); ok {
			s.url = base
		}
	}
	p := newPktReader(resp.Body)

	line, kind, err := p.next()
	if err != nil {
		return nil, nil, s.wrap(err)
	}
	if kind == pktData && bytes.HasPrefix(line, []byte("# service=")) {
		if _, kind, err = p.next(); err != nil {
			return nil, nil, s.wrap(err)
		}
		if kind != pktFlush {
			return nil, nil, s.wrap(errMalformed)
		}
		if line, kind, err = p.next(); err != nil {
			return nil, nil, s.wrap(err)
		}
	}
	if kind == pktData && string(line) == "version 2" {
		caps := &v2caps{}
		for {
			line, kind, err = p.next()
			if err != nil {
				return nil, nil, s.wrap(err)
			}
			if kind == pktFlush {
				return nil, caps, nil
			}
			if kind != pktData {
				return nil, nil, s.wrap(errMalformed)
			}
			k, v, _ := bytes.Cut(line, []byte{'='})
			switch string(k) {
			case "agent":
				caps.agent = true
			case "object-format":
				caps.objectFormat = string(v)
			}
		}
	}

	// Protocol v0 advertisement: "<oid> <ref>[\0caps]" lines, then a flush.
	var tags []Tag
	for first := true; kind == pktData; first = false {
		if first {
			if i := bytes.IndexByte(line, 0); i >= 0 {
				line = line[:i]
			}
		}
		if bytes.HasPrefix(line, []byte("ERR ")) {
			return nil, nil, fmt.Errorf("ls-refs %s: %s", s.url, line[4:])
		}
		if oid, ref, ok := bytes.Cut(line, []byte{' '}); ok {
			if off, ok := s.refName(string(ref)); ok {
				if base, peeled := bytes.CutSuffix(ref[off:], []byte("^{}")); peeled {
					setCommit(tags, base, string(oid))
				} else {
					str := string(line) // one allocation per ref; name and commit slice into it
					tags = append(tags, Tag{Name: str[len(oid)+1+off:], Commit: str[:len(oid)]})
				}
			} else if string(ref) == "HEAD" {
				s.head = string(oid)
			}
		}
		if line, kind, err = p.next(); err != nil {
			return nil, nil, s.wrap(err)
		}
	}
	if kind != pktFlush {
		return nil, nil, s.wrap(errMalformed)
	}
	return tags, nil, nil
}

// setCommit overwrites the commit of tag name; the peeled line usually directly follows the tag.
func setCommit(tags []Tag, name []byte, commit string) {
	for i := len(tags) - 1; i >= 0; i-- {
		if tags[i].Name == string(name) {
			tags[i].Commit = commit
			return
		}
	}
}

// lsRefs runs the protocol v2 ls-refs command restricted to tags and HEAD (HEAD's commit is
// recorded in s.head), or with heads to HEAD and branches.
func (s *session) lsRefs(ctx context.Context, caps *v2caps) ([]Tag, error) {
	body := make([]byte, 0, 128)
	body = appendPkt(body, "command=ls-refs\n")
	if caps.agent {
		body = appendPkt(body, "agent="+httpx.UserAgent()+"\n")
	}
	if caps.objectFormat != "" {
		body = appendPkt(body, "object-format="+caps.objectFormat+"\n")
	}
	body = append(body, "0001"...)
	body = appendPkt(body, "peel\n")
	if s.heads {
		body = appendPkt(body, "ref-prefix HEAD\n")
		body = appendPkt(body, "ref-prefix refs/heads/\n")
	} else {
		body = appendPkt(body, "ref-prefix refs/tags/\n")
		body = appendPkt(body, "ref-prefix HEAD\n")
	}
	body = append(body, "0000"...)

	release, err := httpx.Acquire(ctx, s.l.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	// ls-refs only reads: retry it like a GET.
	resp, err := s.do(httpx.Idempotent(ctx), http.MethodPost, s.url+"/git-upload-pack", body)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	p := newPktReader(resp.Body)

	var tags []Tag
	for {
		line, kind, err := p.next()
		if err != nil {
			return nil, s.wrap(err)
		}
		switch kind {
		case pktFlush, pktResponseEnd:
			return tags, nil
		case pktDelim:
			return nil, s.wrap(errMalformed)
		}
		if bytes.HasPrefix(line, []byte("ERR ")) {
			return nil, fmt.Errorf("ls-refs %s: %s", s.url, line[4:])
		}
		// "<oid> <ref>[ <attr>...]" with attr "peeled:<oid>".
		str := string(line) // one allocation per tag; name and commit slice into it
		oid, rest, ok := strings.Cut(str, " ")
		if !ok {
			return nil, s.wrap(errMalformed)
		}
		ref, attrs, _ := strings.Cut(rest, " ")
		off, ok := s.refName(ref)
		if !ok {
			if ref == "HEAD" {
				s.head = oid
			}
			continue
		}
		name := ref[off:]
		commit := oid
		for attrs != "" {
			var a string
			a, attrs, _ = strings.Cut(attrs, " ")
			if peeled, ok := strings.CutPrefix(a, "peeled:"); ok {
				commit = peeled
			}
		}
		tags = append(tags, Tag{Name: name, Commit: commit})
	}
}

// do sends one request with the session's credentials. On a 401 without credentials it asks the
// git credential helper and retries once. Non-2xx responses become errors.
func (s *session) do(ctx context.Context, method, target string, body []byte) (*http.Response, error) {
	for {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, r)
		if err != nil {
			return nil, fmt.Errorf("ls-refs %s: %w", s.url, err)
		}
		req.Header.Set("Git-Protocol", "version=2")
		req.Header.Set("User-Agent", httpx.UserAgent())
		if body != nil {
			req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
			req.Header.Set("Accept", "application/x-git-upload-pack-result")
		}
		if s.hasCreds {
			req.SetBasicAuth(s.user, s.pass)
		}
		resp, err := s.l.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ls-refs %s: %w", s.url, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && !s.hasCreds && !s.triedHelper {
			closeBody(resp)
			s.triedHelper = true
			if s.user, s.pass, s.hasCreds = s.l.auth.Helper(ctx, s.host); s.hasCreds {
				continue
			}
			return nil, fmt.Errorf("ls-refs %s: HTTP %d", s.url, resp.StatusCode)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			closeBody(resp)
			return nil, fmt.Errorf("ls-refs %s: HTTP %d", s.url, resp.StatusCode)
		}
		return resp, nil
	}
}

func (s *session) wrap(err error) error {
	if errors.Is(err, errMalformed) {
		return fmt.Errorf("ls-refs %s: malformed pkt-line", s.url)
	}
	return fmt.Errorf("ls-refs %s: %w", s.url, err)
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	httpx.DrainBody(resp)
	_ = resp.Body.Close()
}

var errMalformed = errors.New("malformed pkt-line")

// Packet kinds returned by pktReader.next.
const (
	pktFlush       = 0 // "0000"
	pktDelim       = 1 // "0001"
	pktResponseEnd = 2 // "0002"
	pktData        = 3
)

const maxPktLen = 65520

type pktReader struct {
	r   *bufio.Reader
	buf []byte
}

func newPktReader(r io.Reader) *pktReader { return &pktReader{r: bufio.NewReader(r)} }

// next returns the next packet. Data payloads have one trailing newline removed and are only
// valid until the following call.
func (p *pktReader) next() ([]byte, int, error) {
	hdr, err := p.r.Peek(4)
	if err != nil {
		return nil, 0, eofMalformed(err)
	}
	n := 0
	for _, c := range hdr {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return nil, 0, errMalformed
		}
		n = n<<4 | int(d)
	}
	_, _ = p.r.Discard(4)
	switch {
	case n <= pktResponseEnd:
		return nil, n, nil
	case n < 4 || n > maxPktLen:
		return nil, 0, errMalformed
	}
	n -= 4
	var b []byte
	if n <= p.r.Size() {
		if b, err = p.r.Peek(n); err != nil {
			return nil, 0, eofMalformed(err)
		}
		_, _ = p.r.Discard(n)
	} else {
		if cap(p.buf) < n {
			p.buf = make([]byte, n)
		}
		b = p.buf[:n]
		if _, err = io.ReadFull(p.r, b); err != nil {
			return nil, 0, eofMalformed(err)
		}
	}
	return bytes.TrimSuffix(b, []byte{'\n'}), pktData, nil
}

func eofMalformed(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errMalformed
	}
	return err
}

func appendPkt(b []byte, payload string) []byte {
	const hex = "0123456789abcdef"
	n := len(payload) + 4
	b = append(b, hex[n>>12&0xf], hex[n>>8&0xf], hex[n>>4&0xf], hex[n&0xf])
	return append(b, payload...)
}
