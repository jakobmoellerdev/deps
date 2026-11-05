package githubreleases

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the github-releases datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`owner/repo` (github.com) or `https://host/owner/repo` (GHES)",
	Endpoints: "with a token: GraphQL `releases` (`https://api.github.com/graphql`, GHES `<host>/api/graphql`); without: `GET /repos/{repo}/releases?per_page=100`. " +
		"At most 5 pages, and only until the current release is listed. Drafts and prereleases are dropped. The GraphQL list also reads the repository's `isArchived`: an archived repository is listed under the dashboard's deprecated dependencies.",
	Auth:         "token for the host",
	ReleaseTimes: "`publishedAt`",
}
