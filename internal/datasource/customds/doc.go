package customds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the custom.<name> datasources.
var Doc = refdoc.Datasource{
	Name:         "custom.<name>",
	NameForm:     "free-form",
	Endpoints:    "`customDatasources.<name>.url`, once per run, at most 10 MiB",
	Auth:         "none",
	ReleaseTimes: "none",
}
