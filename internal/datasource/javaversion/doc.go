package javaversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the java-version datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`java` or `java-jdk` (the JDK) or `java-jre` (the JRE), optionally followed by `?os=<os>&architecture=<arch>` (Renovate's form)",
	Endpoints: "`GET <DEPS_JAVA_RELEASES_URL or https://api.adoptium.net/v3/info/release_versions>?image_type=<jdk|jre>&project=jdk&release_type=ga&sort_method=DATE&sort_order=DESC&page_size=50&page=<n>` (Renovate's source, Eclipse Temurin) once per image type and filter per run, " +
		"pages until a short page or a 404, at most 50: the GA versions as Adoptium semver (`21.0.5+11`), plus a derived `X.Y` and `X` per line",
	Auth:         "none",
	ReleaseTimes: "none: the Adoptium version list carries no dates (Renovate's has none either), so `minimumReleaseAge` treats versions as of unknown age (see `minimumReleaseAgeBehaviour`)",
}
