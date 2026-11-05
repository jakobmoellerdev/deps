package gradlewrapper

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gradle-wrapper manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Gradle version of the wrapper in `gradle-wrapper.properties`: the version inside `distributionUrl` " +
		"(`https\\://services.gradle.org/distributions/gradle-8.10.2-bin.zip`, `-all.zip`, or any mirror URL whose last path segment is `gradle-<version>-(bin|all).zip`) " +
		"and, when present, `distributionSha256Sum`.",
	Files: []string{"`gradle/wrapper/gradle-wrapper.properties`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeWrapper, What: "`distributionUrl` version", Versioning: "`maven`", Datasources: []string{"gradle-version"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "unsupported distributionUrl", When: "the last path segment is not `gradle-<version>-bin.zip` or `gradle-<version>-all.zip`"},
		{Reason: "invalid distributionSha256Sum", When: "`distributionSha256Sum` is not 64 hex characters"},
	},
	Sections: []refdoc.Section{
		{Title: "Checksum", Items: []string{
			"With `distributionSha256Sum`, the new checksum is downloaded from `<distributionUrl with the new version>.sha256` (services.gradle.org and its mirrors serve the bare hex) and replaced together with the version.",
		}},
		{Title: "Wrapper files", Body: "After the properties are edited, `apply` and `run` regenerate `gradle/wrapper/gradle-wrapper.jar`, `gradlew` and `gradlew.bat` as Renovate does: " +
			"`./gradlew --console=plain --no-daemon -q wrapper --gradle-version <version> --distribution-type <bin|all>` (`gradle` from `PATH` without an executable `gradlew`) runs on a temporary copy of the project with the edited properties, " +
			"adding `--gradle-distribution-url <url>` for a mirror and `--gradle-distribution-sha256-sum <sum>` with a pinned checksum. " +
			"The edited properties file is kept unless Gradle's rewrite differs in more than formatting (a key the new version adds). " +
			"Without Java (`java` on `PATH` or `JAVA_HOME`) the update fails with `java not found (needed to regenerate the Gradle wrapper)`; `plan` shows only the properties edit."},
	},
}
