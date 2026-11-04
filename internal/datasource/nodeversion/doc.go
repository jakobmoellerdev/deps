package nodeversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the node-version datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "any (`node`)",
	Endpoints:    "`<DEPS_NODE_DIST_URL or https://nodejs.org/dist>/index.json`, once per run; published versions plus a derived `X.Y` and `X` per stable line",
	Auth:         "none",
	ReleaseTimes: "`date`",
}
