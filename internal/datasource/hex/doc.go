package hex

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the hex datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "the Hex package name (`phoenix`, `gleam_stdlib`), or `name:organization` for a package of a private Hex organization (Renovate's form)",
	Endpoints: "`GET <DEPS_HEX_URL or https://hex.pm>/api/packages/<name>` (`/api/repos/<organization>/packages/<name>` for an organization's package), once per package; " +
		"versions that are not SemVer are dropped, retired versions (`mix hex.retire`) stay listed and are reported as deprecated with their reason. " +
		"Source repository (release notes): the package's `GitHub` link. hex.pm allows 100 unauthenticated API requests per minute (500 with a key); the shared client waits out its `X-RateLimit-Reset`",
	Auth:         "`HEX_API_KEY` (a hex.pm API key, `mix hex.user key generate`), sent as the `Authorization` header; needed for organization packages",
	ReleaseTimes: "the `inserted_at` time of each release",
}
