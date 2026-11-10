package precommit

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pre-commit manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the `rev` of the hook repositories in pre-commit configurations (tags, SHAs frozen by `pre-commit autoupdate --freeze` and bare SHAs), " +
		"and the `additional_dependencies` of hooks that declare `language: node`, `python` or `golang`.",
	Files: []string{"`.pre-commit-config.yaml` / `.pre-commit-config.yml` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeRepository, What: "`repos[]` entry with `repo` and `rev`; the name is the URL path (`owner/repo`), the source its `https://` URL without `.git` on any git host (see Repositories)", Versioning: "`tag`", Datasources: []string{"git-tags"}},
		{Name: DepTypeNode, What: "`additional_dependencies` item `<name>@<range>` (`prettier@^3.6.2`, `@scope/plugin@1.2.0`) of a hook with `language: node`", Versioning: "`semver`", Datasources: []string{"npm"}},
		{Name: DepTypePython, What: "`additional_dependencies` item, a PEP 508 requirement (`black==26.1.0`, `rich>=13.6.0`), of a hook with `language: python`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeGolang, What: "`additional_dependencies` item `<package or module path>@<version>` (`github.com/wasilibs/go-shellcheck/cmd/shellcheck@v0.11.1`) of a hook with `language: golang`", Versioning: "`go`", Datasources: []string{"go"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "local repository", When: "`repo: local` (its hooks' additional_dependencies are extracted)"},
		{Reason: "meta repository", When: "`repo: meta`"},
		{Reason: "unrecognized repo", When: "`repo` is not an https/http/ssh/git or `git@host:path` URL (a local path, `file://`)"},
		{Reason: "no rev", When: "the entry has no `rev`"},
		{Reason: "pinned without version comment", When: "`rev` is a 40-hex SHA followed by a comment that is no version (`# fix for #651`)"},
		{Reason: "non-version ref", When: "`rev` is neither a SHA nor a version (a branch such as `main`)"},
		{Reason: skipLocateValue, When: "the rev or additional dependency bytes were not found (block scalars, escapes)"},
		{Reason: SkipNoVersion, When: "a node or golang additional dependency without `@<version>`"},
		{Reason: SkipModulePath, When: "a golang additional dependency whose path is no import path (`./tools@v1`)"},
		{Reason: pyreq.SkipUnconstrained, When: "a python additional dependency without specifier (`pyyaml`)"},
		{Reason: pyreq.SkipRange, When: "a python specifier that does not parse; a node range outside npm's range grammar"},
		{Reason: pyreq.SkipURL, When: "`name @ https://…`; a node `http(s)://` tarball"},
		{Reason: pyreq.SkipGit, When: "`name @ git+…`; a node git dependency"},
		{Reason: pyreq.SkipLocal, When: "`name @ file://…`; a node `file:`/`link:` dependency"},
		{Reason: "npm alias", When: "a node `<name>@npm:<other>@<range>`"},
		{Reason: "tag", When: "a node dist-tag (`prettier@latest`)"},
		{Reason: SkipInvalid, When: "a python additional dependency that does not start with a valid project name"},
	},
	Sections: []refdoc.Section{
		{Title: "Revs", Items: []string{
			"A tag (`v4.5.0`) is updated to newer tags. A branch (`main`) and a suffixed version (`v4.0.0-alpha.8`) are skipped (`non-version ref`), as for github-actions refs: `tag` versioning has no suffix order that is safe on hook mirrors (semver sorts mirrors-prettier's `v4.0.0-alpha.3-1` above its `v4.0.0-alpha.8`). Renovate skips branches too.",
			"A SHA with `# frozen: <version>` (written by `pre-commit autoupdate --freeze`) or `# <version>` is at that version: it is updated like a tag, writing the new SHA and version.",
			"A SHA with `# frozen: <tag>` where the tag is no tag version (lychee's `nightly`, `v4.0.0-alpha.8`) follows that tag: when the tag moves, a `digest` update writes its new commit. A tag that no longer exists skips the dep.",
			"A bare SHA without a comment is a commit pin, resolved as a github-actions one: a SHA a tag points at is that tag's version (the newest one) and is updated like a frozen SHA, writing ` # frozen: <version>` after the new SHA. A SHA no tag points at is ignored by default (`ignored by default (commit pin without version; …)`), as Renovate disables such unversioned references; a rule setting `ignore: false` enables a `digest` update to the commit of the repository's default branch, without a comment.",
			"A SHA with any other comment is skipped: the comment may say why that commit is pinned.",
		}},
		{Title: "Repositories", Items: []string{
			"Every git host is looked up the same way (`git-tags`, the smart HTTP protocol): GitHub, GitLab (with subgroups), Codeberg/Forgejo/Gitea, Bitbucket, sourcehut (`https://git.sr.ht/~user/repo`) and self-hosted servers need no host rule, unlike Renovate, which skips (`unknown-registry`) a host it does not detect as GitHub, GitLab or Forgejo unless a host rule names it.",
			"An `http(s)://` URL keeps its scheme and port (`https://git.example.com:8443/team/hooks`); `ssh://` and `git@host:path` URLs are looked up over https on the default port.",
			"A repository at the top level of its host (`https://pagure.io/<project>`) is named by its one path element.",
		}},
		{Title: "Additional dependencies", Items: []string{
			"As in Renovate, only hooks that declare `language: node`, `python` or `golang` in the configuration are read: other languages and hooks whose language the hook repository's `.pre-commit-hooks.yaml` defines are not. Hooks of `repo: meta` are not read; those of `repo: local` are.",
			"`node`: `<name>@<range>` is an npm dependency-section range (see the npm manager's Ranges): its versions, skips and rewrites are the npm manager's, without a lockfile.",
			"`python`: a PEP 508 requirement as in requirements files (see Specifiers and Ranges below); markers and extras are kept.",
			"`golang`: `<path>@<version>` as `go install` takes it. The path may name a package rather than a module: the `go` datasource resolves it as the go command does, to its longest prefix whose lookup lists versions (`github.com/wasilibs/go-shellcheck/cmd/shellcheck` → module `github.com/wasilibs/go-shellcheck`). The version is rewritten in place; major module path updates are not made.",
			"Security advisories (`builtins.security`) are looked up for python additional dependencies (OSV queries PyPI deps of every manager), not for node and golang ones.",
			"There is no lockfile: under the default `rangeStrategy` (`auto` = `update-lockfile`) a version a range admits is not proposed, and one outside it rewrites the range (`black==26.1.0` → `black==26.3.1`, `prettier@^3.6.2` → `prettier@^4.0.0`); see [`rangeStrategy`](configuration.md#rangestrategy).",
			"`pin: digest` pins hook repositories only: npm, PyPI and Go packages have no digest.",
		}},
		pyreq.SpecifierSection,
		pyreq.RangeSection,
		{Title: "Edits", Items: []string{
			"Tag revs are replaced in place, keeping quotes and comments.",
			"Frozen revs get the new SHA and the version in the `# frozen:` comment is rewritten.",
			"Pinning a tag rev writes the SHA and inserts ` # frozen: <version>` after the value; so does an update of a bare SHA the engine resolved to a tag.",
			"Additional dependencies have their range, specifier or version rewritten inside the YAML string; quotes and the rest of the list are kept.",
		}},
	},
}
