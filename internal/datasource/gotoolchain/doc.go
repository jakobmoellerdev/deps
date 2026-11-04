package gotoolchain

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the go-toolchain datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "`go` / `toolchain`",
	Endpoints:    "`https://go.dev/dl/?mode=json&include=all`, once per run; a derived `X.Y` per minor. Release dates: `https://raw.githubusercontent.com/golang/website/HEAD/internal/history/release.go` (as Renovate's golang-version), once per run when a release time is needed",
	Auth:         "none",
	ReleaseTimes: "release date from Go's release history (midnight UTC); a derived `X.Y` has the date of `X.Y.0`",
}
