package dartversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the dart-version datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "any (`dart`)",
	Endpoints: "`GET <DEPS_DART_ARCHIVE_URL or https://storage.googleapis.com>/storage/v1/b/dart-archive/o?delimiter=/&prefix=channels/<channel>/release/` for the channels `stable`, `beta` and `dev` (all pages), once per run, as Renovate does: one release directory per version (`3.5.4`, `3.6.0-216.1.beta`, `3.7.0-1.0.dev`); `latest`, old Subversion revision numbers and stable versions listed in another channel are skipped. " +
		"Source repository (release notes): `dart-lang/sdk`",
	Auth:         "none",
	ReleaseTimes: "none",
}
