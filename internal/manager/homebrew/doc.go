package homebrew

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the homebrew manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the stable `url` and `sha256` of Homebrew formulae: GitHub tag archives, GitHub release assets and npm registry tarballs.",
	Files:   []string{"`Formula/<name>.rb`, also `Formula/<dir>/<name>.rb` (homebrew-core's sharding), at the repository root"},
	DepTypes: []refdoc.DepType{
		{
			Name: DepType,
			What: "the first `url \"…\"` of the formula (the stable download) and the first `sha256 \"…\"` after it; " +
				"`https://github.com/<owner>/<repo>/archive/[refs/tags/]<tag>.tar.gz` (also `.zip`, `.tgz`): name `<owner>/<repo>`, tags of the repository; " +
				"`https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>`: name `<owner>/<repo>`, its releases; " +
				"`https://registry.npmjs.org/[@<scope>/]<package>/-/<package>-<version>.tgz`: name the package",
			Datasource:  "archive: `git-tags`; release: `github-releases`; npm tarball: `npm`",
			Versioning:  "GitHub: `loose`; npm: `semver`",
			Datasources: []string{"git-tags", "github-releases", "npm"},
		},
	},
	Skips: []refdoc.Skip{
		{Reason: SkipUnsupported, When: "the `url` is none of the forms above (other hosts, git URLs with `tag:`/`revision:`), its tag has no digit, or the version occurs more than three times in it"},
		{Reason: SkipNoSHA256, When: "no `sha256 \"…\"` of 64 hex characters follows the `url` before the next `url`"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"The version of a GitHub URL is its tag after the non-numeric prefix (`v1.2.3` → `1.2.3`, `jq-1.8.2` → `1.8.2`); the dep's `extractVersion` keeps only tags with the same prefix.",
			"Every occurrence of the version in the URL is replaced together: the tag and the asset name (`releases/download/v1.2.3/tool-1.2.3.tar.gz`).",
		}},
		{Title: "Checksum", Items: []string{
			"Neither GitHub archives nor npm tarballs come with a sha256 to look up, so deps downloads the new URL and hashes it (see Checksums); the new `sha256` is written together with the URL.",
			"`resource`, `patch` and `bottle` blocks, `head` and further `url`s are not updated.",
		}},
	},
}
