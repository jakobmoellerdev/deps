package maven

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the maven manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the versions of dependencies, plugins, build extensions, annotation processor paths and the parent in `pom.xml`. " +
		"The pom is parsed into an element tree that keeps the byte span of each element's text; only the version bytes (or the property value) are replaced. " +
		"A `${name}` version is resolved through `.mvn/maven.config` `-D` definitions, the pom's properties, its active profiles and its parent poms in the repository, so the update edits the definition, also in another file. " +
		"Elements without a `<version>` (managed by a parent or BOM) are not deps.",
	Files: []string{"`pom.xml`, `*.pom.xml`", "`.mvn/extensions.xml`", "`.mvn/maven.config` (property definitions only, no deps of its own)"},
	DepTypes: []refdoc.DepType{
		{Name: "compile", What: "`<dependency>` without a `<scope>` or with `compile`, in `<dependencies>` or `<dependencyManagement>` (updates use commit type `fix`)", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "provided", What: "`<dependency>` with `<scope>provided</scope>` (commit type `fix`)", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "runtime", What: "`<dependency>` with `<scope>runtime</scope>` (commit type `fix`)", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "test", What: "`<dependency>` with `<scope>test</scope>`", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "system", What: "`<dependency>` with `<scope>system</scope>` (commit type `fix`)", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "import", What: "`<dependencyManagement>` BOM with `<scope>import</scope>` (commit type `fix`)", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "parent", What: "`<project><parent>` (commit type `fix`)", Datasource: "`maven`", Versioning: "`maven`"},
		{Name: "build", What: "`<plugin>` (default groupId `org.apache.maven.plugins`) and its `<dependency>` elements, `<extension>`, `<annotationProcessorPaths><path>`", Datasource: "`maven`", Versioning: "`maven`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "unresolved property <ref>", When: "a `${name}` version defined neither in the pom nor in its parent poms in the repository, when the topmost of them has no `<parent>`, or a Maven built-in such as `${project.version}`; `<ref>` is the `${name}` as written"},
		{Reason: "property <ref> not defined in this repository", When: "a `${name}` version defined neither in the pom nor in its parent poms in the repository, when the topmost of them has a `<parent>` outside the repository: the property comes from that remote parent pom, which the repository cannot edit (the `parent` dep's update moves it)"},
		{Reason: "property <ref> only defined in profiles", When: "a `${name}` version whose property only profiles define, none of them active or several active ones (see Profiles); a dep inside a profile also sees its own profile's properties"},
		{Reason: "unresolved property in <group:artifact>", When: "a groupId or artifactId with a `${…}` that neither the properties (as for versions) nor `project.groupId`/`project.artifactId` resolve"},
		{Reason: "unsupported version <value>", When: "a version mixing a property with text (`${guava.version}-jre`, `${revision}${changelist}`)"},
		{Reason: "version range", When: "`[1.0,2.0)`, `(,1.0]`"},
		{Reason: "cannot locate value", When: "a version written with entities, CDATA or split by a comment"},
		{Reason: "internal module", When: "the groupId:artifactId of a pom extracted in the same run (a module or parent of the repository); applied after property resolution"},
		{Reason: "m2e lifecycle mapping (not a published plugin)", When: "`org.eclipse.m2e:lifecycle-mapping`, the Eclipse m2e settings pseudo-plugin (no repository publishes it: Central answers 404 for its `maven-metadata.xml`)"},
		{Reason: "system jar in repository", When: "a `<scope>system</scope>` dep whose `<systemPath>` is relative or below `${basedir}`/`${project.basedir}`: a jar checked into the repository, which no repository publishes"},
	},
	Sections: []refdoc.Section{
		{Title: "Properties", Items: []string{
			"A version `${name}` is looked up, following `${a}` → `${b}` chains: in the `-D` definitions of `.mvn/maven.config` (user properties override the pom's, as on Maven's command line), " +
				"the `<properties>` of the dep's `<profile>` (for a dep declared in one), the only active profile defining it (several active ones: none), the pom's top-level `<properties>`, " +
				"then likewise in each parent pom of the repository, nearest first; the nearest definition wins.",
			"The maven.config of a pom is the one of its multi-module project directory: the nearest ancestor directory with a `.mvn/` file deps scans (`maven.config` or `extensions.xml`). " +
				"It takes `-Dname=value`, `-D name=value` (also on two lines, Maven 3.9's one argument per line), `--define name=value`, `--define=name=value`, quoted values, `#` comment lines and `-P`/`--activate-profiles` lists (`!id`/`-id` deactivates, `?id` optional).",
			"A parent pom is in the repository when it was scanned: the pom at `<parent><relativePath>` (default `../pom.xml`, a directory means its `pom.xml`) if its groupId:artifactId matches, " +
				"else the scanned pom with the parent's groupId:artifactId:version (properties such as `${revision}` expanded), else groupId:artifactId. An empty `<relativePath/>` means the parent is remote.",
			"The update edits the final definition of the chain, which may be in a parent pom or maven.config: the dep's file and line are then the definition's. The dep's version reference is the property name.",
			"Deps sharing a definition (`${jackson.version}`, in one pom or across modules) are updated together: one version, one PR named after the property unless a rule groups them (`group:`).",
			"Properties of remote parent poms are not read: they live in published, immutable poms (https://central.sonatype.org/publish/requirements/immutability/) that only the `parent` dep's update moves; " +
				"Renovate likewise resolves parents only among the repository's package files (`resolveParents` in lib/modules/manager/maven/extract.ts). `-D` flags on the actual command line and environment variables are not known.",
		}},
		{Title: "Profiles", Items: []string{
			"A profile is active when maven.config's `-P` selects it, or when it has conditions and all of them hold, unless `-P` deactivates it; when none is, the `<activeByDefault>true` ones are (as in Maven).",
			"`<property><name>x</name>` holds when maven.config defines `x` non-empty (`!x`: when it does not); with `<value>v</value>`, when its value is `v` (`!v`: differs, also when unset).",
			"`<file><exists>`/`<missing>` is checked in the repository, relative to the pom's directory (`${basedir}`/`${project.basedir}` expanded); absolute paths, paths outside the repository and other properties do not hold.",
			"`<jdk>`, `<os>` and other conditions depend on the machine running Maven and never hold, so their profiles' properties do not override (see https://maven.apache.org/guides/introduction/introduction-to-profiles.html).",
		}},
		{Title: "Repositories", Items: []string{
			"Deps are looked up in the pom's `<repositories>` (plugins and extensions: `<pluginRepositories>`), then those of its parent poms in the repository (a repeated id or URL is used once, the child's first), " +
				"then Maven Central (`central=https://repo.maven.apache.org/maven2/`), unless a repository is Central or has the id `central`.",
			"Each repository is passed to the datasource as `<id>=<url>/`. Without repositories, only Maven Central is used. URLs with properties are ignored.",
			"`settings.xml` mirrors, credentials and active-profile repositories are applied by the maven datasource (see its docs), matched by repository id.",
		}},
		{Title: "Replacements", Body: "Dependencies, plugins with an explicit `groupId`, extensions, annotation processor paths and the parent can be [replaced](configuration.md#rulesreplacementname) by other `groupId:artifactId` coordinates when both are written literally; only the changed elements are rewritten. A replacement version is not written to a version property (`${...}`) other dependencies may share: such deps keep their version or are skipped."},
		{Title: "Versioning", Body: "`maven` (Maven's ComparableVersion): `1.0` = `1.0.0` = `1.0-final`, `alpha` < `beta` < `milestone` < `rc` < `snapshot` < release < `sp`. " +
			"SNAPSHOTs are never proposed, other prereleases only from a prerelease, and the first unknown qualifier after a `-` must match (`33.3.1-jre` stays on `-jre`). " +
			"Deps sharing a property take the highest version all of them reach. " +
			"See [`rules[].versioning`](configuration.md#rulesversioning)."},
	},
}
