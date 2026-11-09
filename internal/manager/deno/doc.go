package deno

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	specDatasources = "`jsr` for `jsr:` specifiers, `npm` for `npm:` specifiers, `deno` for deno.land URLs"
	specVersionings = "`semver` (npm ranges for `jsr:` and `npm:` specifiers)"
)

// specTypes are the datasources of the specifier dep types.
var specTypes = []string{"jsr", "npm", "deno"}

// Doc is the reference documentation of the deno manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the `jsr:` and `npm:` package specifiers and the deno.land module URLs of Deno configuration files, and moves `deno.lock` with `deno install --lockfile-only`. " +
		"The configuration is read as JSON with comments; only the version strings that change are rewritten, so formatting, comments and key order are kept.",
	Files: []string{"`deno.json`, `deno.jsonc` (its lock: the configuration's `lock` file, `deno.lock` next to it by default, or the workspace root's for a `workspace` member)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeImports, What: "values of the import map `imports`: `jsr:@std/path@^1.0.0`, `npm:chalk@5` (`npm:/name@…` too, subpaths after the version kept), `https://deno.land/x/oak@v12.6.1/mod.ts`, `https://deno.land/std@0.224.0/`", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeScopes, What: "values of the import map `scopes`, in the same forms", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeTasks, What: "versioned specifiers in the commands of `tasks` written as strings (`deno run -A npm:create-vite@^6.0.0`); unversioned ones are not pins", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeTaskCommand, What: "versioned specifiers in the `command` of `tasks` written as objects", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeTypes, What: "entries of `compilerOptions.types` (`npm:@types/node@^22`)", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeJSXImportSource, What: "`compilerOptions.jsxImportSource` (`npm:preact@^10`)", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeJSXImportSourceTypes, What: "`compilerOptions.jsxImportSourceTypes`", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
		{Name: DepTypeLintPlugins, What: "entries of `lint.plugins` (`jsr:@scope/plugin@1`)", Datasource: specDatasources, Versioning: specVersionings, Datasources: specTypes},
	},
	Skips: []refdoc.Skip{
		{Reason: skipLocal, When: "a local path (`./`, `../`, `/`, `file:`)"},
		{Reason: skipURL, When: "a URL or specifier other than `jsr:`, `npm:` and deno.land modules (`https://esm.sh/…`, `node:fs`, `https://deno.land/manual`)"},
		{Reason: skipBare, When: "a bare specifier resolved through the import map (`\"jsxImportSource\": \"preact\"`)"},
		{Reason: skipUnversioned, When: "a specifier without a version (`jsr:@std/path`, `npm:chalk`, `https://deno.land/x/oak/mod.ts`)"},
		{Reason: skipName, When: "a `jsr:` name that is not `@scope/name`, an invalid npm package name"},
		{Reason: skipVersion, When: "a deno.land URL whose version is not a semantic version"},
		{Reason: skipEscaped, When: "a value written with JSON escapes"},
		{Reason: "tag", When: "a dist-tag instead of a range (`npm:chalk@latest`)"},
		{Reason: "unsupported range", When: "a version requirement outside npm's range grammar"},
	},
	Sections: []refdoc.Section{
		{Title: "Specifiers", Items: []string{
			"`jsr:` and `npm:` specifiers are `<scheme>:<name>@<requirement>[/<subpath>]`; deno takes a subset of npm's range grammar there (`^1.2.3`, `~1.2.3`, `1.2.3`, `1`, `1.2`, `1.x`; no blanks, comparators or `||`). The requirement is rewritten per [rangeStrategy](configuration.md#rangestrategy) like an npm dependency range (`auto` = `update-lockfile`; `^1.2.3` → `^1.4.0` under `bump`, `^2.1.0` for 2.1.0 under `replace`, `2.1.0` under `pin`); `widen` is `replace`, as in Renovate's deno versioning, since a specifier cannot hold alternatives. The name is the package (`@std/path`, `chalk`; the import map key is the alias the code imports).",
			"deno.land URLs (`https://deno.land/x/<module>@<version>/…`, `https://deno.land/std@<version>/…`) are deps named `https://deno.land/x/<module>` or `https://deno.land/std` (Renovate's names), whose version is replaced as written (`v12.6.1` → `v17.2.0`).",
			"Specifiers in tasks are not locked: their updates only change the configuration file.",
			"An `importMap` file the configuration names is not read; nor are `package.json` files of a Deno workspace (the npm manager updates them, with npm's lockfiles), nor specifiers written in source files.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run deno, and prints `note    deno.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, the lockfile of every scope is regenerated in the temporary copy: deps keeps only its `version` and `remote` (the hashes of the URL modules the code imports) and runs `deno install --lockfile-only`, which locks every package of the configuration files, transitive ones included, at the newest version the requirements admit. As in Renovate, packages that only source files import by specifier (`import \"jsr:@std/fmt@1\"`) drop out of the lockfile until deno runs on that code. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only the lockfiles; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a configuration file is its `lock` (a path, or `lock.path`; `deno.lock` next to it by default; none for `lock: false`); a `workspace` member's is the workspace root's. Without one the file is only patched.\n" +
	"2. **Locked versions.** Before resolution each lockfile (version 3 to 5) is read: a `jsr:` or `npm:` requirement gets the version locked for it, or, since deno writes requirements normalized (`^1.0.0` as `1`), the newest version locked for the package that the requirement admits. An unreadable lock is reported once and its packages are bumped.\n" +
	"3. **Temporary copy.** The scope directory is mirrored to a temp dir: the configuration files of the root and of every workspace member and the lockfile are copied, the directories leading to them recreated, and every other entry but `node_modules`, the root's `vendor` and `.git` symlinked; the directories above it up to the repository top are recreated with their entries symlinked.\n" +
	"4. **Update.** In the copy, every requirement of an updated package that admits the update's version, lockfile-only updates included, is set to exactly that version and `deno install --lockfile-only --frozen=false` locks it; the exact requirement keeps deno from locking a newer version (e.g. when `minimumReleaseAge` held it back). As deno.lock records each requirement as written, the pinned requirements are renamed to the ones of the updated configuration, the lockfile's `workspace` section is dropped and deno runs once more on the updated configuration, which writes the lockfile in deno's own form without resolving anything anew. Only the lockfile is taken back.\n" +
	"5. **Tool.** `deno` 2.6 or later (for `--lockfile-only`) must be on `PATH` (e.g. from `denoland/setup-deno`); it writes lockfile version 5. `DENO_DIR`, `NPM_CONFIG_REGISTRY`, `.npmrc` and `JSR_URL` are in effect; the update check is off (`DENO_NO_UPDATE_CHECK`). Without the tool, the configuration files fail with `deno not found (needed to update deno.lock)`.\n" +
	"6. **Failure.** A failed command reports `deno install in <dir>: <last 20 output lines>`, and the configuration's changes are dropped."
