package pipsetup

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pip_setup manager.
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the requirement string literals of `setup.py` files. As Renovate's pip_setup, the file is lexed as Python but not evaluated: every plain string literal (`'…'` or `\"…\"`, also `r`/`u`-prefixed) that is a PEP 508 requirement with a version specifier (`\"requests>=2.31\"`, `'django[argon2]~=5.0; python_version>=\"3.10\"'`) is a dep, wherever it is written (in `setup()`, a list variable, a dict of extras). " +
		"Literals without specifier or whose specifier does not parse as PEP 440 (entry points such as `'cli=pkg.main:run'`), with escapes, triple-quoted, f-strings and bytes are not, nor are operands of `+`, `*` or `%` and receivers of a method call (`\"x>={}\".format(v)`), so requirements built at runtime or read from files yield no deps. Only the specifier's versions inside the literal are rewritten. A `setup.py` has no lockfile.",
	Files: []string{"any `setup.py`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeInstall, What: "a literal in the `install_requires=` keyword argument", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeSetup, What: "a literal in `setup_requires=`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeTest, What: "a literal in `tests_require=`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeExtra, What: "a literal in `extras_require=`", Versioning: "`pep440`", Datasources: []string{"pypi"}},
		{Name: DepTypeOther, What: "any other requirement literal (a module-level `REQUIRES = [...]` passed by name; Renovate sets no dep type)", Versioning: "`pep440`", Datasources: []string{"pypi"}},
	},
	Skips: []refdoc.Skip{
		pyreq.SpecifierSkips[0],
		{Reason: SkipIgnored, When: "the literal is followed on its line (after an optional `,`) by a `# renovate: ignore` comment"},
	},
	Sections: []refdoc.Section{pyreq.SpecifierSection, pyreq.RangeSection},
}
