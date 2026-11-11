package forge

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	gitlabName = "project path (`group/subgroup/project`) on `registryUrl` (default `" + DefaultGitLabURL + "`), or a repository URL `https://host/group/project`"
	giteaAuth  = "Basic credentials registered for the host (the platform token, `x-access-token:<token>`), or `Authorization: Bearer` when the token is a JWT (a Forgejo Authorized Integration); without, on 401/403/404 `git credential fill` once per host"
	gitlabAuth = "the token registered for the host (the platform token, `DEPS_GITLAB_TOKEN` or `CI_JOB_TOKEN` for the GitLab instance) as `Authorization: Bearer`, the CI job token as `JOB-TOKEN`; without, on 401/403/404 `git credential fill` once per host"
	pages      = " Newest first; pages are followed while the response links a next one, at most 10."
	// archived ends the description of the request that tells whether the repository is
	// archived.
	archived   = ", sent alongside the first page once per repository: an archived repository is listed under the dashboard's deprecated dependencies"
	gitlabRepo = ". `GET <registryUrl>/api/graphql?query=…project(fullPath:<path>){archived}` (the anonymous REST project has no `archived`; a project GitLab does not show is not archived)" + archived
	giteaRepo  = ". `GET <registryUrl>/api/v1/repos/<owner>/<repo>` (`archived`)" + archived
)

func giteaName(def string) string {
	return "`owner/repo` on `registryUrl` (default `" + def + "`), or a repository URL `https://host/owner/repo`"
}

// GitLabReleasesDoc is the reference documentation of the gitlab-releases datasource.
var GitLabReleasesDoc = refdoc.Datasource{
	Name:         GitLabReleases,
	NameForm:     gitlabName,
	Endpoints:    "`GET <registryUrl>/api/v4/projects/<path>/releases?per_page=100`." + pages + " Upcoming releases are dropped; the version is the `tag_name`" + gitlabRepo,
	Auth:         gitlabAuth,
	ReleaseTimes: "`released_at`",
}

// GitLabTagsDoc is the reference documentation of the gitlab-tags datasource.
var GitLabTagsDoc = refdoc.Datasource{
	Name:         GitLabTags,
	NameForm:     gitlabName,
	Endpoints:    "`GET <registryUrl>/api/v4/projects/<path>/repository/tags?per_page=100`." + pages + " The digest is the tag's `commit.id`; a pinned tag older than the listed pages is looked up with `GET …/repository/tags/<tag>`" + gitlabRepo,
	Auth:         gitlabAuth,
	ReleaseTimes: "`commit.created_at`",
}

// GiteaReleasesDoc is the reference documentation of the gitea-releases datasource.
var GiteaReleasesDoc = refdoc.Datasource{
	Name:         GiteaReleases,
	NameForm:     giteaName(DefaultGiteaURL),
	Endpoints:    "`GET <registryUrl>/api/v1/repos/<owner>/<repo>/releases?draft=false&limit=50`." + pages + " Drafts and prereleases are dropped; the version is the `tag_name`" + giteaRepo,
	Auth:         giteaAuth,
	ReleaseTimes: "`published_at`",
}

// GiteaTagsDoc is the reference documentation of the gitea-tags datasource.
var GiteaTagsDoc = refdoc.Datasource{
	Name:         GiteaTags,
	NameForm:     giteaName(DefaultGiteaURL),
	Endpoints:    "`GET <registryUrl>/api/v1/repos/<owner>/<repo>/tags?limit=50`." + pages + " The digest is the tag's `commit.sha`; a pinned tag older than the listed pages is looked up with `GET …/tags/<tag>`" + giteaRepo,
	Auth:         giteaAuth,
	ReleaseTimes: "`commit.created`",
}

// ForgejoReleasesDoc is the reference documentation of the forgejo-releases datasource.
var ForgejoReleasesDoc = refdoc.Datasource{
	Name:         ForgejoReleases,
	NameForm:     giteaName(DefaultForgejoURL),
	Endpoints:    "as `gitea-releases` (the same API)",
	Auth:         giteaAuth,
	ReleaseTimes: "`published_at`",
}

// ForgejoTagsDoc is the reference documentation of the forgejo-tags datasource.
var ForgejoTagsDoc = refdoc.Datasource{
	Name:         ForgejoTags,
	NameForm:     giteaName(DefaultForgejoURL),
	Endpoints:    "as `gitea-tags` (the same API)",
	Auth:         giteaAuth,
	ReleaseTimes: "`commit.created`",
}
