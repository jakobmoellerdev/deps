package gradleversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gradle-version datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "any (`gradle`)",
	Endpoints:    "`<DEPS_GRADLE_VERSIONS_URL or https://services.gradle.org/versions/all>`, once per run; snapshots, nightlies and broken releases are dropped, release candidates and milestones kept",
	Auth:         "none",
	ReleaseTimes: "`buildTime`",
}
