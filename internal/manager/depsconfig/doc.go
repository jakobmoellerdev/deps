package depsconfig

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the deps-config manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the `#<ref>` of the `extends` entries of the repository's deps configuration (see [Configuration](configuration.md)), as Renovate's renovate-config manager updates `extends` presets: " +
		"a version tag moves to newer tags of the extended repository, a commit to the newest commit of its default branch. Entries without a ref follow the default branch already and are not pinned.",
	Files: []string{"`.deps.yaml`, `.deps.yml` at the root (the files deps reads its configuration from)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeExtends, What: "an `extends` entry `<repo>[//<path>][#<ref>]`; the name is `owner/name` (on the forge of the checkout: the platform's server, else the origin remote's) or the URL path of a git URL, the source its `https://` URL", Datasource: "`git-tags`; `git-refs` for a 40-hex commit ref", Versioning: "`tag`", Datasources: []string{dsTags, dsRefs}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipInvalid, When: "the entry does not parse (deps reports it when loading the configuration)"},
		{Reason: skipURL, When: "a `file://` URL or another git URL that is not https/http/ssh/git or `git@host:path` with an owner/repo path"},
		{Reason: skipForge, When: "an `owner/name` entry while neither the platform nor the origin remote names the forge"},
		{Reason: skipNoRef, When: "the entry has no `#<ref>`: it reads the default branch"},
		{Reason: skipNonVersion, When: "the ref is neither a version tag (`v1`, `1.2`, `v1.2.3`) nor a 40-hex commit (a branch such as `main`)"},
		{Reason: skipLocate, When: "the entry is written with escapes or as a block scalar"},
	},
	Sections: []refdoc.Section{
		{Title: "Updates", Items: []string{
			"A version-tag ref (`org/deps-config#v1.2.0`) gets updates to newer tags of the repository at the tag's precision (`v1` → `v2`), with release notes and compare link; only the ref is rewritten.",
			"A commit ref (`org/deps-config#<40 hex>`) gets `digest` updates to the head of the repository's default branch.",
			"Renovate's renovate-config manager updates `github>`, `gitlab>`, `gitea>` and `forgejo>` presets with a tag; deps' `extends` entries name a repository directly, so any forge and plain git URLs work.",
		}},
	},
}
