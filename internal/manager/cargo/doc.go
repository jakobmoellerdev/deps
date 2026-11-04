package cargo

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const dependencyForms = "`name = \"1.2\"`, `name = { version = \"1.2\", features = [...] }`, `name.version = \"1.2\"` and `[<table>.name]` tables; `package = \"<crate>\"` renames are looked up by the crate name"

// Doc is the reference documentation of the cargo manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates crate requirements in `Cargo.toml`, then refreshes the workspace's `Cargo.lock` with `cargo update`. " +
		"`Cargo.toml` is scanned with a span-keeping TOML reader; only the version bytes are replaced, so comments, formatting and the operator are kept. " +
		"`[patch]`, `[replace]` and `[features]` are not extracted.",
	Files: []string{"`Cargo.toml`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`[dependencies]` and `[target.<cfg>.dependencies]`: " + dependencyForms + " (updates use commit type `fix`)", Datasource: "`crate`; `git` + `tag`: `git-tags`", Versioning: "`semver`; partial versions `tag`"},
		{Name: DepTypeDev, What: "`[dev-dependencies]` (also `dev_dependencies`) and `[target.<cfg>.dev-dependencies]`", Datasource: "`crate`; `git` + `tag`: `git-tags`", Versioning: "`semver`; partial versions `tag`"},
		{Name: DepTypeBuild, What: "`[build-dependencies]` (also `build_dependencies`) and `[target.<cfg>.build-dependencies]`", Datasource: "`crate`; `git` + `tag`: `git-tags`", Versioning: "`semver`; partial versions `tag`"},
		{Name: DepTypeWorkspace, What: "`[workspace.dependencies]`, inherited by members with `name.workspace = true`", Datasource: "`crate`; `git` + `tag`: `git-tags`", Versioning: "`semver`; partial versions `tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "workspace dependency", When: "`workspace = true`: the requirement is inherited from `[workspace.dependencies]`, which is updated instead"},
		{Reason: "path dependency", When: "`path = ...`, also together with `version`"},
		{Reason: "git dependency", When: "`git = ...` without `tag`, with `branch`/`rev` only, or with a non-`http(s)` URL"},
		{Reason: "alternative registry", When: "`registry = ...` or `registry-index = ...`"},
		{Reason: "no version", When: "a table without `version`"},
		{Reason: "unsupported requirement", When: "`*`, `1.*`, `<`, `<=`, `>`, comma-separated lists, build metadata, empty"},
		{Reason: "cannot locate value", When: "an escaped or multi-line string"},
	},
	Sections: []refdoc.Section{
		{Title: "Requirements", Items: []string{
			"Accepted: an optional `^`, `~`, `=` or `>=` (optionally followed by spaces), then `MAJOR[.MINOR[.PATCH[-prerelease]]]`. A bare `1.2` is Cargo's `^1.2`.",
			"The range is **bumped** and keeps its precision (Renovate's `rangeStrategy: bump`): `1.0.75` → `1.0.104` and `~2.7.0` → `~2.9.1` use `semver`; partial `1.2` → `1.4`, `0.4` → `0.5` and `1` → `2` use `tag` versioning on the derived release lines of the `crate` datasource. " +
				"So `1` only changes on a new major and `0.4` on a new 0.x minor, which matches what Cargo considers compatible.",
			"Update types are numeric: `0.4` → `0.5` is `minor`, although Cargo treats it as breaking. Prereleases are only proposed from a prerelease, and yanked versions are never proposed.",
			"In-range updates that only touch `Cargo.lock` (Renovate's `update-lockfile`) are not made; run `cargo update` for those.",
		}},
		{Title: "git dependencies", Body: "`name = { git = \"https://host/path\", tag = \"v1.2.0\" }` updates the tag from the repository's tags (`tag` versioning, so the prefix and number of parts are kept). The URL's query, fragment, trailing slash and `.git` suffix are dropped for the lookup."},
		{Title: "Lockfile post-processing (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run cargo, and prints `note    Cargo.lock changes are computed by apply/run`."}},
	},
}

const lockBody = "1. **Workspace root.** The root is the nearest directory at or above the `Cargo.toml` (within `--dir`) that holds `Cargo.lock`. " +
	"It is accepted if it is the manifest's own directory, or if its `[workspace]` `members` globs include the manifest directory and `exclude` does not. Otherwise the manifest is only patched; no lockfile is created.\n" +
	"2. **Temporary copy.** The root is mirrored to a temp dir: every `Cargo.toml` is copied (patched where changed) together with `Cargo.lock`; all other files are symlinked, and `.git`, `target` and `node_modules` are left out. Path dependencies outside the root are not available there.\n" +
	"3. **Update.** `cargo update --workspace` re-resolves only the requirements that no longer match the lock. " +
	"Then, for every updated crate whose new requirement is a full version, deps runs `cargo update --package <name>@<locked> --precise <version>` when Cargo just locked a newer version of the same compatibility range, so the lock holds the version the pull request names (for example when `minimumReleaseAge` held back the newest release). A version that was already locked is never downgraded, and partial requirements keep Cargo's newest match.\n" +
	"4. **Tool.** `cargo` must be on `PATH` (with `rustup`, a `rust-toolchain.toml` in the root selects the toolchain). Without it the manifests of the root fail with `cargo not found (needed to update Cargo.lock)`.\n" +
	"5. **Failure.** A failed command reports `cargo update in <dir>: <last 20 output lines>`, and the root's changes are dropped."
