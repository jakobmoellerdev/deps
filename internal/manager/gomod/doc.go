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
		{Reason: "vendored module (not supported)", When: "`vendor/modules.txt` exists next to `go.mod`"},
		{Reason: "prerelease toolchain", When: "`go1.27rc1` style directive"},
		{Reason: "cannot locate value", When: "the version bytes were not found"},
	},
	Sections: []refdoc.Section{
		{Title: "Versioning", Items: []string{
			"`go` versioning excludes pseudo-versions, prereleases (unless the current version is one), `+incompatible` (unless the current version has it), and other majors (except v0 → v1).",
			"Directives move at their precision: `go 1.26` → `go 1.27`, `go 1.26.1` → `go 1.27.1`.",
		}},
		{Title: "Pseudo-versions (digest updates)", Items: []string{
			"A requirement on a pseudo-version (`v0.0.0-<timestamp>-<commit>`) tracks a commit. A newer release (above the pseudo-version's base, e.g. `v1.2.4` for `v1.2.4-0.<ts>-<commit>`) is proposed as a version update; prereleases only when the base is one.",
			"Otherwise the module's `@latest` is asked: when it is a newer pseudo-version of the same major (the tip of the default branch of an untagged module), it is proposed as a `digest` update, titled `update <module> digest to <commit7>`.",
			"Rules apply as for other digest updates (`updates: [digest]`, `ignore`, `updateTypes: [digest]`); `minimumReleaseAge` of the digest type is checked against the commit time.",
			"Modules with releases whose pseudo-version is ahead of the latest release get no digest update (`@latest` is that release, not the branch tip).",
		}},
		{Title: "Major module paths (`gomodUpdateImportPaths`)", Items: []string{
			"Major versions ≥ 2 live under their own module path. For a requirement on `<path>/v<N>` (or an unsuffixed v0/v1 module, `/v<M+1>` for `vM.x.y+incompatible`, `gopkg.in/x.v<N>`), deps lists `/v<N+1>`, `/v<N+2>`, … from the GOPROXY URLs until one has no releases (at most 5).",
			"The newest stable release of each path that the major policy accepts (`updates`, `allow`, `minimumReleaseAge`, `ignore` per update type) is a `major` update in the major PR bucket (`v<N>` buckets with `separateMultipleMajor`); a newer path replaces an older one in the same bucket.",
			"The update rewrites the require (path and version) and every import of the old path and its packages in the module's `.go` files (below the go.mod, skipping `vendor`, `testdata`, `.`/`_` directories and nested modules; only the import path literals change). `go mod tidy` then sees the rewritten files through `-overlay`.",
			"The new major's API may have changed: the PR body says so, and the build is not guaranteed to pass.",
			"`gomodUpdateImportPaths: false` turns module path updates off. Only GOPROXY URLs are probed: GONOPROXY/GOPRIVATE modules, and paths no proxy before `direct` has, get no module path updates.",
			"A probe only decides whether a module path update is offered, so each probe request to a proxy gets 10 s, retries included, and is not retried once that runs out. A path no proxy answers in time counts as having no releases and is logged as a warning (`major module path probe timed out; assuming no newer major`); the requirement keeps its other updates.",
		}},
		{Title: "Post-processing (`apply`, `run`)", Items: []string{
			"After any gomod change, deps runs `go mod tidy -modfile=<tmp>/go.mod` in the module directory, on temporary copies of `go.mod` and `go.sum`, with `GOWORK=off` and `GOFLAGS` cleared; source files rewritten by a major module path update are passed with `-overlay`.",
			"The tidied `go.mod` and `go.sum` replace the patched files. The real files are only written by `apply`.",
			"At most 4 post-processors (tidy and npm installs together) run concurrently.",
			"A failure drops that module's change: `go mod tidy in <dir>: <last 20 lines of stderr>`.",
			"`plan` does not run tidy, and prints `note    go.sum changes are computed by apply/run`.",
		}},
	},
}
