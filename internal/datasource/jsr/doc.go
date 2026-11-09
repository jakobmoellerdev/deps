package jsr

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the jsr datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "the JSR package name `@<scope>/<name>` (`@std/assert`), as in a `jsr:` specifier",
	Endpoints: "`GET <JSR_URL or https://jsr.io>/@<scope>/<name>/meta.json` once per package (`JSR_URL` is the variable the deno CLI reads); yanked versions are dropped (a yanked current version is reported as deprecated), `latest` is the release the registry recommends. " +
		"Source repository (release notes): the package's linked GitHub repository (`githubRepository`)",
	Auth:         "none",
	ReleaseTimes: "the `createdAt` time of each version; JSR only records it for packages published to since 2025-09-18, so a version without one counts as published on 2025-09-18, as in Renovate",
}
