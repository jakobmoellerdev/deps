package cargo

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	dependencyForms = "`name = \"1.2\"`, `name = { version = \"1.2\", features = [...] }`, `name.version = \"1.2\"` and `[<table>.name]` tables; `package = \"<crate>\"` renames are looked up by the crate name"
	datasources     = "`crate` (crates.io, or the `registry`/`registry-index` of the entry); `git` + `tag`: `git-tags`"
	versionings     = "`cargo`; git tags `tag`"
)

// depDatasources are the datasources of the versioned dep types: registries and git tags.
var depDatasources = []string{Datasource, "git-tags"}

// Doc is the reference documentation of the cargo manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates crate requirements in `Cargo.toml`, moves `Cargo.lock` with `cargo update` (in-range updates only touch the lock, as Renovate's `update-lockfile`), and groups the crates of well-known Rust monorepos. " +
		"`Cargo.toml` is read with the span-keeping TOML parser `internal/tomlspan`; only the requirement's bytes are rewritten, so comments, formatting, operators and precision are kept. " +
		"`[features]` and `[package]` are not extracted.",
	Files: []string{"`Cargo.toml`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`[dependencies]` and `[target.<cfg>.dependencies]`: " + dependencyForms + " (updates use commit type `fix`)", Datasource: datasources, Versioning: versionings, Datasources: depDatasources},
		{Name: DepTypeDev, What: "`[dev-dependencies]` (also `dev_dependencies`) and `[target.<cfg>.dev-dependencies]`", Datasource: datasources, Versioning: versionings, Datasources: depDatasources},
		{Name: DepTypeBuild, What: "`[build-dependencies]` (also `build_dependencies`) and `[target.<cfg>.build-dependencies]`", Datasource: datasources, Versioning: versionings, Datasources: depDatasources},
		{Name: DepTypeWorkspace, What: "`[workspace.dependencies]`, inherited by members with `name.workspace = true`", Datasource: datasources, Versioning: versionings, Datasources: depDatasources},
		{Name: DepTypePatch, What: "every `[patch.<source>]` table (`[patch.crates-io]`, `[patch.\"https://…\"]`): versioned and git-tag entries; path patches are skipped", Datasource: datasources, Versioning: versionings, Datasources: depDatasources},
		{Name: DepTypeReplace, What: "`[replace]`: the git tag of `\"<name>:<version>\" = { git = …, tag = … }` (the key's version is the package being replaced and stays)", Versioning: "`tag`", Datasources: []string{"git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "workspace dependency", When: "`workspace = true`: the requirement is inherited from `[workspace.dependencies]`, which is updated instead"},
		{Reason: "path dependency", When: "`path = ...`, also together with `version`"},
		{Reason: "git dependency", When: "`git = ...` without `tag`, with `branch`/`rev` only, or with a non-`http(s)` URL"},
		{Reason: "unknown registry <name>", When: "`registry = \"<name>\"` without `CARGO_REGISTRIES_<NAME>_INDEX` or a `[registries.<name>] index` in the config files (Cargo cannot resolve it either)"},
		{Reason: "no version", When: "a table without `version`"},
		{Reason: "unsupported requirement", When: "anything outside Cargo's grammar: `~>1`, `1.2 - 2.0`, `v1`, `>=1.*`, empty"},
		{Reason: "cannot locate value", When: "an escaped string"},
		{Reason: "no Cargo.lock version for a requirement without a floor", When: "`*`, `<2` or `<=1.5` (nothing to raise) without a Cargo.lock entry of the crate"},
	},
	Sections: []refdoc.Section{
		{Title: "Requirements", Items: []string{
			"Cargo's grammar: comparators `^`, `~`, `=`, `>`, `>=`, `<`, `<=` (optionally followed by spaces) or none on `MAJOR[.MINOR[.PATCH[-pre][+build]]]`, wildcards `*`, `1.*`, `1.2.x`, and comma-separated lists (`\">= 0.10, < 0.11\"`). A bare `1.2` is `^1.2`; `Dep.Constraint` carries that translation for the engine.",
			"The version a bump raises is that of the first lower bound (`^`, `~`, `=`, `>`, `>=`, none, or a wildcard's prefix). When the requirement is rewritten, every lower bound takes the new version at the precision it was written with, upper bounds `<B` that the new version reaches move to the next compatibility boundary above it, `<=B` to the new version: `1.2` → `1.4` or `2.0`, `~2.7.0` → `~2.9.1`, `1.*` → `2.*`, `\">= 0.6.8, < 0.7\"` → `\">= 0.7.1, < 0.8\"`, `<2` → `<3`.",
			"`rangeStrategy` (top level or per rule, default `auto` = `update-lockfile`, as in Renovate): versions the requirement admits are lockfile-only updates from the version Cargo.lock locks (`, lockfile` in the report; the manifest is unchanged); newer ones rewrite the requirement. With `bump` every newer version rewrites it, with `in-range-only` only those it admits. Without a Cargo.lock, `update-lockfile` proposes only versions outside the requirement. `replace` leaves admitted versions alone; `widen` also does and keeps the lower bounds, moving only the upper ones (`\">=1.2, <2\"` → `\">=1.2, <4\"` for 3.1.0) or, when there are none, writing `>=<lower bound>, <boundary above the new version>` (`1.2` → `\">=1.2, <3\"` for 2.0.1; `=1.2.3` is replaced); `pin` writes `=<version>`: a `pin` update to the locked version (else the newest admitted one) unless another update shares its PR bucket, then every newer version.",
			"A requirement without a floor (`*`, `<2`) has nothing to raise: its current version is the locked one, and under `bump` too its in-range updates only move the lock.",
			"Versioning `cargo` follows Cargo's compatibility rules: leaving the range of the leftmost non-zero part is `major` (`1.x` → `2.0`, `0.4` → `0.5`, `0.0.3` → `0.0.4`); within it a minor change of a `1.x` version is `minor`, everything else `patch`. `separateMultipleMajor` buckets are named after that range (`v0.5`). Partial requirements are compared at their precision against the release lines the `crate` datasource derives. Prereleases are only proposed from a prerelease, yanked versions never.",
		}},
		{Title: "Registries", Items: []string{
			"`registry = \"<name>\"` looks the crate up in that registry: the index from `CARGO_REGISTRIES_<NAME>_INDEX`, else `[registries.<name>] index` of the `.cargo/config.toml` (or legacy `.cargo/config`) files from the manifest directory up to `--dir`, nearest first, then `$CARGO_HOME/config.toml` (`~/.cargo`). `registry = \"crates-io\"` is crates.io.",
			"`registry-index = \"<url>\"` names the index directly. `sparse+https://…` indexes are read over HTTP; any other URL is a git index, read from a shallow blob-less `git clone` (removed when the run ends) with `git cat-file`.",
			"The token of a registry is `CARGO_REGISTRIES_<NAME>_TOKEN`, else `[registries.<name>] token` of `$CARGO_HOME/credentials.toml`, else of the config files; it is sent as `Authorization`, as Cargo does, and only to the index host (a `config.json` `api` on another host gets no credentials). " +
				"Without a token, the platform token is sent to its own host as Basic auth, and after a 401 the git credential helper's credentials for the index host. Git indexes use git's credential helpers.",
			"`[source.crates-io] replace-with` is followed (also through chains) to a mirror with `registry = \"…\"`; `directory`/`local-registry` replacements vendor crates.io, which stays the lookup.",
			"OSV advisories are only looked up for crates.io crates.",
		}},
		{Title: "git dependencies", Body: "`name = { git = \"https://host/path\", tag = \"v1.2.0\" }` (also in `[patch]` and `[replace]`) updates the tag from the repository's tags (`tag` versioning, so the prefix and number of parts are kept). The URL's query, fragment, trailing slash and `.git` suffix are dropped for the lookup."},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run cargo, and prints `note    Cargo.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `Cargo.lock` of a scanned `Cargo.toml` (its workspace root, as above) is refreshed with `cargo update` in the temporary copy, which moves every locked crate to the newest version its requirement admits. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `Cargo.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Workspace root.** The root is the nearest directory at or above the `Cargo.toml` (within `--dir`) that holds `Cargo.lock`. " +
	"It is accepted if it is the manifest's own directory, or if its `[workspace]` `members` globs include the manifest directory and `exclude` does not. Otherwise the manifest has no lock: it is only patched, and `update-lockfile` proposes only versions outside the requirement.\n" +
	"2. **Locked versions.** Before resolution the root's Cargo.lock is read: each requirement gets the highest registry package of its crate that it admits. An unreadable lock is reported once and its requirements are bumped.\n" +
	"3. **Temporary copy.** The root is mirrored to a temp dir: every `Cargo.toml` is copied (patched where changed) together with `Cargo.lock`; all other files are symlinked, and `.git`, `target` and `node_modules` are left out. When the root sits below the repository top (the nearest ancestor with `.git`), the directories above it are recreated with their other entries symlinked, so path dependencies outside the root (`path = \"../crates/x\"`) resolve as in the checkout.\n" +
	"4. **Update.** `cargo update --workspace` re-resolves only the requirements that no longer match the lock. " +
	"Then, for every rewritten requirement whose version is full, deps runs `cargo update --package <name>@<locked> --precise <version>` when Cargo just locked a newer version of the same compatibility range, so the lock holds the version the pull request names (for example when `minimumReleaseAge` held back the newest release). A version that was already locked is never downgraded, and partial requirements keep Cargo's newest match. " +
	"A rewritten requirement that still admits the locked version (`<0.10` → `<2.0` with 0.9.0 locked) leaves the lock alone under `cargo update --workspace`; such a crate (also for a security update) is resolved once with its requirement written as `=<new>` in the temporary copy, then the rewritten requirement is restored and resolved again, so the lock holds the pull request's version while other dependents that still need the old version keep it locked beside it. " +
	"A lockfile-only update runs `cargo update --package <name>@<from> --precise <new>`, from the entry earlier pins left in the locked version's compatibility range (pinning a crate can move its dependencies), and is skipped when that entry already has the version.\n" +
	"5. **Tool.** `cargo` must be on `PATH` (with `rustup`, a `rust-toolchain.toml` in the root selects the toolchain). Without it the manifests of the root fail with `cargo not found (needed to update Cargo.lock)`.\n" +
	"6. **Failure.** A failed command reports `cargo update in <dir>: <last 20 output lines>`, and the root's changes are dropped."
