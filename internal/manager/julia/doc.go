package julia

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	datasources = "`julia-registry` (the General registry)"
	versionings = "`julia`"
)

// Doc is the reference documentation of the julia manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the `[compat]` entries of Julia projects (`Project.toml`, `JuliaProject.toml`) from the General registry, and moves their `Manifest.toml` with Pkg (in-range updates only touch the manifest, as `update-lockfile`). " +
		"Dependabot's `julia` ecosystem; Renovate has no Julia manager. Only the compat string is rewritten, so the file's layout and comments are kept.",
	Files: []string{"`Project.toml`, `JuliaProject.toml` (Pkg prefers the latter where both exist; both are read)", "`Manifest.toml`, `JuliaManifest.toml`, `Manifest-v<major>.<minor>.toml` (the locked versions, see below)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDeps, What: "`[compat]` entries of packages in `[deps]` (`JSON = \"0.21, 1\"`), and `[deps]` packages without a compat entry, which only the manifest moves (updates use commit type `fix`)", Datasource: datasources, Versioning: versionings, Datasources: []string{"julia-registry"}},
		{Name: DepTypeWeakdeps, What: "`[compat]` entries of `[weakdeps]` packages (package extension triggers)", Datasource: datasources, Versioning: versionings, Datasources: []string{"julia-registry"}},
		{Name: DepTypeExtras, What: "`[compat]` entries of `[extras]` packages (test dependencies of `[targets]`)", Datasource: datasources, Versioning: versionings, Datasources: []string{"julia-registry"}},
		{Name: DepTypeJulia, What: "the `julia` compat entry (skipped)", Datasource: "none", Versioning: versionings, Datasources: []string{}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipJulia, When: "the `julia` compat entry: which Julia releases a project supports is its authors' decision, not an update"},
		{Reason: skipStdlib, When: "a standard library (`LinearAlgebra`, `Test`, `Pkg`, the bundled `_jll`s, by UUID): its version is Julia's. Registered former stdlibs (`Statistics`, `DelimitedFiles`) are looked up like any package; stdlibs in `[deps]` without a compat entry are not listed at all"},
		{Reason: skipNotDep, When: "a compat entry naming a package the project does not list (Pkg rejects such a project)"},
		{Reason: skipNonString, When: "a compat value that is not a string"},
		{Reason: skipEscaped, When: "a compat string with escapes, whose bytes deps would not rewrite faithfully"},
		{Reason: skipUnsupported, When: "a compat entry outside Pkg's grammar (`> 1`, `<= 2`, `1.x`, `*`, an empty string)"},
		{Reason: skipPath, When: "a package that `[sources]` installs from a `path`"},
		{Reason: skipURL, When: "a package that `[sources]` installs from a git `url`"},
		{Reason: skipNoLock, When: "a compat entry with only upper bounds (`< 2`), or a `[deps]` package without a compat entry, that the manifest does not lock (or that has no manifest)"},
	},
	Sections: []refdoc.Section{
		{Title: "Compat entries", Items: []string{
			"Pkg's grammar (`semver_spec`): comma-separated terms, any of which admits a version. A bare or `^` version is a caret range (`1.2.3` = [1.2.3, 2), `0.2.3` = [0.2.3, 0.3), `0.0.3` only 0.0.3, `1` = [1, 2), `0` = [0, 1), `0.0` = [0, 0.1)); `~1.2.3` and `~1.2` are [1.2.x, 1.3), `~1` is [1, 2); `=1.2` is exactly 1.2.0; `>= 1.2` (or `≥`) has no upper bound, `< 1.2` no lower one; `1.2 - 2` is [1.2, 3). A `v` prefix is allowed. Prereleases and builds count as their release.",
			"Versioning `julia` orders versions as Julia's VersionNumber does (1–3 parts, `-pre` and `+build` after three; JLL rebuilds `1.2.11+1` > `1.2.11+0`). Update types follow the caret: the leftmost non-zero part is the major (`0.21` → `0.22` is major, `1.2` → `1.3` minor). Prereleases are only proposed from a prerelease.",
			"Rewrites per [rangeStrategy](configuration.md#rangestrategy): `widen` appends a term for the new version in the style of the term with the highest upper bound, the new version's caret prefix for caret terms (`0.21, 1` → `0.21, 1, 2` for 2.3.4, `0.21` → `0.21, 0.22` for 0.22.1), as CompatHelper does; `~` terms take the version at their precision, `=` terms the version itself (`=1.2` is exactly 1.2.0), and `< 2` or the upper half of `1.2 - 1.5` is raised in place. " +
				"`replace` (and an out-of-range version under `update-lockfile` and `bump`) writes that one term with the version at its precision (`0.21, 1` → `2`, `4.2` → `5.1`, `~1.2` → `~1.5`, `=0.3.1` → `=0.4.2`). `bump` and `in-range-only` drop the terms entirely below an admitted version and raise the floor of the term admitting it (`0.21, 1` → `1` for 1.4.5, `1.2` → `1.4`); `pin` writes `=<version>`.",
			"`auto` is `update-lockfile` for a project with a manifest (an application, or a workspace committing its manifest): versions the compat entry admits are manifest-only updates from the locked version (`, lockfile` in the report), others replace the entry. A project without one (a package: registered packages do not commit manifests) gets `widen`, the Julia convention of keeping older majors supported; versions the entry admits are not proposed then.",
			"A `[deps]` package without a compat entry admits any version (`>= 0` in reports): it is never written into the project, only moved in the manifest (`update-lockfile`, `bump`, `in-range-only`).",
			"Each compat entry is looked up by its name and the UUID its `[deps]`, `[weakdeps]` or `[extras]` entry gives; a package the General registry does not have (a private registry's) is skipped with the datasource's reason.",
		}},
		{Title: "Manifest (`apply`, `run`)", Body: manifestBody,
			Items: []string{"`plan` does not run Julia, and prints `note    Manifest.toml changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every manifest of a scanned project (a workspace's once) is refreshed with `Pkg.update()` in the temporary copy, which moves every locked package, transitive ones included, to the newest version the compat entries admit. " +
			"All manifests share the PR on `<branchPrefix>lock-file-maintenance`, which changes only manifests; `minimumReleaseAge` does not apply to it."},
	},
}

const manifestBody = "1. **Manifest.** A project's manifest is `JuliaManifest.toml` or `Manifest.toml` next to it, else the `Manifest-v<major>.<minor>.toml` of the newest Julia release there (other versioned manifests are neither read nor updated). " +
	"A project listed in the `[workspace] projects` of a project in a directory above (Julia 1.12 workspaces, nested ones too) uses the manifest of the workspace root; without a manifest the project is only patched.\n" +
	"2. **Locked versions.** Before resolution each manifest (format 1 or 2) is read: a package gets the `version` of its entry with the package's UUID; entries tracking a `path` or a git `repo-url` are not registry versions and are left out. An unreadable manifest is reported once and its packages are bumped.\n" +
	"3. **Temporary copy.** The manifest's directory is mirrored to a temp dir: project files are copied (patched ones with their new compat entries), the manifest is written as `Manifest.toml` (so Pkg reads it whatever its Julia version), other files are symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so `[sources]` paths resolve as in the checkout.\n" +
	"4. **Update.** For each project of the scope with updates, `julia` runs `Pkg.activate(<project>)` and `Pkg.add([Pkg.PackageSpec(name=…, uuid=…, version=\"<new version>\"), …])` for its updated `[deps]` packages the manifest locks, which moves exactly those to the update's version (a build suffix dropped) and keeps every other locked version Pkg can keep. " +
	"A project whose updates move no locked package (compat entries of `[weakdeps]` and `[extras]`) runs `Pkg.resolve()` instead, which records the new compat entries in the manifest's `project_hash` and moves what they exclude. Lockfile-only updates are added the same way, with the project unchanged. Only the manifest is taken back.\n" +
	"5. **Tool.** `julia` must be on `PATH` (e.g. `julia-actions/setup-julia`); it runs with `JULIA_PKG_PRECOMPILE_AUTO=0` and the runner's depot, installing the General registry first when the depot has none. Pkg installs the packages of the manifest as it resolves (into the depot, not the repository). Without `julia`, the projects fail with `julia not found (needed to update Manifest.toml)`.\n" +
	"6. **Failure.** A failed run reports `julia Pkg in <dir>: <last 20 output lines>`, and the scope's changes are dropped."
