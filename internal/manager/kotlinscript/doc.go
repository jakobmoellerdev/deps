package kotlinscript

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the kotlin-script manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Maven dependencies of Kotlin scripts, as Renovate's kotlin-script manager: the coordinates of `@file:DependsOn(…)` annotations, " +
		"looked up in the repositories of `@file:Repository(…)`. Only the version inside the string is rewritten.",
	Files: []string{"`*.main.kts` at any depth (other `*.kts` files, such as `build.gradle.kts`, are not Kotlin scripts of this kind)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "every string argument of `@file:DependsOn(\"group:artifact:version\", …)` (also spread over lines, with spaces around `:` and the parenthesis, or a qualified annotation name) " +
			"that is a Maven coordinate `group:artifact[:extension[:classifier]]:version`, named `group:artifact`; annotations in comments and strings are ignored, " +
			"other strings (a jar path, a coordinate without group, artifact or version) are not deps", Versioning: "`maven`", Datasources: []string{"maven"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipTemplate, When: "a coordinate with a `$` template or an escape (`\"org.jetbrains:kotlinx-html:$version\"`)"},
		{Reason: skipRange, When: "a Maven version range: `[1.0,2.0)`"},
		{Reason: skipVersion + "<v>", When: "Maven's `LATEST` or `RELEASE` meta versions"},
	},
	Sections: []refdoc.Section{
		{Title: "Repositories", Items: []string{
			"Like Kotlin's resolver, every dep is looked up in the repositories of all `@file:Repository(\"url\", …)` annotations of the file, in file order (also those after the `DependsOn`), " +
				"and in Maven Central only when the file has none; scripts list Central (`https://repo.maven.apache.org/maven2/`) next to other repositories to keep it. " +
				"Local repositories (`file://…`) cannot be queried and are left out; a script with only those is looked up in Maven Central.",
		}},
	},
}
