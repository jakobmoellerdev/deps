package fvm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the fvm manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Flutter SDK version [FVM](https://fvm.app) pins for a project: the top-level `flutter` of `.fvmrc`, or `flutterSdkVersion` of the legacy `.fvm/fvm_config.json` (the first one present, as FVM and Renovate read it). " +
		"Only the version string is replaced; flavors and other settings are kept.",
	Files: []string{"any `.fvmrc`", "any `.fvm/fvm_config.json`"},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "the pinned Flutter version (name `" + DepName + "`): `3.24.3`, or a beta such as `3.26.0-0.1.pre`", Versioning: "`semver`", Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipChannel, When: "a release channel instead of a version: `stable`, `beta`, `dev`, `master`, `main`"},
		{Reason: skipUnsupported, When: "any other value: a version with a channel (`3.24.3@beta`), a commit, a fork (`myfork/3.24.3`)"},
		{Reason: skipEscaped, When: "the version is written with JSON escapes"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions", Items: []string{
			"Stable versions get stable updates; a beta (`-x.y.pre`) also gets newer betas.",
			"Nothing else is run: `fvm use` installs the new version on the next run.",
		}},
	},
}
