package poetry

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the poetry manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Poetry dependency tables of `pyproject.toml`, then refreshes `poetry.lock`. " +
		"The file is parsed with byte spans; only the constraint's versions inside the TOML string are rewritten. " +
		"The `python` entry (else `[project] requires-python`) is no dep but drops the releases that do not support all of it (see the `pypi` datasource); `[project]` dependencies of Poetry 2 projects belong to the `pep621` manager.",
	Files: []string{"`pyproject.toml`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`[tool.poetry.dependencies]` (updates use commit type `fix`)", Datasource: "`pypi`", Versioning: "`pep440`"},
		{Name: DepTypeDevDependencies, What: "`[tool.poetry.dev-dependencies]` (Poetry 1.0/1.1)", Datasource: "`pypi`", Versioning: "`pep440`"},
		{Name: DepTypeGroup, What: "`[tool.poetry.group.<name>.dependencies]`", Datasource: "`pypi`", Versioning: "`pep440`"},
	},
	Skips: slices.Concat(pyreq.PoetrySkips, []refdoc.Skip{
		{Reason: pyreq.SkipGit, When: "`{ git = … }`"},
		{Reason: pyreq.SkipLocal, When: "`{ path = … }`"},
		{Reason: pyreq.SkipURL, When: "`{ url = … }`"},
		{Reason: SkipMultiple, When: "an array of constraint tables (per-marker constraints)"},
		{Reason: SkipUnknownSource + " <name>", When: "a `source` that no `[[tool.poetry.source]]` names"},
		pyreq.InRepositorySkip,
	}),
	Sections: []refdoc.Section{
		pyreq.PoetrySection,
		pyreq.RangeSection,
		{Title: "Sources", Items: []string{
			"Values are a constraint string or a table with `version` (`{ version = \"^2.0\", extras = [\"socks\"] }`).",
			"A table's `source = \"<name>\"` looks the dep up on that `[[tool.poetry.source]]` URL only; otherwise the first source with `priority = \"primary\"` or `\"default\"` (or the legacy `default = true`) is used, else `PIP_INDEX_URL` or PyPI (see the `pypi` datasource), and the releases of every `priority = \"supplemental\"` source are merged in. `explicit` sources serve only deps that name them.",
		}},
		pyreq.LockedSection,
		pyreq.LockSection("The lock directory of a `pyproject.toml` is its own directory when it holds `poetry.lock`."),
		pyreq.MaintenanceSection("the `poetry.lock` of each lock directory of a scanned `pyproject.toml` is maintained, also of Poetry 2 projects with only `[project]` dependencies; a directory that also holds `uv.lock` is the `pep621` manager's, whose `uv.lock` is maintained instead.",
			"poetry.lock"),
	},
}
