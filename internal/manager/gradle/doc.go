package gradle

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gradle manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates Maven dependencies and Gradle plugins in Gradle version catalogs, build scripts, `gradle.properties` and `buildSrc`/convention-plugin sources. " +
		"Catalogs are parsed as TOML 1.0 (multi-line inline tables of TOML 1.1 too): `[versions]`, `[libraries]` and `[plugins]` with string notation (`\"group:name:version\"`, `\"id:version\"`), " +
		"inline tables (`module`, `group` + `name`, `id`; `version = \"x\"`, `version.ref = \"k\"`, `version = { ref = \"k\" }`), dotted keys and `[libraries.<alias>]` tables; " +
		"other tables such as `[bundles]` are ignored, and a catalog that is not valid TOML fails the file, as it fails Gradle. " +
		"Entries without a version (managed by a platform/BOM) are not deps. A `version.ref` dep carries the span of the referenced `[versions]` entry and its key as the shared version name. " +
		"Build scripts (Groovy and Kotlin DSL) are matched by regular expression on their code, outside of comments and string contents; " +
		"versions may come from variables defined in the same build (see Variables), dynamic versions are read from `gradle.lockfile` (see Lockfiles and dynamic versions), " +
		"and the repositories a build declares are queried (see Repositories).",
	Files: []string{
		"`*.versions.toml` (default `gradle/libs.versions.toml`)",
		"`*.gradle`, `*.gradle.kts` (`build.gradle(.kts)`, `settings.gradle(.kts)`, applied scripts such as `gradle/dependencies.gradle`)",
		"`gradle.properties` (variable definitions only)",
		"`buildSrc/**/*.kt`, `buildSrc/**/*.groovy`; `*.kt` below `src/main/kotlin` of `build-logic`, `build-conventions` and `convention-plugins` included builds",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies,
			What: "catalog `[libraries]` and settings catalogs (`versionCatalogs { create(\"libs\") { library(\"a\", \"g\", \"n\").version(\"v\") / .versionRef(\"k\"); library(\"a\", \"g:n:v\") } }`, Groovy also `libs { … }`); " +
				"build-script string notation `implementation(\"group:name:version\")`, `api 'group:name:version[:classifier][@ext]'` " +
				"for the common configurations (`implementation`, `api`, `compileOnly`, `runtimeOnly`, `test…`, `androidTestImplementation`, `debugImplementation`, `kapt`, `ksp`, " +
				"`annotationProcessor`, `classpath`, `compile`, `testCompile`, `detektPlugins`, `lintChecks`, `checkstyle`, …) and `platform(…)`/`enforcedPlatform(…)`; " +
				"map notation `implementation group: 'g', name: 'n', version: 'v'` (optional parentheses, any order, extra keys such as `classifier`/`ext` ignored) and " +
				"Kotlin named arguments `implementation(group = \"g\", name = \"n\", version = \"v\")`; " +
				"coordinates assigned to variables or map entries (`def lib = \"g:n:v\"`, `libs += [x: \"g:n:$versions.x\"]`, `mapOf(\"x\" to \"g:n:v\")`); " +
				"in `buildSrc`/convention-plugin sources every string literal of the form `group:name:version`; the name is `group:name`",
			Datasource: "`maven`", Versioning: "`maven`"},
		{Name: DepTypePlugin,
			What: "catalog `[plugins]` and settings catalog `plugin(\"a\", \"id\").version(\"v\")`/`.versionRef(\"k\")`; build-script and settings `plugins { id(\"x\") version \"y\" }`, `id 'x' version 'y'`, `id(\"x\").version(\"y\")`, " +
				"`kotlin(\"jvm\") version \"y\"` (id `org.jetbrains.kotlin.jvm`), also with a variable as version (`version kotlinVersion`, `version(Versions.kotlin)`); the name is the plugin id",
			Datasource: "`maven` (Gradle plugin portal marker artifact `<id>:<id>.gradle.plugin`)", Versioning: "`maven`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "unresolved version.ref <k>", When: "a catalog entry (TOML or settings) references a version key that does not exist"},
		{Reason: "rich version", When: "a catalog version is a rich version (TOML table or settings `version(\"k\") { strictly(…) }`/`.version { … }` with `strictly`, `require`, `prefer`, `reject`, `rejectAll`)"},
		{Reason: "cannot locate value", When: "a TOML catalog version is not a single-line string or contains a backslash escape"},
		{Reason: "unresolved variable <x>", When: "a version is exactly one variable reference and no literal definition of it is visible (see Variables)"},
		{Reason: "version variable", When: "a coordinate's group or name, or a plugin id, is interpolated, or the version mixes variables with text (`${v}-jre`) or is an expression that is not a variable reference"},
		{Reason: "dynamic version <v>", When: "a dynamic version (`1.+`, `[1.0,2.0)`, `latest.release`) has no locked version in a lockfile, or is another `latest.<status>` (`latest.milestone`), or is a prefix without a dot before the `+` (`1.2+`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Variables", Items: []string{
			"Definitions (literal strings only): `gradle.properties` (`key=value`, `key: value`, `key value`); Groovy `def x = '1'` (also `final def`, `String x`), `ext.x = '1'`, " +
				"`project.ext.x = '1'`, `ext { x = '1' }`, `ext.set('x', '1')`, `ext['x'] = '1'`; Groovy maps `versions = [x: '1']`, `versions += [x: '1']`, `ext.versions = [...]` " +
				"(nested maps too) and `versions.x = '1'` for a declared map (`versions.x`); Kotlin `val x = \"1\"`, `const val x = \"1\"`, `val x by extra(\"1\")`, `extra[\"x\"] = \"1\"`, " +
				"`extra.set(\"x\", \"1\")`, Kotlin maps `val versions = mapOf(\"x\" to \"1\")` (also `mutableMapOf`, nested `mapOf`; `versions.x`); " +
				"members of `object`/`class` bodies are also defined qualified (`Versions.x`, `Deps.Versions.x`); settings catalog versions `version(\"k\", v)` may take a variable.",
			"References: `$x`, `${x}` (Groovy also `$versions.x`), `${project.x}`, `${rootProject.x}`, `${property(\"x\")}`, `${project.property(\"x\")}`, " +
				"`${providers.gradleProperty(\"x\").get()}`, `${extra[\"x\"]}`, `${rootProject.extra[\"x\"]}`, `${versions['x']}`, `${versions.getValue(\"x\")}`, `${Versions.x}`, and bare expressions as a plugin, " +
				"settings catalog or map-notation version (`version kotlinVersion`, `version = Versions.kotlin`, `.version(sonarPluginApi)`); `val x: String by project` reads `gradle.properties`. " +
				"Only a version that is exactly one reference is resolved.",
			"Resolution order: the referencing file, then the build scripts and `gradle.properties` of its directory and of each parent directory up to the build root " +
				"(the nearest directory with a `settings.gradle(.kts)`; `buildSrc` is its own build), then the scripts in the root's `gradle/` directory, " +
				"then the `buildSrc` and convention-plugin sources of the build. The first definition wins.",
			"A resolved dep is moved to its definition: file, line, span and version are the definition's, and the variable name is its shared version name, " +
				"so one update edits the definition once for every dep using it.",
		}},
		{Title: "Repositories", Items: []string{
			"Declarations: `mavenCentral()` (`central=https://repo.maven.apache.org/maven2/`, `DEPS_MAVEN_CENTRAL_URL` replaces it), `google()` (`google=https://dl.google.com/dl/android/maven2/`), " +
				"`gradlePluginPortal()` (`gradlePluginPortal=https://plugins.gradle.org/m2/`), `jcenter()` (sunset; Maven Central is queried instead), " +
				"`maven { url = uri(\"…\"); name = \"x\" }`, `maven { url '…' }`, `maven { setUrl(\"…\") }`, `maven(\"…\")`, `maven(url = \"…\")`; the `name` is the repository id. " +
				"`mavenLocal()`, non-http(s) and interpolated URLs, and repositories nested deeper (conditionals) are ignored.",
			"Content filters: `content { }`/`mavenContent { }` of a repository (`includeGroup`, `includeGroupAndSubgroups`, `includeGroupByRegex`, `includeModule`, `includeModuleByRegex`, " +
				"`includeVersion…` (as its module) and the `exclude…` counterparts except version excludes; `snapshotsOnly()` serves no release) drop the repositories that do not serve a dep; " +
				"`exclusiveContent { forRepository { … }; filter { … } }` makes its repositories the only ones of the modules the filter includes and serves nothing else. " +
				"Plugins are matched as their marker artifact `<id>:<id>.gradle.plugin`. When every declared repository of a level excludes a dep, the next level applies.",
			"Plugins (build-script and catalog): `settings.gradle(.kts)` `pluginManagement { repositories }` of the build, else the Gradle plugin portal then Google Maven.",
			"Buildscript classpath deps: the script's `buildscript { repositories }`, else Maven Central, Google Maven, then the Gradle plugin portal.",
			"Other deps: the project's repositories — `allprojects { repositories }` of the build scripts from the root down, `subprojects { repositories }` of the parent ones, " +
				"and the script's top-level `repositories` (catalogs and `buildSrc` sources: the root project's `allprojects`) — else `dependencyResolutionManagement { repositories }` " +
				"(first when `RepositoriesMode.PREFER_SETTINGS`), else Maven Central, Google Maven, then the Gradle plugin portal (plugin artifacts and markers declared as libraries).",
		}},
		{Title: "Lockfiles and dynamic versions", Items: []string{
			"Dynamic versions become ranges: `1.+`/`1.2.+` (prefix, prereleases included), `[1.0,2.0)`, `(1.0,2.0]`, `[1.0,)`, `(,2.0)`, `]1.0,2.0[`, " +
				"`latest.release` (every release) and `latest.integration` (prereleases too). " +
				"With dependency locking their locked version is read from `gradle.lockfile` (`group:artifact:version=configurations`) or the legacy `gradle/dependency-locks/*.lockfile` " +
				"of the project (the build script's directory; `buildscript-gradle.lockfile` and `buildscript-classpath.lockfile` for buildscript classpath deps and plugins, `settings-gradle.lockfile` " +
				"for settings plugins; plugins are locked as their marker `<id>:<id>.gradle.plugin`); catalog and `buildSrc` deps " +
				"take the first project of the build, root project first, whose lockfile has the module. Several locked versions (per configuration) yield the highest.",
			"`rangeStrategy` applies as for npm: under `auto`/`update-lockfile` an update the range admits only moves the lockfile; others rewrite the range keeping its form " +
				"(`1.+` → `2.+` with the same number of components, an exclusive upper bound becomes the next value of the new version at the position where the bounds differ: " +
				"`[1.0,2.0)` → `[1.0,3.0)`, an inclusive one the new version). A locked `latest.*` selector admits every version, so its updates only move the lockfile; " +
				"it is never rewritten (under `bump` its manifest edit fails).",
			"Lockfiles and `gradle/verification-metadata.xml` are regenerated by the gradle post-processor (`apply`/`run`, not `plan`) when the build root has one: it runs `./gradlew` " +
				"(else `gradle`; Java is required) with `--write-locks`, or `--update-locks group:artifact` for lockfile-only updates, and fails the build's files when the tools are missing.",
			"Lock file maintenance (`run`): each build root with a lockfile gets one `lockFileMaintenance` update (the root project's `gradle.lockfile`, else its first lockfile by path; " +
				"builds with only `gradle/verification-metadata.xml` get none). It runs every locked project's `dependencies` (`buildEnvironment` for buildscript locks) with `--write-locks` " +
				"and no `--update-locks`, so every configuration is re-resolved and dynamic versions move to the newest they admit; verification metadata present is regenerated as well. " +
				"Build files are unchanged; only changed lockfiles and metadata are written.",
		}},
		{Title: "Shared versions", Body: "One `[versions]` entry or variable may serve many libraries and plugins; each becomes a dep with the same file and span. " +
			"`plan` and `apply` update the definition once. `run` groups the updates of deps sharing a definition into one PR; group rules, for example " +
			"`{manager: gradle, names: [\"org.jetbrains.kotlin*\"], group: kotlin}`, combine further deps."},
		{Title: "Not covered", Items: []string{
			"Values computed from expressions (`\"$a.$b\"`, `if`/`?:`, `versions[\"a\"]!![\"b\"]`), catalogs imported in settings with `from(files(…))` (the TOML file itself is a catalog dep source) and `useModule(…)` in `resolutionStrategy`.",
			"Variables defined in `~/.gradle/gradle.properties`, by `-P`/environment, or in included builds other than `buildSrc`.",
			"`latest.<status>` for statuses other than release and integration (`latest.milestone`, custom statuses) depend on component metadata deps does not read; they are skipped even with a lockfile.",
			"Prefixes without a dot (`1.2+`): Gradle matches the text before the `+` as a string prefix (https://docs.gradle.org/current/userguide/dependency_versions.html: " +
				"\"Only versions exactly matching the portion before the + are included\"), so `1.2+` also admits 1.20–1.29, which no semver constraint expresses; they are skipped.",
			"`exclusiveContent { forRepositories(…) }` by reference, `onlyForConfigurations`/`notForConfigurations`, version-specific excludes and Ivy/flatDir repositories.",
		}},
	},
}
