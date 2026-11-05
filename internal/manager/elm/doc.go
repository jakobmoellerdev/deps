package elm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the elm manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the package dependencies and the compiler version of Elm 0.19 projects (`elm.json`): the exact versions of an application and the constraints of a package. " +
		"Only the version string is rewritten; the file keeps its formatting. No Elm toolchain is needed.",
	Files: []string{"`elm.json` at any depth; its `type` (`application` or `package`) decides how it is read, other files have no deps"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDirect, What: "application `dependencies.direct` (`\"elm/json\": \"1.1.3\"`)", Datasource: "`elm-package`", Versioning: "`semver`"},
		{Name: DepTypeIndirect, What: "application `dependencies.indirect`; ignored by default (`ignore: false` opts in)", Datasource: "`elm-package`", Versioning: "`semver`"},
		{Name: DepTypeTestDirect, What: "application `test-dependencies.direct`", Datasource: "`elm-package`", Versioning: "`semver`"},
		{Name: DepTypeTestIndirect, What: "application `test-dependencies.indirect`; ignored by default (`ignore: false` opts in)", Datasource: "`elm-package`", Versioning: "`semver`"},
		{Name: DepTypeDependencies, What: "package `dependencies` constraints (`\"elm/core\": \"1.0.0 <= v < 2.0.0\"`)", Datasource: "`elm-package`", Versioning: "`semver`; the constraint is the dep's range, its lower bound the current version"},
		{Name: DepTypeTestDependencies, What: "package `test-dependencies` constraints", Datasource: "`elm-package`", Versioning: "`semver`, as `dependencies`"},
		{Name: DepTypeElmVersion, What: "`elm-version`: the compiler version of an application (`0.19.1`), the compiler constraint of a package (`0.19.0 <= v < 0.20.0`); the name is `elm/compiler`", Datasource: "`github-releases` (`elm/compiler`)", Versioning: "`semver`"},
	},
	Skips: []refdoc.Skip{
		{Reason: skipName, When: "a dependency name that is not `author/project`"},
		{Reason: skipVersion, When: "an application version that is not `MAJOR.MINOR.PATCH`, or a value with JSON escapes"},
		{Reason: skipConstraint, When: "a package constraint outside Elm's `<lower> <op> v <op> <upper>` grammar (`<` or `<=`, `MAJOR.MINOR.PATCH` bounds)"},
	},
	Sections: []refdoc.Section{
		{Title: "Applications", Items: []string{
			"Every version is exact and replaced by the new one. The `indirect` versions pin the dependencies of the `direct` ones, which `elm make` checks against their constraints: they are ignored by default, as Go's indirect requirements, and a direct update whose new version needs newer indirect dependencies fails `elm make` until they are raised (`elm install` does that).",
		}},
		{Title: "Package constraints", Items: []string{
			"`rangeStrategy` applies to package constraints (`dependencies`, `test-dependencies`, `elm-version`). Packages have no lockfile, so versions a constraint admits are not proposed except under `bump`.",
			"`auto` (default) is `widen`, Dependabot's update of Elm packages: the lower bound stays and an upper bound the new version reaches moves past it (`1.0.0 <= v < 2.0.0` → `1.0.0 <= v < 3.0.0` for 2.1.0).",
			"`replace` and `update-lockfile` also set the lower bound to the new version (`2.1.0 <= v < 3.0.0`); `bump` does so for admitted versions too (`1.0.0 <= v < 2.0.0` → `1.2.0 <= v < 2.0.0`). An exclusive upper bound moves to the next boundary of its precision above the new version (`< 2.0.0` → `< 3.0.0`, `< 1.5.0` → `< 1.8.0` for 1.7.2), an inclusive one to the new version. `pin` writes `2.1.0 <= v <= 2.1.0`.",
		}},
	},
}
