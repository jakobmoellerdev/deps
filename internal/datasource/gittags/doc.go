package gittags

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the git-tags datasource.
var Doc = refdoc.Datasource{
	Name:         "git-tags",
	NameForm:     "repository URL (`https://host/path` or `host/path`)",
	Endpoints:    "`GET <url>/info/refs?service=git-upload-pack`, then `POST <url>/git-upload-pack` (protocol v2 `ls-refs`, `ref-prefix refs/tags/`, `peel`); v0 servers via the advertisement",
	Auth:         "Basic `x-access-token:<token>` for the platform host; `DEPS_GITHUB_COM_TOKEN` for github.com; on 401, `git credential fill` once per host",
	ReleaseTimes: "the tag's release (github.com: the release list; Gitea/Forgejo: their releases API); a tag without one (a floating `v25`) takes the earliest listed release of the same commit, else the tag date (GitHub GraphQL: tagger date of an annotated tag, else committer date; GitHub REST: committer date; Gitea/Forgejo: `GET /repos/{repo}/tags/{tag}` `commit.created`)",
}
