package gotoolchain

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the go-toolchain datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "`go` / `toolchain`",
	Endpoints:    "`https://go.dev/dl/?mode=json&include=all`, once per run; a derived `X.Y` per minor",
	Auth:         "none",
	ReleaseTimes: "none",
}
