package pep621

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pep621 manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the PEP 508 requirement strings of `pyproject.toml` (PEP 621 project metadata, PEP 735 dependency groups, uv's and pdm's dev dependencies), then refreshes `uv.lock`, `poetry.lock` or `pdm.lock`. " +
		"The file is parsed with byte spans; only the specifier's versions inside the TOML string are rewritten. " +
		"`include-group` entries of dependency groups name another group, whose requirements are extracted there; `[project] requires-python` is no dep but drops the releases that do not support all of it (see the `pypi` datasource).",
	Files: []string{"`pyproject.toml`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`[project] dependencies` (updates use commit type `fix`)", Datasource: "`pypi`; `git-tags` for git tags (see Git)", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeOptional, What: "every array of `[project.optional-dependencies]`", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeGroups, What: "every array of `[dependency-groups]` (PEP 735)", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeUVDev, What: "`[tool.uv] dev-dependencies`", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypePDMDev, What: "every array of `[tool.pdm.dev-dependencies]` (editable `-e …` entries are ignored)", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeBuildSystem, What: "`[build-system] requires`: the build backend and its plugins (`hatchling>=1.18`, `setuptools-scm>=8`)", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeUVConstraint, What: "`[tool.uv] constraint-dependencies` (not in Renovate; ignored by default, as these lists usually cap versions on purpose: a rule with `ignore: false` enables them)", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeUVOverride, What: "`[tool.uv] override-dependencies` (not in Renovate; ignored by default, as these lists usually cap versions on purpose: a rule with `ignore: false` enables them)", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
		{Name: DepTypeUVBuildConstraint, What: "`[tool.uv] build-constraint-dependencies`, string entries (not in Renovate; ignored by default, as these lists usually cap versions on purpose: a rule with `ignore: false` enables them)", Datasource: "`pypi`; `git-tags` for git tags", Versioning: "`pep440`; `tag` for git tags", Datasources: []string{"pypi", "git-tags"}},
	},
	Skips: slices.Concat(pyreq.SpecifierSkips, []refdoc.Skip{
		{Reason: pyreq.SkipURL, When: "`name @ https://…`, or a `[tool.uv.sources]` entry with `url`"},
		{Reason: pyreq.SkipGit, When: "`name @ git+…`, or a `[tool.uv.sources]` entry with `git`, that is not pinned to a version tag of an http(s) repository (see Git)"},
		{Reason: pyreq.SkipLocal, When: "`name @ file://…`, or a `[tool.uv.sources]` entry with `path`"},
		{Reason: SkipWorkspace, When: "a `[tool.uv.sources]` entry with `workspace = true`"},
		{Reason: SkipMultipleSources, When: "a `[tool.uv.sources]` entry that is an array (per-marker sources)"},
		{Reason: SkipUnknownIndex + " <name>", When: "a `[tool.uv.sources]` `index` that no `[[tool.uv.index]]` names"},
		pyreq.InRepositorySkip,
	}),
	Sections: []refdoc.Section{
		pyreq.SpecifierSection,
		pyreq.RangeSection,
		{Title: "Indexes", Items: []string{
			"A `[tool.uv.sources]` entry `{ index = \"<name>\" }` looks the dep up on that `[[tool.uv.index]]` URL only; otherwise the first index with `default = true` is used, else `PIP_INDEX_URL` or PyPI (see the `pypi` datasource), and the releases of every `[[tool.uv.index]]` that is neither `default` nor `explicit = true` are merged in.",
			"`[[tool.pdm.source]]` indexes are searched like pdm does: the source named `pypi` replaces PyPI, the other sources are extra indexes whose releases are merged in; a source whose `include_packages` match the (normalized) name is the only one searched for that dep, and one whose `exclude_packages` match is not searched for it. `type = \"find_links\"` sources are ignored, and so are URLs with `${VAR}` (a `pypi` one falls back to `PIP_INDEX_URL` or PyPI). A `[tool.uv.sources]` index takes precedence.",
			"Poetry 2 projects declare `[project] dependencies` too: they are extracted here, while `[tool.poetry.*]` tables belong to the `poetry` manager; both lock through `poetry.lock`.",
		}},
		{Title: "Git", Items: []string{
			pyreq.GitURLItem,
			"A `[tool.uv.sources]` entry `{ git = \"https://…\", tag = \"v1.2.0\" }` makes the dep (also an unconstrained one) a `git-tags` dep whose `tag` in the source table is replaced; `branch` and `rev` are not updated.",
		}},
		pyreq.LockedSection,
		pyreq.LockSection("The lock directory of a `pyproject.toml` is its own directory when it holds `uv.lock` (uv), `poetry.lock` (Poetry) or `pdm.lock` (pdm)."),
		pyreq.MaintenanceSection("the lockfile of each lock directory of a scanned `pyproject.toml` (a uv workspace's root, as above) that post-processing updates is maintained when it is `uv.lock` or `pdm.lock` (`uv.lock` comes first); a `poetry.lock` is maintained by the `poetry` manager.",
			"uv.lock", "pdm.lock"),
	},
}
