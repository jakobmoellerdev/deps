package elmpackage

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the elm-package datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`author/project` as in elm.json (`elm/json`)",
	Endpoints: "`GET <DEPS_ELM_PACKAGE_URL or https://package.elm-lang.org>/packages/<author>/<project>/releases.json`, once per package; " +
		"the registry answers an unknown package with HTTP 500. " +
		"Source repository (release notes): `https://github.com/<author>/<project>`, the only place `elm publish` publishes from",
	Auth:         "none",
	ReleaseTimes: "the Unix time the registry lists for each version",
}
