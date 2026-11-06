package flutterversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the flutter-version datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "any (`flutter`)",
	Endpoints: "`GET <FLUTTER_STORAGE_BASE_URL or https://storage.googleapis.com>/flutter_infra_release/releases/releases_linux.json` once per run (the variable is the flutter tool's mirror setting): stable releases and betas (`3.26.0-0.1.pre`); a stable version listed in the beta or dev channel is skipped, as in Renovate. " +
		"Source repository (release notes): `flutter/flutter`",
	Auth:         "none",
	ReleaseTimes: "the `release_date` of each version (the earliest, for a version listed more than once)",
}
