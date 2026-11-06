package sbt

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// libraryWhat describes the ModuleID forms of the library dep types.
const libraryWhat = "ModuleIDs `\"group\" % \"artifact\" % \"version\"` (`%%`/`%%%` append the build's Scala binary version to the artifact: `cats-core_2.13`) anywhere in a file " +
	"(`libraryDependencies += …`, `Seq(…)`, `val x = …`), whose `% <configuration>` is "

// Doc is the reference documentation of the sbt manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates sbt builds as Renovate's sbt manager: library and plugin ModuleIDs of `*.sbt` and `project/*.scala` files, `scalaVersion`, and `sbt.version` of `project/build.properties`. " +
		"Files are read with a small Scala lexer (strings, comments, objects, `val`s); versions are rewritten where they are written, also when a `val` in another file of the build defines them.",
	Files: []string{"`*.sbt` at any depth", "`project/*.scala`", "`project/build.properties`", "`repositories` at the root (sbt's proxy repositories file)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeCompile, What: libraryWhat + "absent or not one below (`Compile`, `\"compile\"`, custom configurations)", Datasource: "`sbt-package`", Versioning: "`maven`", Datasources: []string{"sbt-package"}},
		{Name: DepTypeTest, What: libraryWhat + "`Test`/`\"test\"` (the first configuration of `\"test->test\"`, `\"test;provided\"`)", Datasource: "`sbt-package`", Versioning: "`maven`", Datasources: []string{"sbt-package"}},
		{Name: DepTypeProvided, What: libraryWhat + "`Provided`/`\"provided\"`", Datasource: "`sbt-package`", Versioning: "`maven`", Datasources: []string{"sbt-package"}},
		{Name: DepTypeRuntime, What: libraryWhat + "`Runtime`/`\"runtime\"`", Datasource: "`sbt-package`", Versioning: "`maven`", Datasources: []string{"sbt-package"}},
		{Name: DepTypeOptional, What: libraryWhat + "`Optional`/`\"optional\"`", Datasource: "`sbt-package`", Versioning: "`maven`", Datasources: []string{"sbt-package"}},
		{Name: DepTypeIt, What: libraryWhat + "`IntegrationTest`/`\"it\"`", Datasource: "`sbt-package`", Versioning: "`maven`", Datasources: []string{"sbt-package"}},
		{Name: DepTypePlugin, What: "`addSbtPlugin(\"group\" % \"artifact\" % \"version\")` (named `group:artifact`, looked up for the build's sbt version), and compiler plugins `addCompilerPlugin(…)`/`compilerPlugin(…)` (cross-versioned as libraries)", Datasource: "`sbt-plugin` (addSbtPlugin), `sbt-package` (compiler plugins)", Versioning: "`maven`", Datasources: []string{"sbt-plugin", "sbt-package"}},
		{Name: DepTypeScala, What: "`scalaVersion := \"2.13.16\"` (also `ThisBuild / scalaVersion`, `scalaVersion in ThisBuild`, a `val`, `<seq>.head`): `org.scala-lang:scala-library`, `org.scala-lang:scala3-library_3` for Scala 3", Datasource: "`maven`", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeSbt, What: "`sbt.version` of `project/build.properties` (name `sbt/sbt`)", Datasource: "`github-releases` (`sbt/sbt`)", Versioning: "`semver`", Datasources: []string{"github-releases"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNonLiteral, When: "a version that is an expression (`sys.props.getOrElse(…)`, `(v + \".0\")`), an interpolated or escaped string"},
		{Reason: "unresolved version <ref>", When: "a version reference no `val` of the build defines as a string literal (`scalaVersion.value`, a setting key)"},
		{Reason: "ambiguous version <ref>", When: "a version reference that neither an enclosing object nor a qualified path defines and that several definitions of the build end with (`cats` with `Compile.cats` and `Test.cats`)"},
		{Reason: skipDynamic, When: "an Ivy dynamic revision: `latest.integration`, `1.+`, `[1.0,2.0)`"},
		{Reason: skipScalaVersion, When: "a `%%`/`%%%` (or `CrossVersion.full`) ModuleID of a build whose `scalaVersion` and `crossScalaVersions` settings do not resolve to a literal"},
		{Reason: skipCoordinates, When: "only when a file is extracted alone: a ModuleID whose group or artifact refers to a `val` of another file; with the whole build, ModuleIDs whose group or artifact is not a string are not deps"},
	},
	Sections: []refdoc.Section{
		{Title: "Builds and definitions", Items: []string{
			"A build is a directory with a `project/` directory (else the directory of a `*.sbt` file): its `*.sbt` files and those of subdirectories, `project/*.scala`, `project/*.sbt` and `project/build.properties`.",
			"Versions, groups and artifacts may be `val`/`lazy val`/`var`/parameterless `def` references, also qualified by objects (`Dependencies.Versions.cats`, `V.cats` for an anonymous `val V = new { val cats = … }`) and across the files of a build; " +
				"`val a = b` chains, `<module>.revision` (the version of another ModuleID `val`) and `<seq>.head` are followed. The update edits the definition; deps sharing it are grouped into one PR.",
			"The Scala binary version of `%%` is that of the first `scalaVersion` that resolves to a literal (`*.sbt` at the build root first, then `project/*.scala`, then other `*.sbt`), else of the first element of `crossScalaVersions`; " +
				"without either setting sbt's default (Scala 2.12 for sbt 1.x, Scala 3 for sbt 2.x). `.cross(CrossVersion.full)` appends the full Scala version, `for3Use2_13` and `for2_13Use3` swap 3 and 2.13, `disabled` none. " +
				"Deps of `project/*.sbt` (the meta-build) use sbt's own Scala version.",
			"Scala releases are compared with the `maven` versioning; Scala 3.8+ is also published as `scala-library`, so a Scala 2 project sees it as a major update (as in Renovate); a rule `{names: [org.scala-lang:scala-library], updates: [minor, patch]}` keeps Scala 2.",
		}},
		{Title: "Repositories", Items: []string{
			"Libraries are looked up in Maven Central, then the `resolvers += \"name\" at \"url\"` (also `++= Seq(…)`) of the build's files; `project/*.sbt` resolvers apply to the meta-build's deps.",
			"Plugins are looked up in the sbt plugin releases repository (Ivy layout) and Maven Central, then the meta-build's resolvers. A root `repositories` file (sbt's `[repositories]` proxies: `maven-central`, `name: url[, ivy pattern]`) replaces these defaults for every build; entries with an `[organization]` pattern are Ivy repositories.",
			"`scalaVersion` deps use the library repositories with the `maven` datasource.",
		}},
	},
}

// ScalafmtDoc is the reference documentation of the scalafmt manager.
var ScalafmtDoc = refdoc.Manager{
	Name:    ScalafmtName,
	Summary: "Updates the scalafmt version of `.scalafmt.conf` (the top-level `version = 3.8.3` key, quoted or not), as Renovate's scalafmt manager.",
	Files:   []string{"`.scalafmt.conf` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeScalafmt, What: "the `version` key (name `scalameta/scalafmt`)", Datasource: "`github-releases` (`scalameta/scalafmt`)", Versioning: "`semver`", Datasources: []string{"github-releases"}},
	},
}
