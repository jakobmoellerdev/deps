package homeassistant

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the homeassistant-manifest manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the PEP 508 `requirements` of Home Assistant integration manifests (`custom_components/<domain>/manifest.json`, or the integrations of Home Assistant core). " +
		"Only the specifier's versions inside the JSON string are rewritten; the file keeps its formatting. There is no lockfile.",
	Files: []string{"`manifest.json` at any depth whose top-level object has string `domain` and `name` members (other `manifest.json` files, such as web app or browser extension manifests, have no deps)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeRequirements, What: "every string of `requirements` (`\"alexapy==1.34.0\"`, `\"dictor>=0.1.12,<0.2\"`); non-string items are ignored", Datasource: "`pypi`; `git-tags` for git tags (`name @ git+https://…@<tag>`)", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
	},
	Skips: slices.Concat(pyreq.SpecifierSkips[:2], []refdoc.Skip{
		{Reason: pyreq.SkipLocate, When: "a JSON string with escapes"},
		{Reason: pyreq.SkipURL, When: "`name @ https://…`"},
		{Reason: pyreq.SkipGit, When: "`name @ git+…` not pinned to a version tag of an http(s) repository"},
		{Reason: pyreq.SkipLocal, When: "`name @ file://…`"},
		{Reason: SkipInvalid, When: "a requirement that does not start with a valid project name"},
	}),
	Sections: []refdoc.Section{
		pyreq.SpecifierSection,
		pyreq.RangeSection,
		{Title: "Git", Items: []string{pyreq.GitURLItem}},
	},
}
