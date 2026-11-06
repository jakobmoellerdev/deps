package endoflife

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the endoflife-date datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "an endoflife.date product (`nodejs`, `ubuntu`, `python`); custom managers only; the dep's `registryUrl` (an API base URL) replaces the default",
	Endpoints: "`GET <registryUrl, DEPS_ENDOFLIFE_DATE_URL or https://endoflife.date/api>/<product>.json`, once per product; " +
		"each cycle's `latest` (else the cycle) is a version; cycles whose `eol` or `discontinued` is `true` or a past date are dropped " +
		"and reported as deprecated when installed; an unknown product (HTTP 404) is skipped",
	Auth:         "none",
	ReleaseTimes: "the cycle's `latestReleaseDate` (its `releaseDate` for a cycle without `latest`)",
}
