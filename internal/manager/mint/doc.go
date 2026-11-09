package mint

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the mint manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the versions of the Swift command line tools a [Mint](https://github.com/yonaskolb/Mint) `Mintfile` pins. " +
		"Only the version is replaced; comments and other lines are kept.",
	Files: []string{"any `Mintfile`"},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "every line `<repo>@<version>`, read as Mint reads it (whitespace trimmed, `#` starts a comment): `owner/repo` is a GitHub repository, `github.com/owner/repo` and `<host.tld>/owner/repo` are https URLs, and http(s), `ssh://` and `git@host:owner/repo` URLs are taken as written (`git@github.com:owner/repo.git@1.2.0`); the name is the URL path (`owner/repo`), the source its http(s) URL (https for `ssh://` and `git@host:`) without `.git`. The version is the tag Mint checks out", Versioning: "`tag`", Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipUnspecified, When: "a line without `@<version>` (Mint installs the newest tag), as Renovate"},
		{Reason: skipRepo, When: "a repository that is neither `owner/repo` nor a git URL with an owner/repo path"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"A branch or commit (`@main`, `@a1b2c3d`) is no version and is skipped by the engine (`non-version value`).",
			"Nothing else is run: `mint bootstrap` installs the new versions.",
		}},
	},
}
