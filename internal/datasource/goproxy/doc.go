package goproxy

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the go datasource.
var Doc = refdoc.Datasource{
	Name:         "go",
	NameForm:     "module path; outside go.mod (mise `go:` tools, pre-commit golang hooks' additional_dependencies, custom managers) also a package path, resolved like `go install <package>@<version>` to the longest prefix that lists versions (`github.com/golangci/golangci-lint/v2/cmd/golangci-lint` → `github.com/golangci/golangci-lint/v2`)",
	Endpoints:    "GOPROXY `<proxy>/<escaped path>/@v/list`, following `,` / `\\|` fallback semantics (`direct` → `go list -m -versions -json`); GONOPROXY/GOPRIVATE modules via `go list`. Pseudo-version deps also ask `<proxy>/<escaped path>/@latest` (`go list -m -json <path>@latest`); major module path probes list `<path>/v<N+1>/@v/list`, … from the GOPROXY URLs only, reusing the proxy's list of a successor path that is a dependency itself",
	Auth:         "none for proxy requests; `go list` uses the go command's own auth (git credentials, `.netrc`)",
	ReleaseTimes: "`@v/<v>.info`, read from `$GOMODCACHE/cache/download` first, then the proxy (`go list -m -json` for private modules)",
	Notes: []refdoc.Section{{
		Title: "Go proxy environment",
		Body:  "`GOPROXY`, `GONOPROXY`, `GOPRIVATE` and `GOMODCACHE` are read once via `go env -json`. If `go` is not on `PATH`, deps falls back to the process environment, with default `GOPROXY=https://proxy.golang.org,direct`.",
	}},
}
