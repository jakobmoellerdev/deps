package githubreleases

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the github-releases datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`owner/repo` (github.com) or `https://host/owner/repo` (GHES)",
	Endpoints: "with a token: GraphQL `releases` (`https://api.github.com/graphql`, GHES `<host>/api/graphql`); without: `GET /repos/{repo}/releases?per_page=100`. " +
		"Newest first by creation; pages are fetched only until the current release is listed, since every release created after it precedes it, else up to 100 pages (10000 releases, Renovate's GraphQL query limit). Drafts are dropped, prereleases are listed (the versioning drops them for a dep at a stable version). The GraphQL list also reads the repository's `isArchived`: an archived repository is listed under the dashboard's deprecated dependencies.",
	Auth:         "token for the host",
	ReleaseTimes: "`publishedAt`",
}
