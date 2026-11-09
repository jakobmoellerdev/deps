package ant

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// depWhat describes the dependency forms of every dep type.
const depWhat = "`<dependency groupId=\"…\" artifactId=\"…\" version=\"…\"/>` or `<dependency coords=\"group:artifact[:extension[:classifier]]:version[:scope]\"/>` " +
	"(Maven Ant Tasks, Maven Resolver Ant Tasks; any namespace prefix, at any depth) whose scope (`scope` attribute, or the last part of `coords`) is "

// Doc is the reference documentation of the ant manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Maven dependencies of Apache Ant builds that use the Maven Ant Tasks or the Maven Resolver Ant Tasks, as Renovate's ant manager. " +
		"Only the version bytes are rewritten: in the attribute, inside `coords`, or at the definition of the Ant property the version names, also in a properties file or an imported build file.",
	Files: []string{"`build.xml` at any depth", "properties files (`<property file>`) and build files (`<import file>`) they read, which are no deps of their own"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeCompile, What: depWhat + "absent, `compile` or not a Maven scope", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeRuntime, What: depWhat + "`runtime`", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeTest, What: depWhat + "`test`", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeProvided, What: depWhat + "`provided`", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeSystem, What: depWhat + "`system`", Versioning: "`maven`", Datasources: []string{"maven"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "unresolved property <ref>", When: "a `${name}` version no `<property>` or properties file of the build defines (Ant built-ins, `<condition>`/`<available>` properties, properties of files outside the repository); `<ref>` is the `${name}` as written"},
		{Reason: "property cycle <ref>", When: "a `${name}` version whose properties refer to each other in a cycle"},
		{Reason: "unsupported version <value>", When: "a version (or the property value it names) mixing a property with text (`${jetty.version}.v20240101`, `${major}.${minor}`)"},
		{Reason: "unresolved property in <group:artifact>", When: "a groupId or artifactId with a `${name}` no property defines"},
		{Reason: "cannot locate value", When: "a version written with XML entities, or a properties value with escapes or a continuation line"},
		{Reason: "version range", When: "a Maven range Maven rejects (`[2.0,1.0)`); valid ranges are updated as by the maven manager"},
	},
	Sections: []refdoc.Section{
		{Title: "Properties and imports", Items: []string{
			"The build file and the files it imports are read in document order, as Ant does: `<property name=\"…\" value=\"…\"/>` and the keys of `<property file=\"…\"/>` " +
				"(a Java properties file; `prefix` honored) define properties, the first definition of a name wins (Ant properties are immutable), also inside targets.",
			"`<import file=\"…\"/>` reads the build file relative to the importing file; `<property file>` and `settingsFile` paths are relative to the project's `basedir`. " +
				"`${name}` in these paths is expanded with the properties defined so far and `${basedir}`; paths outside the repository and undefined properties are ignored.",
			"A `${name}` version follows `${a}` → `${b}` chains to the final definition, which the update edits; the dep's file and line are then the definition's. " +
				"Deps sharing a definition are updated together (one version, one PR named after the property unless a rule groups them).",
			"A build file imported by several build files (or itself a `build.xml`) yields its deps once.",
		}},
		{Title: "Repositories", Items: []string{
			"Deps are looked up in the repositories of their enclosing element (the nearest one declaring any): `<remoteRepository url=\"…\"/>` children " +
				"(`refid` naming an earlier `<remoteRepository id url>`) and the mirrors and profile repositories of a `settingsFile` Maven settings file, followed by Maven Central; " +
				"without any, Maven Central. A URL with an undefined `${name}` is left out.",
		}},
	},
}
