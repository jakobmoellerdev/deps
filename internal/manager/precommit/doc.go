package precommit

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pre-commit manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the `rev` of the hook repositories in pre-commit configurations, plain tags and SHAs frozen by `pre-commit autoupdate --freeze`.",
	Files:   []string{"`.pre-commit-config.yaml` / `.pre-commit-config.yml` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: "repository", What: "`repos[]` entry with `repo` and `rev`; the name is the URL path (`owner/repo`), the source its `https://` URL without `.git`", Versioning: "`tag`", Datasources: []string{"git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "local repository", When: "`repo: local`"},
		{Reason: "meta repository", When: "`repo: meta`"},
		{Reason: "unrecognized repo", When: "`repo` is not an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: "no rev", When: "the entry has no `rev`"},
		{Reason: "pinned without version comment", When: "`rev` is a 40-hex SHA without a `# frozen: <version>` (or `# <version>`) comment"},
		{Reason: "non-version ref", When: "`rev` is neither a SHA nor a version tag"},
		{Reason: "cannot locate value", When: "the rev bytes were not found (block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Edits", Items: []string{
			"Tag revs are replaced in place, keeping quotes and comments.",
			"Frozen revs get the new SHA and the version in the `# frozen:` comment is rewritten.",
			"Pinning a tag rev writes the SHA and inserts ` # frozen: <version>` after the value.",
		}},
	},
}
