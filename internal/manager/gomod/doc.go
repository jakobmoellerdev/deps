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
		{Reason: "module path changed to <path> (<module>@<version>)", When: "every newer candidate's `go.mod` declares another module path (a renamed module, or a fork that kept its origin's path); go refuses such a version"},
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
		{Title: "Module path changes", Items: []string{
			"A version is only an update when its `go.mod` declares the path it is required as. The `@v/<version>.mod` of a candidate that passed the other checks is read from the module download cache, else the GOPROXY URLs (one request per module@version and run). One that declares another path (`github.com/google/cel-go` v0.32.0 declares `cel.dev/cel-go`) is rejected like a too new one, and the next candidate of its PR bucket is tried; for a renamed module the next 8 are fetched concurrently. This applies to major module paths and pseudo-version heads too.",
			"A requirement left without updates this way is skipped with `module path changed to <declared> (<module>@<version>)` (the newest such version): moving to the new path is a code change deps does not make. A failed `.mod` lookup is logged and leaves the candidate to `go mod tidy`; GONOPROXY/GOPRIVATE modules are not checked.",
			"When `go mod tidy` still refuses a module version this way (`module declares its path as … but was required as …`, for a module the update pulls in, or one the check could not read), `run` skips the update with the same reason instead of failing: its PR group is reported as `skipped: update cannot be applied` and is no error, so it does not keep obsolete PRs open (`apply` leaves the update out and applies the others).",
		}},
		{Title: "Post-processing (`apply`, `run`)", Items: []string{
			"After any gomod change, deps runs `go mod tidy -modfile=<tmp>/go.mod` in the module directory, on temporary copies of `go.mod` and `go.sum`, with `GOWORK=off` and `GOFLAGS` cleared; source files rewritten by a major module path update are passed with `-overlay`.",
			"The tidied `go.mod` and `go.sum` replace the patched files. The real files are only written by `apply`.",
			"At most 4 post-processors (tidy and npm installs together) run concurrently.",
			"A failure drops that module's change: `go mod tidy in <dir>: <last 20 lines of stderr>` (except a module path change, above).",
			"`plan` does not run tidy, and prints `note    go.sum changes are computed by apply/run`.",
		}},
		{Title: "Lock file maintenance (`run`)", Body: "Go has no lockfile, and Renovate has no gomod lock file maintenance. With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, deps treats the `// indirect` requirements (which it does not update by default) as the locked versions of each scanned `go.mod` that has a `go.sum` and no `vendor/modules.txt`. " +
			"In the module directory, on temporary copies of `go.mod` and `go.sum` and with the environment of `go mod tidy` above, deps runs `go get -modfile=<tmp>/go.mod <module>@upgrade …` for every indirect requirement without a `replace` directive, then `go mod tidy`. `@upgrade` is the newest non-retracted release of the same module path (a prerelease only when there is no release); a current version newer than that (a prerelease or pseudo-version) stays.",
			Items: []string{
				"The `go` and `toolchain` directives and the direct requirements do not move. When the `go get` call would move them, or go reports a version conflict between the requested modules or a module that needs a newer go than the running one, the modules are split in halves and each half is tried on the result of the one before. Only a module whose upgrade conflicts on its own keeps its version; without conflicts this is one `go get` call.",
				"The PR changes `go.mod` (indirect lines only) and `go.sum`, and shares `<branchPrefix>lock-file-maintenance` with the other lockfiles; `minimumReleaseAge` does not apply to it. A module with nothing to move gets no change.",
				"Other `go get` failures drop that module's maintenance: `go get in <dir>: <last 20 lines of stderr>`.",
				"With `GOTOOLCHAIN=auto`, a module that needs a newer go than the running one makes go download that toolchain before deps rejects the upgrade.",
			}},
	},
}
