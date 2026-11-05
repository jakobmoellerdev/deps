package gitsubmodules

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the git-submodules manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Moves git submodules to the newest commit of the branch they track, or to the newest version tag when `branch` is one. " +
		"The current commit is the submodule's gitlink in the git index (`git ls-files --stage`). " +
		"Disabled by default, as in Renovate: enable it with a rule `{managers: [git-submodules], ignore: false}`.",
	Files: []string{"`.gitmodules` at any depth; `path` is relative to its directory"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeSubmodule, What: "`[submodule]` with `url`, `path` and `branch` (none: the remote's default branch, `HEAD`; `.`: the superproject's checked-out branch); the name is the URL path (`owner/repo`), the source its `https://` URL without `.git`, the digest the gitlink commit", Datasource: "`git-refs`; `git-tags` when `branch` is a version tag", Versioning: "`tag`", Datasources: []string{dsRefs, dsTags}},
	},
	Skips: []refdoc.Skip{
		{Reason: "no url", When: "the section has no `url`"},
		{Reason: "relative url without a URL origin remote", When: "`url` is relative (`../lib.git`) and `remote.origin.url` is unset, scp-style (`git@host:path`) or a local path"},
		{Reason: "unrecognized url", When: "`url` (resolved against `remote.origin.url` when relative) is not an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: "no path", When: "the section has no `path`"},
		{Reason: "invalid path", When: "`path` is absolute or leaves the scanned directory"},
		{Reason: "not a gitlink in the git index", When: "the git index has no submodule commit at `path`"},
		{Reason: "branch = . with a detached HEAD", When: "`branch = .` while the superproject has no branch checked out"},
		{Reason: "cannot locate branch value", When: "a version-tag `branch` is written with escapes"},
	},
	Sections: []refdoc.Section{
		{Title: "Updates", Items: []string{
			"A submodule tracking a branch gets a `digest` update when the branch's commit differs from the gitlink; `.gitmodules` is unchanged. As in Renovate, such PRs have no release notes or compare link.",
			"A submodule whose `branch` is a version tag (`v1.2.3`) gets updates to newer tags like any `git-tags` dep, with their release notes and compare link: `branch` is rewritten in place and the gitlink moves to the tag's commit. Without a newer tag, a gitlink that is not the tag's commit gets a `digest` update to it.",
			"A relative `url` resolves against `remote.origin.url` like a URL path (Renovate's `new URL(url, origin + \"/\")`): `../lib.git` with origin `https://github.com/acme/app` is `https://github.com/acme/lib.git`.",
			"`run` commits the new gitlink (mode `160000`) without fetching the submodule; `plan` prints it as `Subproject commit <sha>`. " +
				"`apply` stages it with `git update-index --cacheinfo` and leaves the submodule's checkout alone: run `git submodule update` to check the commit out.",
		}},
	},
}
