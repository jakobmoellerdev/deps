package mavenwrapper

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the maven-wrapper manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Maven version of `distributionUrl` (`…/org/apache/maven/apache-maven/<version>/apache-maven-<version>-bin.zip` on Maven Central or any mirror) " +
		"and the Maven Wrapper version of `wrapperVersion` and `wrapperUrl` (`…/org/apache/maven/wrapper/maven-wrapper/<version>/maven-wrapper-<version>.jar`) " +
		"in `maven-wrapper.properties`; `apply` and `run` then regenerate the wrapper scripts with `mvn wrapper:wrapper`.",
	Files: []string{"`.mvn/wrapper/maven-wrapper.properties`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeMaven, What: "`distributionUrl` version, dep `" + MavenName + "`", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeWrapper, What: "`wrapperVersion` and the `wrapperUrl` version (one dep when they agree), dep `" + WrapperName + "`", Versioning: "`maven`", Datasources: []string{"maven"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "unsupported distributionUrl", When: "the URL does not end in `org/apache/maven/apache-maven/<version>/apache-maven-<version>-bin.zip` with the same version twice"},
		{Reason: "unsupported wrapperUrl", When: "the URL does not end in `org/apache/maven/wrapper/maven-wrapper/<version>/maven-wrapper-<version>.jar` with the same version twice"},
		{Reason: "unsupported wrapperVersion", When: "`wrapperVersion` is not a version"},
		{Reason: "invalid distributionSha256Sum", When: "`distributionSha256Sum` is not 64 hex characters"},
		{Reason: "invalid wrapperSha256Sum", When: "`wrapperSha256Sum` is not 64 hex characters"},
	},
	Sections: []refdoc.Section{
		{Title: "Repository", Items: []string{
			"The repository of a dep is the URL before `org/apache/maven/`: Maven Central (`https://repo.maven.apache.org/maven2/`) or a mirror, which is then the only repository looked up.",
			"All occurrences of the version are replaced together: the directory and file name in the URL, and `wrapperVersion` with `wrapperUrl`.",
		}},
		{Title: "Checksum", Items: []string{
			"With `distributionSha256Sum` or `wrapperSha256Sum`, the new sum is replaced together with the version. Maven Central publishes `.sha1` and `.sha512` files for `apache-maven-<version>-bin.zip` and only `.sha1` for `maven-wrapper-<version>.jar`, " +
				"but the wrapper verifies SHA-256 sums, so deps downloads the new artifact from the dep's repository and hashes it (see Checksums).",
		}},
		{Title: "Wrapper files", Body: "After the properties are edited, `apply` and `run` run `./mvnw -B -q org.apache.maven.plugins:maven-wrapper-plugin:<wrapperVersion>:wrapper -Dmaven=<version> -Dtype=<type>` " +
			"(`mvn` from `PATH` without an executable `mvnw`) on a temporary copy of the project and take `mvnw`, `mvnw.cmd`, `.mvn/wrapper/maven-wrapper.jar` and `.mvn/wrapper/MavenWrapperDownloader.java` from it; " +
			"the edited properties file is kept. `<type>` is `distributionType`, else `bin` with a `maven-wrapper.jar`, `source` with a `MavenWrapperDownloader.java`, `script` with a `wrapperUrl`, else `only-script`; " +
			"without a known wrapper version the goal is `wrapper:wrapper`. Without Java (`java` on `PATH` or `JAVA_HOME`) the wrapper files are kept with a warning and the update carries only the properties edit, which the existing wrapper reads to download the new distribution (Renovate commits that edit too and reports an artifact error); `plan` always shows only the properties edit."},
	},
}
