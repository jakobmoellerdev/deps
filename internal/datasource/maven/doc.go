package mavends

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the maven datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "`groupId:artifactId`, or a Gradle plugin id (no `:`), looked up as its marker artifact `<id>:<id>.gradle.plugin`; " +
		"the repositories are the dep's `[<id>=]<url>/` list (Maven Central when empty)",
	Endpoints: "`GET <repository>/<group/path>/<artifactId>/maven-metadata.xml` per repository, " +
		"versions merged in repository order; a 404 contributes nothing; none found → `artifact <name> not found`",
	Auth: "maven and maven-wrapper deps: `settings.xml` `<servers>` (Basic auth from `<username>`/`<password>`, encrypted passwords included, and `<configuration><httpHeaders>`) of the repository's id after mirroring; " +
		"gradle deps: the `<name>Username`/`<name>Password` project properties of the named repository. " +
		"Checksum downloads (`Artifact`) below a dep's repository use the same mirror and credentials",
	ReleaseTimes: "`HEAD …/<version>/<artifactId>-<version>.pom` `Last-Modified`, only for versions that end up in a PR; " +
		"not asked for `minimumReleaseAge` when every repository holding the artifact dated its `maven-metadata.xml` (`versioning` `lastUpdated`, UTC `yyyyMMddHHmmss`) and the newest of those is already older",
	Notes: []refdoc.Section{
		{
			Title: "Maven repositories",
			Items: []string{
				"The repositories of a dep are its manager's: for maven the `pom.xml` `<repositories>` (`<pluginRepositories>` for plugins) followed by Maven Central; for gradle Maven Central then Google Maven (plugins: the Gradle plugin portal `https://plugins.gradle.org/m2/` then Google Maven); Maven Central alone otherwise.",
				"Maven Central (id `central`) is `https://repo.maven.apache.org/maven2/`; `DEPS_MAVEN_CENTRAL_URL` replaces it (a mirror).",
				"A failing repository (other than 404) is ignored when another one lists the artifact. SNAPSHOT metadata is not supported.",
			},
		},
		{
			Title: "Maven settings.xml",
			Body: "maven and maven-wrapper deps apply the user settings (`DEPS_MAVEN_SETTINGS`, else `~/.m2/settings.xml`) and the nearest in-repository settings walking up from the pom's directory to `--dir` " +
				"(`.mvn/settings.xml`, `.m2/settings.xml`, then `settings.xml` in each directory). In-repository mirrors, servers and profiles replace user ones with the same id. Gradle ignores settings.xml.",
			Items: []string{
				"`<mirrors>`: each repository is replaced by the first mirror whose `<mirrorOf>` is its id, else by the first that matches: `*`, `external:*` (not localhost or `file:`), `external:http:*`, `id1,id2`, `!id` exclusions. Repositories without id match only `*` and `external:*`.",
				"`<servers>`: requests to a repository whose id (the mirror's, when mirrored) matches use the server's `<username>`/`<password>` as Basic auth and its `<configuration><httpHeaders><property>` headers. Headers are dropped when a redirect leaves the host.",
				"`${env.NAME}` and `${NAME}` are expanded from the environment (empty when unset).",
				"Encrypted passwords (`{…}`, `mvn --encrypt-password`) are decrypted with the master password of `settings-security.xml` (`DEPS_MAVEN_SETTINGS_SECURITY`, else next to the user settings; `<relocation>` is followed); a password that cannot be decrypted fails the lookups using it.",
				"`<repositories>` (plugins: `<pluginRepositories>`) of the profiles listed in `<activeProfiles>` or with `<activeByDefault>true` follow the dep's own repositories, before Maven Central.",
				"Mirror and profile repository URLs that are not http(s) after expansion (`file:`, unfiltered `@name@` resource placeholders, empty) are ignored.",
			},
		},
		{
			Title: "Gradle credentials",
			Body: "A gradle dep's repository with a name (`maven { name = \"corp\"; credentials(PasswordCredentials) }`) authenticates with `corpUsername`/`corpPassword`: " +
				"`ORG_GRADLE_PROJECT_corpUsername`/`ORG_GRADLE_PROJECT_corpPassword`, else `$GRADLE_USER_HOME/gradle.properties` (default `~/.gradle/gradle.properties`).",
		},
		{
			Title: "Source repository",
			Body: "Release notes use the POM of the new version: `<scm><url>`, else `<scm><connection>`/`<developerConnection>` without `scm:git:`, else a github.com or gitlab.com `<url>`; " +
				"without one, the `<parent>` POM's (up to 3 parents). A plugin marker POM is followed to the plugin artifact. Tags `<artifactId>-<version>` are recognized.",
		},
	},
}
