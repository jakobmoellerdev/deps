package custom

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the custom manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "User-defined managers: each `custom[]` entry is one manager instance; see [`custom`](configuration.md#custom).",
	Files:   []string{"`custom[].files` globs"},
	DepTypes: []refdoc.DepType{
		{Name: "annotation", What: "`# deps: <datasource> <name>` or `# renovate: datasource=… depName=…` on the previous line", Datasource: "from the marker", Versioning: "from the marker or `custom[].versioning`"},
		{Name: "regex", What: "`match` RE2 over the whole file", Datasource: "`custom[].datasource` or the `datasource` group", Versioning: "`custom[].versioning` or the `versioning` group"},
	},
	Skips: []refdoc.Skip{
		{Reason: "no version on next line", When: "an annotation is not followed by a line with a version"},
		{Reason: "renovate marker without datasource", When: "a `# renovate:` marker lacks `datasource=`"},
		{Reason: "renovate marker without depName", When: "a `# renovate:` marker lacks `depName=`"},
		{Reason: "empty version", When: "regex mode: the version is empty"},
		{Reason: "empty name", When: "regex mode: the name is empty"},
		{Reason: "empty datasource", When: "regex mode: the datasource is empty"},
		{Reason: "checksum is not 64 or 128 hex digits", When: "regex mode: the `checksum` group is not a SHA-256 or SHA-512 hex digest"},
		{Reason: "unknown versioning \"<v>\"", When: "the versioning is not a known scheme"},
		{Reason: "invalid git-tags name \"<name>\"", When: "a `git-tags` dep whose name is not a repository URL"},
		{Reason: "invalid github-releases name \"<name>\"", When: "a `github-releases` dep whose name is not `owner/repo` or a repository URL"},
		{Reason: "unknown datasource \"<ds>\"", When: "the datasource is neither built in nor `custom.<name>` of `customDatasources`"},
	},
	Sections: []refdoc.Section{
		{Title: "Edits", Body: "The version is replaced. A captured digest (`@sha256:` for docker markers, or a `digest` group) and a `checksum` group are replaced together with it."},
	},
}
