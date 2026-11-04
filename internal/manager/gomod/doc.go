package gomod

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gomod manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the requirements and the `go`/`toolchain` directives of Go modules. `go.mod` is parsed with `golang.org/x/mod/modfile`.",
	Files:   []string{"`go.mod`"},
	DepTypes: []refdoc.DepType{
		{Name: "direct", What: "`require` without `// indirect`", Datasource: "`go`", Versioning: "`go`"},
		{Name: "indirect", What: "`require … // indirect`", Datasource: "`go`", Versioning: "`go`"},
		{Name: "go", What: "`go 1.x` directive", Datasource: "`go-toolchain`", Versioning: "`tag`"},
		{Name: "toolchain", What: "`toolchain go1.x.y`; the span covers the version after `go`", Datasource: "`go-toolchain`", Versioning: "`tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "replaced", When: "a `replace` directive targets the module"},
		{Reason: "pseudo-version", When: "the current version is a pseudo-version"},
		{Reason: "vendored module (not supported)", When: "`vendor/modules.txt` exists next to `go.mod`"},
		{Reason: "prerelease toolchain", When: "`go1.27rc1` style directive"},
		{Reason: "cannot locate value", When: "the version bytes were not found"},
	},
	Sections: []refdoc.Section{
		{Title: "Versioning", Items: []string{
			"`go` versioning excludes pseudo-versions, prereleases (unless the current version is one), `+incompatible` (unless the current version has it), and other majors (except v0 → v1).",
			"Directives move at their precision: `go 1.26` → `go 1.27`, `go 1.26.1` → `go 1.27.1`.",
		}},
		{Title: "Post-processing (`apply`, `run`)", Items: []string{
			"After any gomod change, deps runs `go mod tidy -modfile=<tmp>/go.mod` in the module directory, on temporary copies of `go.mod` and `go.sum`, with `GOWORK=off` and `GOFLAGS` cleared.",
			"The tidied `go.mod` and `go.sum` replace the patched files. The real files are only written by `apply`.",
			"At most 4 post-processors (tidy and npm installs together) run concurrently.",
			"A failure drops that module's change: `go mod tidy in <dir>: <last 20 lines of stderr>`.",
			"`plan` does not run tidy, and prints `note    go.sum changes are computed by apply/run`.",
		}},
	},
}
