package pythonversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the python-version datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "any (`python`)",
	Endpoints:    "`GET <DEPS_PYTHON_RELEASES_URL or https://www.python.org/api/v2/downloads/release/>` once per run (Renovate's source): the published CPython releases (`Python X.Y.Z[{a,b,rc}N]`; other entries such as the install manager are ignored), plus a derived `X.Y` and `X` per stable line",
	Auth:         "none",
	ReleaseTimes: "`release_date`",
}
