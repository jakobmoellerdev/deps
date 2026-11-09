package paket

import "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"

// Doc is the reference documentation of the paket manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the NuGet packages of [Paket](https://fsprojects.github.io/Paket/)'s `paket.dependencies` and their versions in `paket.lock`. " +
		"Only the version constraint of a `nuget` line is rewritten, so options, comments and alignment are kept; `apply` and `run` update paket.lock with `paket update --no-install`.",
	Files: []string{
		"`paket.dependencies` (`paket.lock` next to it is read for the locked versions and updated by the post-processor)",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepType, What: "`nuget <id> [<constraint>] [<options>]` lines of every group (`group <name>`; before the first one the `Main` group); `github`, `gist`, `http`, `git` and `clitool` lines are not deps", Datasource: "`nuget` (the group's `source` feeds, else nuget.org)", Versioning: "`nuget`", Datasources: []string{"nuget"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipConstraint, When: "a constraint outside Paket's grammar (an unknown operator, an invalid version)"},
		{Reason: skipNoLock, When: "a package without constraint whose group in paket.lock does not lock it, or without paket.lock"},
		{Reason: skipNoSources, When: "a group whose `source` lines are all local folders or `%VAR%` URLs"},
	},
	Sections: []refdoc.Section{
		{Title: "Constraints", Items: []string{
			"Paket versions are NuGet versions (versioning `nuget`). A bare version, `= 1.2.3` and `== 1.2.3` pin it exactly: the new version replaces it, keeping the operator.",
			"Other constraints (`>= 1.2`, `> 1.2`, `<= 2`, `< 2`, `~> 1.2` (`>= 1.2 < 2.0`; `~> 1.2.3` is below 1.3), combinations such as `>= 1.2.3 < 1.5` or `~> 1.2 >= 1.2.3`, a `!`/`@` resolver strategy prefix, prerelease channels `>= 2 beta rc` or `prerelease`) are ranges, matched as Paket does and rewritten per [rangeStrategy](configuration.md#rangestrategy) (`auto` is `update-lockfile`, as in Renovate): a version they admit is a lockfile-only update; a newer one moves the bounds past it, keeping the shape (`~> 1.2` → `~> 2.1` for 2.1.0, `< 2.0` → `< 2.2` for 2.1.4, `<= 2.0` → `<= 2.1.4`; a floor below a moved `~>` is dropped). `bump` raises `>=` floors and `~>` to an admitted version at their precision, `widen` writes `~> 1.2` as `>= 1.2 < 3.0`, `pin` writes the bare version.",
			"A package without constraint (Paket's `>= 0`) is updated in paket.lock only.",
			"A prerelease is admitted only by a constraint listing its channel (or `prerelease`), or one comparing with a prerelease of the same release.",
		}},
		{Title: "Sources", Items: []string{
			"The `source` lines of the package's group are its feeds (`https://www.nuget.org/api/v2` and `https://nuget.org/api/v2` are read as nuget.org's v3 service index); a group without `source` uses nuget.org. Local folders and `%VAR%` sources are left out; credentials on source lines are not read. Other v2 feeds are not read (see [Datasources](#datasources)).",
		}},
		{Title: "paket.lock", Items: []string{
			"The versions of the `NUGET` sections of each `GROUP` are the dep's locked version (ids compared case-insensitively).",
			"Post-processor (`apply`, `run`): with paket.lock next to the paket.dependencies, the patched file and the lock are copied into a mirror of the directory (without `.git`, `.paket`, `packages`, `paket-files`) and every updated package, manifest and lockfile-only updates alike, is locked at the update's version with `paket update --no-install --group <group> --version <version> <id>`, as Renovate does; lockFileMaintenance runs `paket update --no-install`. Only paket.lock is taken back. Tool: `paket` on PATH (`dotnet tool install --global paket`), else `paket not found (needed to update paket.lock)`; failure `paket update in <dir>: <last 20 output lines>`.",
		}},
	},
}
