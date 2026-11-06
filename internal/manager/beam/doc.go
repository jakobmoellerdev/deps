package beam

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	hexDatasource = "`hex` (hex.pm; `name:organization` for an organization's package)"
	hexVersioning = "`hex`"
)

// requirementItems document the Elixir version requirements both managers rewrite.
var requirementItems = []string{
	"Elixir's requirement grammar (`Version`): `~> 1.7` (`>= 1.7.0 and < 2.0.0-0`), `~> 1.7.2` (`>= 1.7.2 and < 1.8.0-0`), `==`, `!=`, `>`, `>=`, `<`, `<=` and bare versions (`1.7.2`, which is `==`), joined by `and` and `or` (`and` binding tighter). Versions are full SemVer (`1.7.2`, `2.0.0-rc.0`); only `~>` takes MAJOR.MINOR. As Hex resolves (`allow_pre: false`), a prerelease only satisfies an alternative of which a comparator names a prerelease.",
	"Requirements are rewritten per [rangeStrategy](configuration.md#rangestrategy) (top level or per rule), after Renovate's hex versioning: a version outside the requirement replaces its last alternative (`~> 1.7` → `~> 2.1` for 2.1.0, `~> 1.7.2` → `~> 2.1.0`, `== 0.12.0` → `== 0.13.0`, `>= 1.0.0 and < 2.0.0` → `>= 2.1.0 and < 3.0.0`; `>` becomes `>=`, `!=` stays, upper bounds move past the version at their precision: `< 0.15.0` → `< 0.17.0` for 0.16.0, `<= 0.38.0` → `<= 0.39.0`). " +
		"`widen` moves only the upper bounds of the last alternative (`>= 1.0.0 and < 2.0.0` → `>= 1.0.0 and < 3.0.0`), or appends the replaced last alternative (`~> 1.7` → `~> 1.7 or ~> 2.1`); `bump` raises the lower bounds of the first alternative admitting a newer version (`~> 1.7` → `~> 1.9` for 1.9.3, `>= 1.0.0 and < 2.0.0` → `>= 1.4.0 and < 2.0.0`), `in-range-only` only to versions the requirement admits; `pin` writes `== <version>`. The blanks after operators and the words between comparators are kept. " +
		"`bump` of a requirement with several comparators widens, as in Renovate. A requirement without a lower bound (`>= 0.0.0`, `< 2.0.0`) is ranked from the locked version, and skipped without one.",
	"Versioning `hex` orders versions as SemVer 2.0 does (the build is ignored); versions with a `v` or fewer than three parts are not versions. Update types follow the three parts; prereleases are only proposed from one.",
}

// lockfileItem is the shared Lockfile item: what plan does not run.
func lockfileItem(lock string) string {
	return "`plan` does not run the tool, and prints `note    " + lock + " changes are computed by apply/run`."
}

// DocMix is the reference documentation of the mix manager (docs/reference/managers.md).
var DocMix = refdoc.Manager{
	Name: NameMix,
	Summary: "Updates the Hex requirements and git tags of Elixir dependencies in `mix.exs` and moves `mix.lock` with `mix deps.update` (in-range updates only touch the lock, as Renovate's `update-lockfile`). " +
		"mix.exs is Elixir code: deps reads the dependency tuples of the functions named `deps` or ending in `_deps` (`defp deps do … end`, `defp target_deps(:app), do: […]`) without running it, and rewrites only the requirement or tag strings that change.",
	Files: []string{"`mix.exs` (its lock: the `lockfile:` of its project, as umbrella apps set `lockfile: \"../../mix.lock\"`, else the `mix.lock` next to it)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeProd, What: "a dependency tuple `{:app, \"<requirement>\", …}` without `only:`, or with an `only:` naming `:prod` (updates use commit type `fix`)", Datasource: hexDatasource + ", `git-tags` for a git dependency with a `tag:`", Versioning: hexVersioning + ", `tag`", Datasources: []string{Datasource, GitDatasource}},
		{Name: DepTypeDev, What: "a dependency tuple whose `only:` leaves out `:prod` (`only: [:dev, :test]`)", Datasource: hexDatasource + ", `git-tags` for a git dependency with a `tag:`", Versioning: hexVersioning + ", `tag`", Datasources: []string{Datasource, GitDatasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipPath, When: "a `path:` dependency"},
		{Reason: skipUmbrella, When: "an `in_umbrella: true` dependency"},
		{Reason: skipGit, When: "a `git:`/`github:` dependency without a `tag:` (a `branch:`, a `ref:` commit, the default branch), or of a URL deps cannot list tags of"},
		{Reason: skipNoReq, When: "a Hex dependency without a requirement (`{:dep, override: true}`)"},
		{Reason: skipUnsupported, When: "a requirement that is no string literal (`@version`, a function call) or outside Elixir's grammar"},
		{Reason: skipEscaped, When: "a requirement or tag with escapes or interpolation (`\"~> #{@v}\"`)"},
		{Reason: skipRepo, When: "a `repo:` other than `hexpm` or `hexpm:<organization>` (a self-hosted Hex repository)"},
		{Reason: skipNoLock, When: "a requirement without a lower bound (`>= 0.0.0`, `< 2.0.0`) that the lockfile does not lock (or without one)"},
	},
	Sections: []refdoc.Section{
		{Title: "Dependencies", Items: []string{
			"A tuple's first element is the app (`:phoenix`), the Hex package unless `hex: :other` names another; `organization: \"acme\"` (or `repo: \"hexpm:acme\"`) looks the package up in that private organization (`HEX_API_KEY`). Options after the requirement may be written as a keyword list or `[…]`; a tuple whose options are an expression (`{:hex_core, \"~> 0.18\", opts()}`) keeps its requirement.",
			"Only list elements are dependencies, also inside expressions (`if … do [{:spec_lint, \"~> 0.1\"}] else [] end`); module attributes and other code are not evaluated.",
			"A `git:` or `github: \"owner/repo\"` dependency with a `tag:` is a `git-tags` dep of the repository (https and ssh URLs); its tag is replaced (`tag: \"v2.1.1\"` → `\"v2.2.0\"`).",
		}},
		{Title: "Requirements", Items: append([]string{
			"`auto` is `update-lockfile`, as in Renovate: versions the requirement admits are lockfile-only updates from the version mix.lock locks (`, lockfile` in the report; mix.exs is unchanged), others replace the requirement; a requirement with several comparators (`>= 1.0.0 and < 2.0.0`, `~> 1.0 or ~> 2.0`) is widened instead. Without mix.lock only versions outside the requirement are proposed.",
		}, requirementItems...)},
		{Title: "Lockfile (`apply`, `run`)", Body: mixLockBody, Items: []string{lockfileItem(mixLockFile)}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every mix.lock of a scanned mix.exs (an umbrella's once) is refreshed with `mix deps.update --all` in the mirror, which moves every locked dependency, transitive ones included, to the newest version the requirements admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`; `minimumReleaseAge` does not apply to it."},
	},
}

const mixLockBody = "1. **Lockfile.** The lockfile of a mix.exs is its project's `lockfile:` (umbrella apps: the umbrella's mix.lock), else the `mix.lock` next to it; without one the file is only patched.\n" +
	"2. **Locked versions.** Before resolution each mix.lock is read: a Hex package gets the version of its `{:hex, :name, \"<version>\", …}` entry (an organization's package, with repository `<organization>`, is `name:organization`). An unreadable lock is reported once and its dependencies are bumped.\n" +
	"3. **Temporary mirror.** The lockfile's directory is mirrored to a temp dir: its directories are recreated without `deps`, `_build`, `node_modules`, `.elixir_ls` and `.git`, mix.lock and the patched mix.exs files are copied, every other file is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so `path:` dependencies resolve as in the checkout.\n" +
	"4. **Update.** In the mirror's mix.exs files the requirement of every updated Hex dependency, lockfile-only updates included, is `== <version>`, and `mix deps.update <app>…` runs (with `MIX_ENV=dev`): Mix locks exactly the update's version, even when the rewritten requirement admits a newer one (e.g. one `minimumReleaseAge` held back); as `mix deps.update` does, the updated packages' own dependencies may move too, other packages keep their versions. Git dependencies move to their new tag. The written mix.exs keeps the rewritten requirement.\n" +
	"5. **Tool.** `mix` with Hex must be on `PATH` (e.g. from `erlef/setup-beam`, or `mix local.hex --force`). It fetches the project's dependencies into the mirror (git dependencies are cloned) and evaluates mix.exs; `HEX_API_KEY` authenticates organization packages. Without it, the mix.exs files fail with `mix not found (needed to update mix.lock)`.\n" +
	"6. **Failure.** A failed command reports `mix deps.update in <dir>: <last 20 output lines>`, and the mix.exs's changes are dropped."

// DocGleam is the reference documentation of the gleam manager (docs/reference/managers.md).
var DocGleam = refdoc.Manager{
	Name: NameGleam,
	Summary: "Updates the Hex requirements of Gleam packages in `gleam.toml` and moves `manifest.toml` with `gleam deps update`. " +
		"gleam.toml is scanned as TOML without decoding it, so only the requirement bytes change.",
	Files: []string{"`gleam.toml` (its lock: the `manifest.toml` next to it)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "entries of `[dependencies]`: `name = \"<requirement>\"` or `name = { version = \"<requirement>\" }` (updates use commit type `fix`)", Datasource: "`hex`", Versioning: hexVersioning, Datasources: []string{Datasource}},
		{Name: DepTypeDevDependencies, What: "entries of `[dev-dependencies]` or `[dev_dependencies]`", Datasource: "`hex`", Versioning: hexVersioning, Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipPath, When: "a `{ path = \"…\" }` dependency"},
		{Reason: skipGit, When: "a `{ git = \"…\", ref = \"…\" }` dependency"},
		{Reason: skipNoReq, When: "a dependency without a version string"},
		{Reason: skipUnsupported, When: "a requirement outside Elixir's grammar"},
		{Reason: skipEscaped, When: "a requirement written with escapes"},
		{Reason: skipNoLock, When: "a requirement without a lower bound (`>= 0.0.0`, `< 2.0.0`) that manifest.toml does not lock (or without one)"},
	},
	Sections: []refdoc.Section{
		{Title: "Requirements", Items: append([]string{
			"`auto` is `widen`, as in Renovate: versions the requirement admits are not proposed (enable [lockFileMaintenance](configuration.md#lockfilemaintenance) to move them in manifest.toml), others widen it (`>= 0.44.0 and < 2.0.0` → `>= 0.44.0 and < 3.0.0` for 2.1.0). Set `rangeStrategy: update-lockfile` for lockfile-only updates of admitted versions.",
		}, requirementItems...)},
		{Title: "Lockfile (`apply`, `run`)", Body: gleamLockBody, Items: []string{lockfileItem(gleamLockFile)}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every manifest.toml next to a scanned gleam.toml is refreshed with `gleam deps update` in the mirror, which moves every locked package to the newest version the requirements admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`; `minimumReleaseAge` does not apply to it."},
	},
}

const gleamLockBody = "1. **Lockfile.** The lockfile of a gleam.toml is the `manifest.toml` next to it; without one the file is only patched.\n" +
	"2. **Locked versions.** Before resolution each manifest.toml is read: a package gets the version of its `packages` entry with `source = \"hex\"`. An unreadable manifest is reported once and its dependencies are bumped.\n" +
	"3. **Temporary mirror.** The project directory is mirrored to a temp dir as for mix (without `build`, `node_modules` and `.git`), with gleam.toml and manifest.toml copied.\n" +
	"4. **Update.** In the mirror's gleam.toml the requirement of every updated package, lockfile-only updates included, is `== <version>`, and `gleam deps update <name>…` runs, so Gleam locks exactly the update's version and moves other packages only where the new versions need it; the `[requirements]` records of manifest.toml are then set back to the written gleam.toml's requirements.\n" +
	"5. **Tool.** `gleam` must be on `PATH` (e.g. from `erlef/setup-beam`); it downloads the packages into the mirror. Without it, the gleam.toml files fail with `gleam not found (needed to update manifest.toml)`.\n" +
	"6. **Failure.** A failed command reports `gleam deps in <dir>: <last 20 output lines>`, and the gleam.toml's changes are dropped."
