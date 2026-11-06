package gittags

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the git-tags datasource.
var Doc = refdoc.Datasource{
	Name:         "git-tags",
	NameForm:     "repository URL (`https://host/path` or `host/path`)",
	Endpoints:    "`GET <url>/info/refs?service=git-upload-pack`, then `POST <url>/git-upload-pack` (protocol v2 `ls-refs`, `ref-prefix refs/tags/`, `ref-prefix HEAD`, `peel`); v0 servers via the advertisement. `HEAD` is the default branch commit that github-actions commit pins track",
	Auth:         "Basic `x-access-token:<token>` for the platform host; `DEPS_GITHUB_COM_TOKEN` for github.com; on 401, `git credential fill` once per host",
	ReleaseTimes: "the tag's release (github.com: the release list; GitLab: the `gitlab-releases` list, `released_at`; Gitea/Forgejo: their releases API); a tag without one (a floating `v25`) takes the earliest listed release of the same commit, else the tag date (GitHub GraphQL: tagger date of an annotated tag, else committer date; GitHub REST: committer date; GitLab: the `gitlab-tags` list, `commit.created_at`; Gitea/Forgejo: `GET /repos/{repo}/tags/{tag}` `commit.created`). GitLab means gitlab.com and the instance of `gitlabci.url` (else `CI_SERVER_URL`, a GitLab platform); its lists are the gitlab datasources' (up to 10 pages, once per repository); other hosts are asked for the Gitea/Forgejo API, and without it the time is unknown. The default branch head is dated by its committer date (GitHub GraphQL `object(oid)` or REST `/git/commits/{sha}`; GitLab `GET /projects/<path>/repository/commits/{sha}` `committed_date`; Gitea/Forgejo `GET /repos/{repo}/git/commits/{sha}` `created`), asked only when a `digest` minimumReleaseAge applies",
}

// RefsDoc is the reference documentation of the git-refs datasource.
var RefsDoc = refdoc.Datasource{
	Name:         RefsName,
	NameForm:     "repository URL (`https://host/path`); the version is a branch, `HEAD` for the default branch, and the digest its commit",
	Endpoints:    "as `git-tags`, with `ref-prefix HEAD` and `ref-prefix refs/heads/` for the branch commits; releases are the tags",
	Auth:         "as `git-tags`",
	ReleaseTimes: "none: branch commits are not dated, so `minimumReleaseAge` never holds a digest update back",
}
