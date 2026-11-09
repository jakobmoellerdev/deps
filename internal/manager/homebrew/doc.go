package homebrew

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/download"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the homebrew manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the downloads of Homebrew formulae: every `url` with its `sha256`, per platform (`on_macos`, `on_linux`, `on_arm`, `on_intel`) and per `resource`, " +
		"their `mirror`s and the `version` statement. GitHub, Gitea, Forgejo and GitLab tag archives and release assets, and npm registry tarballs.",
	Files: []string{"`Formula/<name>.rb`, also `Formula/<dir>/<name>.rb` (homebrew-core's sharding), at the repository root"},
	DepTypes: []refdoc.DepType{
		{
			Name:        DepType,
			What:        "each `url \"…\"` of the formula outside `resource` blocks (the stable download, also one per platform block) with the first `sha256 \"…\"` after it; " + download.DocURLs,
			Datasource:  download.DocDatasource,
			Versioning:  download.DocVersioning,
			Datasources: download.Datasources,
		},
		{
			Name:        DepTypeResource,
			What:        "the `url \"…\"` of a `resource \"<name>\" do … end` block with the first `sha256 \"…\"` after it; the URL forms of `formula`",
			Datasource:  download.DocDatasource,
			Versioning:  "as `formula`",
			Datasources: download.Datasources,
		},
		{
			Name:        DepTypeMirror,
			What:        "a `mirror \"…\"` after a supported `url`, on any host, when the URL's version occurs in its path one to three times (not inside a longer version): moves with that `url`",
			Datasource:  "the `url`'s: " + download.DocDatasource,
			Versioning:  "the `url`'s",
			Datasources: download.Datasources,
		},
		{
			Name:        DepTypeVersion,
			What:        "a `version \"…\"` statement equal to the version of a supported `url` of the formula (or of the same resource), needed when Homebrew cannot read the version from the URL (bare binaries): moves with that `url`",
			Datasource:  "the `url`'s: " + download.DocDatasource,
			Versioning:  "the `url`'s",
			Datasources: download.Datasources,
		},
	},
	Skips: []refdoc.Skip{
		{Reason: SkipUnsupported, When: "the `url` is none of the forms above (other hosts, git URLs with `tag:`/`revision:`, `/archive/` and `/releases/download/` URLs of hosts not known as Gitea or Forgejo), its tag has no digit, or the version occurs more than three times in it"},
		{Reason: SkipNoSHA256, When: "no `sha256 \"…\"` of 64 hex characters follows the `url` before the next `url` of its block"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"The version of a forge URL is its tag after the non-numeric prefix (`v1.2.3` → `1.2.3`, `jq-1.8.2` → `1.8.2`); the dep's `extractVersion` keeps only tags with the same prefix.",
			"Every occurrence of the version in the URL is replaced together: the tag and the asset name (`releases/download/v1.2.3/tool-1.2.3.tar.gz`).",
			"The deps of one repository share its name, so a formula's per-platform `url`s, their mirrors and its `version` statement are updated in one PR.",
		}},
		{Title: "Checksum", Items: []string{
			"Neither forge archives and assets nor npm tarballs come with a sha256 to look up, so deps downloads each new URL and hashes it (see Checksums); the new `sha256` is written together with the URL.",
			"The `url`s, `mirror`s and `version` statement of one repository in a formula move only together: a new version is proposed only when every one of them has an update to it. When a release lacks the asset of one `url` (renamed, not built for a platform), none is updated, and the run reports `<n> of <m> deps of <name> in <file> resolved to <version>, the others did not; updated none` besides the failed checksum download, rather than a formula whose `version` or platforms disagree.",
		}},
		{Title: "Blocks", Items: []string{
			"`stable`, `on_*` and other blocks are part of the formula; each `resource` block is a scope of its own (its `url`, `sha256`, `mirror` and `version`).",
			"Statements in `head` (built from a branch), `livecheck` (where Homebrew looks for versions), `patch` (pinned patches), `bottle`, `test`, `service` blocks and method bodies (`def`) are not updated; heredocs are skipped.",
		}},
	},
}
