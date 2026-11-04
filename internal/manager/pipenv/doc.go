package pipenv

import (
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pipenv manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the packages of a `Pipfile`, then refreshes `Pipfile.lock`. " +
		"The file is parsed with byte spans; only the specifier's versions inside the TOML string are rewritten. " +
		"`[requires]` is no dep: `python_full_version` (exactly) else `python_version` (`3.11` → `==3.11.*`) drops the releases that do not support it (see the `pypi` datasource).",
	Files: []string{"`Pipfile`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypePackages, What: "`[packages]` (updates use commit type `fix`)", Datasource: "`pypi`", Versioning: "`pep440`"},
		{Name: DepTypeDevPackages, What: "`[dev-packages]`", Datasource: "`pypi`", Versioning: "`pep440`"},
		{Name: DepTypeCategory, What: "every custom package category (pipenv ≥ 2022.10): any other top-level table except `[source]`, `[requires]`, `[pipenv]` and `[scripts]`, e.g. `[docs]`. Match them in rules with `depTypes: [category]` (plus `paths`/`names`); Renovate uses the category name as dep type", Datasource: "`pypi`", Versioning: "`pep440`"},
	},
	Skips: slices.Concat(pyreq.SpecifierSkips, []refdoc.Skip{
		{Reason: pyreq.SkipGit, When: "`{ git = … }`"},
		{Reason: pyreq.SkipLocal, When: "`{ path = … }`"},
		{Reason: pyreq.SkipURL, When: "`{ file = … }`"},
		{Reason: SkipUnknownIndex + " <name>", When: "an `index` that no `[[source]]` names"},
	}),
	Sections: []refdoc.Section{
		pyreq.SpecifierSection,
		pyreq.RangeSection,
		{Title: "Indexes", Items: []string{
			"Values are a specifier string or a table with `version` (`{ version = \"==2.0\", extras = [\"socks\"] }`).",
			"A table's `index = \"<name>\"` looks the dep up on that `[[source]]` URL; any other dep uses the first `[[source]]`, and with `[pipenv] install_search_all_sources = true` also merges in the releases of the other sources (pipenv ≥ 2022.3 otherwise resolves it on the first only). A source URL with `${VAR}` is not expanded and falls back to `PIP_INDEX_URL` or PyPI (see the `pypi` datasource).",
		}},
		pyreq.LockedSection,
		pyreq.LockSection("The lock directory of a `Pipfile` is its own directory when it holds `Pipfile.lock`."),
	},
}
