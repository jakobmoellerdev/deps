package npm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the npm manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates dependencies, tool versions and engines in `package.json`, and the lockfile along with them or on its own (lockfile-only updates, lock file maintenance). " +
		"`package.json` is stream-parsed; each value's byte span must equal the quoted value in the file, otherwise (for example with `\\u` escapes) the dep is skipped as `cannot locate value`. " +
		"`overrides`, `resolutions` and `bundleDependencies` are not extracted.",
	Files: []string{"`package.json`"},
	DepTypes: []refdoc.DepType{
		{Name: "dependencies", What: "`dependencies` (updates use commit type `fix`)", Datasource: "`npm`", Versioning: "`semver`"},
		{Name: "devDependencies", What: "`devDependencies`", Datasource: "`npm`", Versioning: "`semver`"},
		{Name: "optionalDependencies", What: "`optionalDependencies`", Datasource: "`npm`", Versioning: "`semver`"},
		{Name: "peerDependencies", What: "`peerDependencies`", Datasource: "`npm`", Versioning: "`semver`"},
		{Name: "packageManager", What: "`\"packageManager\": \"npm@x\"`, `pnpm@x`, `yarn@x`; the name is the tool", Datasource: "`npm`", Versioning: "`semver`"},
		{Name: "engines", What: "`engines.{node,npm,pnpm,yarn}`", Datasource: "node: `node-version`; others: `npm`", Versioning: "`semver`; partial versions `tag`"},
		{Name: "volta", What: "`volta.{node,npm,pnpm,yarn}`", Datasource: "node: `node-version`; others: `npm`", Versioning: "`semver`; partial versions `tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "npm alias", When: "`npm:`"},
		{Reason: "local dependency", When: "`file:`, `link:`, `workspace:`, `./`, `../`, `/`, `~/`"},
		{Reason: "git dependency", When: "`github:`, `gitlab:`, `bitbucket:`, `gist:`, `git+…`, `git://`, `git@`, `owner/repo[#ref]`"},
		{Reason: "url dependency", When: "`http(s)://` tarballs"},
		{Reason: "tag", When: "dist-tags such as `latest`, `next`"},
		{Reason: "unsupported range", When: "anything outside npm's range grammar (below), a range without a version (`*`, `x`), empty; for engines, volta and packageManager any range but those described below"},
		{Reason: "packageManager with hash", When: "`pnpm@9.0.0+sha512…`"},
		{Reason: "unsupported packageManager", When: "a tool other than npm, pnpm or yarn"},
		{Reason: "cannot locate value", When: "escaped value"},
	},
	Sections: []refdoc.Section{
		{Title: "Ranges", Items: []string{
			"Accepted: npm's range grammar: `||` alternatives of space-separated comparators (an optional `^`, `~`, `~>`, `>=`, `>`, `<=`, `<` or `=`, spaces allowed after it) or hyphen ranges (`1.2.3 - 2.0.0`), on partial versions with `x`, `X` or `*` wildcards and an optional `v` (`1.x`, `^1.2`, `v1.2.3`, `>=1.2.3 <2`, `^1.0.0 || ^2.0.0`). The dep's version is that of a `^`, `~`, `>=` or `=` range, else the lowest version of the last alternative's first lower bound (`^1.2` → 1.2.0, `1.x` → 1.0.0), else of its upper bound (`<2` → 2.0.0).",
			"How a range moves is [`rangeStrategy`](configuration.md#rangestrategy): by default (`auto` = `update-lockfile`) a version the range admits only moves the locked version, and a version outside it replaces the range (`^5.2.8` → `^6.0.0`); `bump` raises the range for every update (`^1.2.3` → `^1.4.0`), `in-range-only` only for versions it admits; `replace` leaves admitted versions alone; `widen` widens the range for the others (`^1.2.3` → `^1.2.3 || ^2.0.0`, `>=1.2.3 <2` → `>=1.2.3 <4`, an exact version is replaced); `pin` writes the exact version (`^1.2.3` → `1.4.0`): first a `pin` update to the locked version (else the newest one the range admits), unless another update shares its PR bucket, then every newer version. `auto` is `widen` for a range with `||`, as in Renovate.",
			"Rewrites keep each version's precision, wildcards and `v`, and the bytes between comparators: a lower bound takes the new version (`1.x` → `2.x`, `^1.2` → `^2.0`, `>` becomes `>=`), an upper bound the new version reaches moves to the next compatibility boundary above it (`<2.0.0` → `<4.0.0` for 3.1.0) or, for `<=`, to the new version; a hyphen range moves its high end to a version above it and its low end to an admitted one. An admitted version raises the first alternative admitting it (`^1.0.0 || ^2.0.0` → `^1.0.0 || ^2.5.0`); outside the range, `replace` keeps only the replaced last alternative (`^1.0.0 || ^2.0.0` → `^3.1.0`). A range the new version cannot raise (`1.x` with 1.5.0) is not rewritten for it. engines, volta and packageManager always replace their version.",
			"For `engines` and `volta`, a partial `^26` or `10.2` is also accepted. It uses `tag` versioning on the numeric part (`^26` → `^27`).",
			"Versioning is `semver`. Prerelease candidates are only considered from a prerelease, and deprecated versions are dropped.",
		}},
		{Title: "Workspaces", Body: "A dep naming a member of the manifest's own workspace is never updated."},
		{Title: "Locked versions", Body: "Before resolving, deps reads the lock root's `package-lock.json` (`lockfileVersion` 2 or 3; the lockfile the post-processor would update, see below) once per run, streamed, and keeps the `packages` entries of the extracted names: a member's own `<member>/node_modules/<name>` wins over the hoisted `node_modules/<name>`, links are not versions.",
			Items: []string{
				"The locked version is the current version under `update-lockfile` (update types compare it with the new version) and is what OSV is queried with, so an already locked fix is not proposed again. Without a lock, the version in package.json is used.",
				"`pnpm-lock.yaml`, `yarn.lock` and v1 `package-lock.json` are not read: `update-lockfile` and `auto` act as `bump` there, and OSV checks the version in package.json. A `package-lock.json` that does not parse is a run error (`<path>: <err>`), with the same fallback.",
			}},
		{Title: "Lockfile post-processing (`apply`, `run`)", Body: lockfileBody,
			Items: []string{"`plan` does not run installs, and prints `note    package-lock.json changes are computed by apply/run`; lockfile-only updates are listed as `(<type>, lockfile)` without a diff."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, the lockfile of every lock root of a scanned `package.json` (the one picked as above; a root without its own `package.json` is skipped) is regenerated from scratch in the temporary copy, as Renovate does: `package-lock.json` and `pnpm-lock.yaml` are left out and `yarn.lock` is copied empty (so yarn keeps the directory as its project root), then the install command above runs (npm twice, since its first lockfile can be out of sync with `package.json`). " +
			"Every locked package, direct and transitive, of the root and all workspace members moves to the newest version its range admits; the `package.json` files are unchanged, and the lockfile's own root and workspace entries keep their ranges. The tool lookup (`packageManager` version, `npx`) is the same as above. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only lockfiles; `minimumReleaseAge` does not apply to it."},
	},
}

const lockfileBody = "1. **Lock root.** The lock root is the nearest directory at or above the `package.json` (within `--dir`) that holds `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock`. " +
	"It is accepted if it is the manifest's own directory, or if its `package.json` `workspaces` (an array or `{packages}`) or `pnpm-workspace.yaml` `packages` globs match the manifest directory (`!glob` excludes). " +
	"Otherwise the manifest is only patched; no lockfile is created.\n" +
	"2. **Temporary copy.** The root and all member `package.json` files (patched where changed), the lockfile, `.npmrc`, `pnpm-workspace.yaml`, `.yarnrc.yml`, and the files referenced by `yarnPath`/`plugins` are copied to a temp dir. " +
	"So are the local files the install resolves: yarn `patch:` files (from `package.json` and `yarn.lock`), pnpm `patchedDependencies` files (`package.json` `pnpm` and `pnpm-workspace.yaml`), the pnpmfile (`.pnpmfile.cjs`, or the `pnpmfile` setting), and the targets of `file:`, `link:` and `portal:` dependencies, resolutions and overrides (a tarball itself, a directory's `package.json` only). " +
	"Such files may lie above the lock root within the repository (the nearest directory with `.git`), at their relative paths; paths outside it are not copied.\n" +
	"3. **Install.**\n" +
	"\n" +
	"   | Tool | Command |\n" +
	"   |---|---|\n" +
	"   | npm | `npm install --package-lock-only --ignore-scripts --no-audit --no-fund` |\n" +
	"   | pnpm | `pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile` |\n" +
	"   | yarn berry (`.yarnrc.yml`, or `__metadata` in the lock) | `yarn install --mode=update-lockfile`, with `YARN_ENABLE_SCRIPTS=0 YARN_ENABLE_IMMUTABLE_INSTALLS=0` |\n" +
	"   | yarn classic | `yarn install --ignore-scripts --ignore-engines --non-interactive --no-progress` |\n" +
	"\n" +
	"   The environment always includes `npm_config_update_notifier=false` and `COREPACK_ENABLE_DOWNLOAD_PROMPT=0`.\n" +
	"4. **Tool lookup.** deps uses the tool binary from `PATH`. Failing that:\n" +
	"   - if the root's `packageManager` names the tool, `npx --yes <tool>@<version>` (for yarn berry, `npx --yes --package=@yarnpkg/cli-dist@<version> -- yarn`);\n" +
	"   - else `npx --yes <tool>`;\n" +
	"   - else error `<tool> not found (needed to update <lock>)`.\n" +
	"5. **Several lockfiles.** If a root has more than one lockfile, the `packageManager` tool's lockfile wins, else `package-lock.json`, then pnpm, then yarn. `npm-shrinkwrap.json` is not handled.\n" +
	"6. **Lockfile-only updates** (package-lock.json only). The manifests stay unchanged. After the plain install (run only when a manifest of the root changed), npm runs `npm install … <name>@<version>` for the root manifest's updates and `npm install … --workspace=<dir> <name>@<version>` per workspace member. " +
	"npm saves `^<version>` into the lockfile's own package entries (`packages[\"\"]`, `packages[\"<member dir>\"]`); deps restores the range package.json holds, so `npm ci` accepts the lockfile. A missing entry is an error.\n" +
	"7. **Failure.** A failed install reports `<tool> install in <dir>: <last 20 output lines>`, and the root's changes are dropped."
