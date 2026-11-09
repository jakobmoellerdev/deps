package bitrise

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the bitrise manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates step versions in Bitrise configurations.",
	Files: []string{
		"`bitrise.y(a)ml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeStep, What: "the step references of `workflows.<name>.steps` and `step_bundles.<name>.steps` (also inside `with` groups): `<id>@<version>` (step library `default_step_lib_source`, else Bitrise's), `<steplib>::<id>@<version>`, and `git::<url>@<tag>` (Source the repository)", Datasource: "`bitrise`; `git-tags` for `git::`", Versioning: "`tag`", Datasources: []string{"bitrise", "git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "no version", When: "a step without `@<version>` (it runs the newest version)"},
		{Reason: "local path", When: "`path::<dir>`"},
		{Reason: "invalid step", When: "a `git::` reference that is no `http(s)://` or `git@<host>:<path>` URL"},
		{Reason: "cannot locate value", When: "the reference bytes were not found (escapes)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Only the version after `@` is rewritten. A partial version (`script@1`, Bitrise's newest 1.x) keeps its precision: the bitrise datasource lists derived `X.Y` and `X` lines.",
			"A git step's tag that is no version (a branch such as `@master`) is skipped by the engine as a non-version value.",
			"`bundle::<name>` references a step bundle, whose steps are extracted where `step_bundles` defines them.",
		}},
	},
}
