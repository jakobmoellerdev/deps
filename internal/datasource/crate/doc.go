package crate

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the crate datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "crate name (case-insensitive)",
	Endpoints: "`GET <DEPS_CRATES_INDEX_URL or https://index.crates.io>/<prefix>/<name>` (sparse index: `1/<n>`, `2/<n>`, `3/<first char>/<n>`, `<n[0:2]>/<n[2:4]>/<n>`), newline-delimited JSON, yanked versions dropped; 404 → `crate <name> not found`. " +
		"Partial requirements also get a derived `X.Y` and `X` per stable release line. " +
		"Source repository (release notes): `GET <DEPS_CRATES_API_URL or https://crates.io>/api/v1/crates/<name>/<version>` `repository`, only for versions in a pull request",
	Auth:         "none",
	ReleaseTimes: "index `pubtime`; a release line has the time of its first release",
}
