package setupcfg

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the setup-cfg manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the PEP 508 requirements of setuptools' declarative configuration in `setup.cfg`. " +
		"A requirement value is the rest of its key line plus the indented continuation lines, one requirement per line (`;`-separated on the key line, as setuptools' `list-semi`); only the specifier's versions are rewritten. " +
		"`#`/`;` comment lines and inline `#` comments are ignored, and so are `file:` and `attr:` directives (a `file: requirements.txt` is read by the `pip_requirements` manager when its name matches). " +
		"`[options] python_requires` is no dep but drops the releases that do not support all of it (see the `pypi` datasource). A `setup.cfg` has no lockfile.",
	Files: []string{"any `setup.cfg`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeInstall, What: "`[options] install_requires` (updates use commit type `fix`)", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeSetup, What: "`[options] setup_requires`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeTest, What: "`[options] tests_require`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeExtra, What: "every key of `[options.extras_require]`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
	},
	Skips: append(append([]refdoc.Skip{}, pyreq.SpecifierSkips[:2]...),
		refdoc.Skip{Reason: pyreq.SkipURL, When: "`name @ https://…`"},
		refdoc.Skip{Reason: pyreq.SkipGit, When: "`name @ git+…`"},
		refdoc.Skip{Reason: pyreq.SkipLocal, When: "`name @ file://…`"},
	),
	Sections: []refdoc.Section{pyreq.SpecifierSection, pyreq.RangeSection},
}
