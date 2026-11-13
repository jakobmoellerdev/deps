package npm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// depSource and depSources are the datasources of dependency-section and override values.
const depSource = "`npm`; git dependencies: `git-tags` (a tag, a `semver:` range), `git-refs` (a commit)"

var depSources = []string{"npm", "git-tags", "git-refs"}

// Doc is the reference documentation of the npm manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates dependencies, overrides, tool versions and engines in `package.json`, and the lockfile along with them or on its own (lockfile-only updates, lock file maintenance). " +
		"`package.json` is stream-parsed; each value's byte span must equal the quoted value in the file, otherwise (for example with `\\u` escapes) the dep is skipped as `cannot locate value`. " +
		"`bundleDependencies` is not extracted.",
	Files: []string{"`package.json`"},
	DepTypes: []refdoc.DepType{
		{Name: "dependencies", What: "`dependencies` (updates use commit type `fix`); ranges, `npm:` aliases and git dependencies (below)", Datasource: depSource, Versioning: "`semver`; git tags: `semver`, partial `tag`", Datasources: depSources},
		{Name: "devDependencies", What: "`devDependencies`, as `dependencies`", Datasource: depSource, Versioning: "as `dependencies`", Datasources: depSources},
		{Name: "optionalDependencies", What: "`optionalDependencies`, as `dependencies`", Datasource: depSource, Versioning: "as `dependencies`", Datasources: depSources},
		{Name: "peerDependencies", What: "`peerDependencies`, as `dependencies`", Datasource: depSource, Versioning: "as `dependencies`", Datasources: depSources},
		{Name: DepTypeOverrides, What: "npm `overrides`: `\"name\": \"<value>\"` and `\"name@range\": …`, nested objects (`\"parent\": {\"child\": …}`, `\".\"` the parent's own version) at any depth; the name is the overridden package", Datasource: depSource, Versioning: "as `dependencies`", Datasources: depSources},
		{Name: DepTypeResolutions, What: "yarn `resolutions`: `name`, `**/name`, `parent/name`, `name@range` selectors; the name is the last package of the selector", Datasource: depSource, Versioning: "as `dependencies`", Datasources: depSources},
		{Name: DepTypePnpmOverrides, What: "`pnpm.overrides`: `name`, `name@range`, `parent>name` selectors; the name is the package after the last `>`", Datasource: depSource, Versioning: "as `dependencies`", Datasources: depSources},
		{Name: "packageManager", What: "`\"packageManager\": \"npm@x\"`, `pnpm@x`, `yarn@x`, `bun@x`; the name is the tool. A Corepack hash (`pnpm@9.0.0+sha512.<hex>`; `sha224` and `sha256` too) is replaced with the hash of the new version in the same algorithm: of the npm registry tarball, for yarn 2 and later of `https://repo.yarnpkg.com/<version>/packages/yarnpkg-cli/bin/yarn.js` (as Renovate and Corepack)", Datasource: "`npm` (yarn 2 and later: `@yarnpkg/cli-dist`)", Versioning: "`semver`", Datasources: []string{"npm"}},
		{Name: "engines", What: "`engines.{node,npm,pnpm,yarn,bun}`", Datasource: "node: `node-version`; others: `npm` (yarn 2 and later: `@yarnpkg/cli-dist`)", Versioning: "`semver`; partial versions `tag`", Datasources: []string{"node-version", "npm"}},
		{Name: "volta", What: "`volta.{node,npm,pnpm,yarn}`", Datasource: "node: `node-version`; others: `npm` (yarn 2 and later: `@yarnpkg/cli-dist`)", Versioning: "`semver`; partial versions `tag`", Datasources: []string{"node-version", "npm"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipAlias, When: "`npm:<name>@…` whose name is no npm package name"},
		{Reason: "local dependency", When: "`file:`, `link:`, `workspace:`, `./`, `../`, `/`, `~/`"},
		{Reason: skipGit, When: "a git dependency deps cannot look up: `gist:`, `git+file://`, an unparsable URL"},
		{Reason: skipGitNoRef, When: "a git dependency without `#<committish>` (it follows the default branch)"},
		{Reason: skipGitUnversion, When: "a committish that is neither a version tag, a commit nor a `semver:` range of npm's grammar (a branch such as `#main`)"},
		{Reason: "url dependency", When: "`http(s)://` tarballs (as Renovate: a tarball URL has no version deps could look up)"},
		{Reason: skipReference, When: "an override value `$name` (the version of the direct dependency `name`)"},
		{Reason: "tag", When: "dist-tags such as `latest`, `next`"},
		{Reason: "unsupported range", When: "anything outside npm's range grammar (below), a range without a version (`*`, `x`), empty; for packageManager anything but a full version"},
		{Reason: "packageManager with hash", When: "a hash deps cannot compute: `sha1` or another algorithm, a hex digest of the wrong length, a `bun@…` hash (Corepack does not install bun)"},
		{Reason: "unsupported packageManager", When: "a tool other than npm, pnpm, yarn or bun"},
		{Reason: "cannot locate value", When: "escaped value"},
	},
	Sections: []refdoc.Section{
		{Title: "Ranges", Items: []string{
			"Accepted: npm's range grammar: `||` alternatives of space-separated comparators (an optional `^`, `~`, `~>`, `>=`, `>`, `<=`, `<` or `=`, spaces allowed after it) or hyphen ranges (`1.2.3 - 2.0.0`), on partial versions with `x`, `X` or `*` wildcards and an optional `v` (`1.x`, `^1.2`, `v1.2.3`, `>=1.2.3 <2`, `^1.0.0 || ^2.0.0`). The dep's version is that of a `^`, `~`, `>=` or `=` range, else the lowest version of the last alternative's first lower bound (`^1.2` → 1.2.0, `1.x` → 1.0.0), else of its upper bound (`<2` → 2.0.0).",
			"How a range moves is [`rangeStrategy`](configuration.md#rangestrategy): by default (`auto` = `update-lockfile`) a version the range admits only moves the locked version, and a version outside it replaces the range (`^5.2.8` → `^6.0.0`); `bump` raises the range for every update (`^1.2.3` → `^1.4.0`), `in-range-only` only for versions it admits; `replace` leaves admitted versions alone; `widen` widens the range for the others (`^1.2.3` → `^1.2.3 || ^2.0.0`, `>=1.2.3 <2` → `>=1.2.3 <4`, an exact version is replaced); `pin` writes the exact version (`^1.2.3` → `1.4.0`): first a `pin` update to the locked version (else the newest one the range admits), unless another update shares its PR bucket, then every newer version. `auto` is `widen` for peerDependencies and for a range with `||`, as in Renovate.",
			"Rewrites keep each version's precision, wildcards and `v`, and the bytes between comparators: a lower bound takes the new version (`1.x` → `2.x`, `^1.2` → `^2.0`, `>` becomes `>=`), an upper bound the new version reaches moves to the next compatibility boundary above it (`<2.0.0` → `<4.0.0` for 3.1.0) or, for `<=`, to the new version; a hyphen range moves its high end to a version above it and its low end to an admitted one. An admitted version raises the first alternative admitting it (`^1.0.0 || ^2.0.0` → `^1.0.0 || ^2.5.0`); outside the range, `replace` keeps only the replaced last alternative (`^1.0.0 || ^2.0.0` → `^3.1.0`). A range the new version cannot raise (`1.x` with 1.5.0) is not rewritten for it. packageManager always replaces its version; for engines and volta see below.",
			"For `engines` and `volta`, a full version with an optional `^`, `~`, `>=` or `=` keeps its operator and only its version is replaced (`>=26.1.0` → `>=27.0.0`), as is a partial `^26` or `10.2`, with `tag` versioning on the numeric part (`^26` → `^27`). Any other range of the grammar (`>= 22 <27`, `^24.15.0 || ^26.13.0`, `22.x`, `18 - 22`) is a range as in the dependency sections, moved under `rangeStrategy`: by default a version it admits is left alone (the lockfile does not lock tools) and one outside it replaces it (`>= 22 <27` → `>= 28 <29` for 28.1.0), while `||` ranges widen (`^24.15.0 || ^26.13.0` → `^24.15.0 || ^26.13.0 || ^28.1.0`), as in Renovate. `*`, `x` and dist-tags (`latest`) stay skipped.",
			"Versioning is `semver`. Prerelease candidates are only considered from a prerelease, and deprecated versions are dropped.",
		}},
		{Title: "Workspaces", Body: "A dep naming a member of the manifest's own workspace is never updated."},
		{Title: "Aliases", Body: "`\"alias\": \"npm:<name>@<range>\"` is the dep `<name>` (Renovate's package alias): the range after `@` is read and rewritten as any range, and a replacement rewrites `<name>`, keeping the alias the code imports. `npm:<range>` without a name (yarn berry resolutions) is the key's own package. The lockfile keys aliases by the alias, so they count as not locked: a version the range admits is proposed only under `bump`."},
		{Title: "Git dependencies", Body: "The values npm installs from a git repository are looked up by their repository (the dep's source; its name stays the key): `github:`, `gitlab:` and `bitbucket:` shorthands, GitHub's `owner/repo`, `git+https://`, `git+ssh://` (also `git+ssh://git@host:owner/repo`), `git://`, `git@host:owner/repo`, and `https://` URLs ending in `.git` or naming a github.com, gitlab.com or bitbucket.org repository, on any git host (Renovate: GitHub only). The committish after `#` decides:",
			Items: []string{
				"a version tag (`#v1.2.3`, `#1.2`) is a `git-tags` dep: the tag is replaced by a newer one (`semver` versioning; partial tags `tag`), never digest-pinned (Renovate's `pinDigests: false`);",
				"`#semver:<range>` is a `git-tags` range, rewritten under `rangeStrategy` as any range (not locked: a version the range admits is proposed only under `bump`);",
				"a commit (7 to 40 hex digits) tracks the default branch head (`git-refs` `HEAD`): digest updates write the new commit abbreviated to the length written (`#1a2b3c4` → `#5d6e7f8`);",
				"after any of them the lockfile is regenerated as for other manifest changes.",
			}},
		{Title: "Overrides", Body: "`overrides`, `resolutions` and `pnpm.overrides` values are read as dependency values (ranges, aliases, git dependencies) and default to updated, as in Renovate. A selector other than the bare name (`name@range`, `**/name`, `parent>name`) is not renamed by a replacement. Overrides are not locked under their name: a version the range admits is proposed only under `bump` (an exact version, the common override, moves with every update)."},
		{Title: "Replacements", Body: "Dependency-section entries can be [replaced](configuration.md#rulesreplacementname) by another package: the key is renamed and the range keeps its operator for the new version; the lockfile is regenerated as for other manifest changes."},
		{Title: "Locked versions", Body: "Before resolving, deps reads the lock root's lockfile (the one the post-processor would update, see below) once per run and keeps only the entries of the extracted deps:",
			Items: []string{
				"`package-lock.json` and `npm-shrinkwrap.json` (`lockfileVersion` 2 or 3), streamed: the `packages` entries, where a member's own `<member>/node_modules/<name>` wins over the hoisted `node_modules/<name>`; links are not versions. `lockfileVersion` 1: the top-level `dependencies` entries.",
				"`pnpm-lock.yaml` (lockfile versions 5.x to 9): the `importers` entry of the manifest's directory (`.` for the root; the top-level sections of a single-project lockfile before version 9), without the peer suffix (`18.3.1(react@18.3.1)`, 5.x `_react@…`). Only each YAML document's head up to its `packages:` is parsed; pnpm 11 and later write `packageManager` and `configDependencies` into a document of their own first.",
				"`yarn.lock`, classic and berry: the entry whose header lists the descriptor `<name>@<range>` (berry `<name>@npm:<range>`), with the range as package.json writes it. Descriptors are shared by the whole workspace.",
				"`bun.lock` (text; trailing commas allowed): the `packages` entry `<member name>/<name>`, else the hoisted `<name>`; its first element is `<name>@<version>`.",
				"`link:`, `workspace:`, `file:`, git and alias resolutions are not versions: such deps count as not locked.",
				"The locked version is the current version under `update-lockfile` (update types compare it with the new version) and is what OSV is queried with, so an already locked fix is not proposed again. Without a lock, the version in package.json is used.",
				"A lockfile that does not parse is a run error (`<path>: <err>`); its deps are bumped under `update-lockfile` and `auto`, and OSV checks the version in package.json. So is a `package-lock.json` without `lockfileVersion`. `bun.lockb` (bun's binary lockfile) is not a lockfile to deps.",
			}},
		{Title: "Lockfile post-processing (`apply`, `run`)", Body: lockfileBody,
			Items: []string{"`plan` does not run installs, and prints `note    package-lock.json changes are computed by apply/run`; lockfile-only updates are listed as `(<type>, lockfile)` without a diff."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, the lockfile of every lock root of a scanned `package.json` (the one picked as above; a root without its own `package.json` is skipped) is regenerated from scratch in the temporary copy, as Renovate does: `package-lock.json`, `npm-shrinkwrap.json`, `pnpm-lock.yaml` and `bun.lock` are left out and `yarn.lock` is copied empty (so yarn keeps the directory as its project root), then the install command above runs (npm twice, since its first lockfile can be out of sync with `package.json`). " +
			"Every locked package, direct and transitive, of the root and all workspace members moves to the newest version its range admits; the `package.json` files are unchanged, and the lockfile's own root and workspace entries keep their ranges. The tool lookup (`packageManager` version, `npx`) is the same as above. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only lockfiles; `minimumReleaseAge` does not apply to it."},
	},
}

const lockfileBody = "1. **Lock root.** The lock root is the nearest directory at or above the `package.json` (within `--dir`) that holds `npm-shrinkwrap.json`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock` or `bun.lock`. " +
	"It is accepted if it is the manifest's own directory, or if its `package.json` `workspaces` (an array or `{packages}`) or `pnpm-workspace.yaml` `packages` globs match the manifest directory (`!glob` excludes). " +
	"Otherwise the manifest is only patched; no lockfile is created.\n" +
	"2. **Temporary copy.** The root and all member `package.json` files (patched where changed), the lockfile, `.npmrc`, `pnpm-workspace.yaml`, `.yarnrc.yml`, `bunfig.toml`, and the files referenced by `yarnPath`/`plugins` are copied to a temp dir. " +
	"So are the local files the install resolves: yarn `patch:` files (from `package.json` and `yarn.lock`), pnpm `patchedDependencies` files (`package.json` `pnpm` and `pnpm-workspace.yaml`), the pnpmfile (`.pnpmfile.cjs`, or the `pnpmfile` setting), and the targets of `file:`, `link:` and `portal:` dependencies, resolutions and overrides (a tarball itself, a directory's `package.json` only). " +
	"Such files may lie above the lock root within the repository (the nearest directory with `.git`), at their relative paths; paths outside it are not copied. " +
	"npm 12 no longer reads or writes `npm-shrinkwrap.json`, so it is copied as `package-lock.json` (the same format) and the result is written back under its own name.\n" +
	"3. **Install.**\n" +
	"\n" +
	"   | Tool | Command |\n" +
	"   |---|---|\n" +
	"   | npm | `npm install --package-lock-only --ignore-scripts --no-audit --no-fund` |\n" +
	"   | pnpm | `pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile` |\n" +
	"   | yarn berry (`.yarnrc.yml`, or `__metadata` in the lock) | `yarn install --mode=update-lockfile`, with `YARN_ENABLE_SCRIPTS=0 YARN_ENABLE_IMMUTABLE_INSTALLS=0` |\n" +
	"   | yarn classic | `yarn install --ignore-scripts --ignore-engines --non-interactive --no-progress` |\n" +
	"   | bun | `bun install --lockfile-only --ignore-scripts` |\n" +
	"\n" +
	"   The environment always includes `npm_config_update_notifier=false` and `COREPACK_ENABLE_DOWNLOAD_PROMPT=0`.\n" +
	"4. **Tool lookup.** deps uses the tool binary from `PATH`. Failing that:\n" +
	"   - if the root's `packageManager` names the tool, `npx --yes <tool>@<version>` (for yarn berry, `npx --yes --package=@yarnpkg/cli-dist@<version> -- yarn`);\n" +
	"   - else `npx --yes <tool>`;\n" +
	"   - else error `<tool> not found (needed to update <lock>)`.\n" +
	"5. **Several lockfiles.** If a root has more than one lockfile, the `packageManager` tool's lockfile wins, else `npm-shrinkwrap.json`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, then `bun.lock`.\n" +
	"6. **Lockfile-only updates.** The manifests stay unchanged: only the lockfile is read back from the copy. The plain install runs first only when a manifest of the root changed (yarn and bun install from the pinned manifests instead). Then, per tool:\n" +
	"\n" +
	"   | Tool | Commands |\n" +
	"   |---|---|\n" +
	"   | npm | `npm install … <name>@<version>` for the root manifest's updates, `npm install … --workspace=<dir> <name>@<version>` per workspace member; npm saves `^<version>` into the lockfile's own package entries (`packages[\"\"]`, `packages[\"<member dir>\"]`), and deps restores the range package.json holds, so `npm ci` accepts the lockfile (a missing entry is an error). A v1 lockfile comes back as npm writes it (version 3). |\n" +
	"   | pnpm | `pnpm update --no-save --lockfile-only --ignore-scripts --filter <sel> <name>@<version>…` per manifest (`<sel>` is `.` for the root, `./<dir>` for a member); the specifiers stay. |\n" +
	"   | yarn (classic and berry) | an install from manifests whose updated ranges are replaced by the new versions, which adds `<name>@<version>` entries (berry: `<name>@npm:<version>`); deps moves each `<name>@<range>` descriptor into that entry (out of its old one, which goes away when nothing is left), then installs from the real manifests, which drops the pinned descriptors nothing else uses. Two installs apply any number of updates. |\n" +
	"   | bun | an install from manifests pinned the same way; deps puts the ranges package.json holds back into `bun.lock`'s `workspaces` entries. |\n" +
	"\n" +
	"   Every tool locks exactly `<version>`. In yarn, a descriptor is shared by every manifest and dependency that requests the same `<name>@<range>`, so they all move.\n" +
	"7. **Failure.** A failed command reports `<tool> <subcommand> in <dir>: <last 20 output lines>` (e.g. `pnpm update in …`), and the root's changes are dropped."
