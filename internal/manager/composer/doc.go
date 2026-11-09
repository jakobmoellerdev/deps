package composer

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	datasources = "`packagist` (Packagist, or the `repositories` of type composer), `git-tags` for a package of a vcs repository keyed by its name"
	versionings = "`composer`"
)

// Doc is the reference documentation of the composer manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the version constraints of PHP packages in `composer.json` and moves `composer.lock` with `composer update` (in-range updates only touch the lock, as Renovate's `update-lockfile`). " +
		"composer.json is read as JSON; only the constraint strings that change are rewritten, so formatting and key order are kept.",
	Files: []string{"`composer.json` (its lock: the `composer.lock` next to it)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeRequire, What: "entries of `require`: `\"vendor/package\": \"<constraint>\"` (updates use commit type `fix`)", Datasource: datasources, Versioning: versionings, Datasources: []string{"packagist", "git-tags"}},
		{Name: DepTypeRequireDev, What: "entries of `require-dev`", Datasource: datasources, Versioning: versionings, Datasources: []string{"packagist", "git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipPlatform, When: "a platform package, any name without a vendor: `php`, `ext-*`, `lib-*`, `composer`, `composer-plugin-api`, `composer-runtime-api`"},
		{Reason: skipUnsupported, When: "a constraint outside Composer's grammar for tagged versions: branches (`dev-main`, `2.x-dev`, `dev-main#abc123`), aliases (`dev-main as 1.0.0`), `self.version`, a bare stability flag (`@dev`)"},
		{Reason: skipEscaped, When: "a constraint written with JSON escapes (`\\u003e=1.0`)"},
		{Reason: skipPath, When: "a package of a `path` repository: keyed by its name in `repositories`, or installed from one per composer.lock (dist type `path`)"},
		{Reason: skipPackageRepo, When: "a package a `package` repository defines"},
		{Reason: skipGitURL, When: "a vcs repository keyed by the package name whose URL is not a git URL deps can list tags of"},
		{Reason: skipNoRepository, When: "`\"packagist.org\": false` and no composer repository whose `only`/`exclude` filters admit the package"},
		{Reason: skipNoLock, When: "a constraint without a lower bound (`*`, `<2.0`) that the composer.lock next to it does not lock to a tagged version (or without composer.lock)"},
	},
	Sections: []refdoc.Section{
		{Title: "Repositories", Items: []string{
			"`repositories` entries (array or object form) of type `composer` are searched in order with their `only`/`exclude` name patterns, then Packagist unless `\"packagist.org\": false` (or `\"packagist\": false`) disables it; the first repository that has the package answers, as Composer's canonical repositories do. See the `packagist` row under [Datasources](#datasources) for the repository formats and `COMPOSER_AUTH`.",
			"A `vcs` (`git`, `github`, `gitlab`, `bitbucket`) repository keyed by a package name (object form, or with a `name`, as Renovate reads it) makes that package a `git-tags` dep of its URL; a vcs repository without a name (Composer reads its composer.json to find out the package) provides the package that composer.lock installed from it (`source.url`), else the package named like its repository path (`acme/tool` from `https://github.com/acme/tool`); its other packages are still looked up in the composer repositories.",
		}},
		{Title: "Constraints", Items: []string{
			"Composer's constraint grammar (composer/semver): `^1.2.3`, `~1.2`, `>=1.0 <2.0` and `>=1.0,<2.0` (blanks after an operator belong to it: `>= 1.0`), `!=`/`<>`, exact versions (`1.2.3`, `==1.2.3`, `v1.2.3`), wildcards (`1.2.*`, `1.x`, `*`), hyphen ranges (`1.0 - 2.0`), `||` and `|` alternatives, and stability flags (`^2.0@beta`). `^0.3` is `>=0.3 <0.4`; `~1.2` is `>=1.2 <2.0` and `~1.2.3` `>=1.2.3 <1.3`. As with Composer's default `minimum-stability: stable`, a version below stable (`2.0.0-RC1`) only satisfies a constraint that names one of its stability or a lower one, or carries such a flag (`@RC`, `@dev`).",
			"Constraints are rewritten per [rangeStrategy](configuration.md#rangestrategy) (top level or per rule), after Renovate's composer versioning: an exact version takes the new version as published, keeping a `v` prefix as written (`v1.2.3` → `v1.4.0`); `^`, `~`, `>=` and `>` keep their precision (`^5.2.8` → `^6.0.1`, `~1.2` → `~1.4`; `>` becomes `>=`), except that a two-part `^`/`~` moving to a new major is written `<major>.0` (`^4.1` → `^5.0` for 5.2.0); wildcards keep theirs (`1.2.*` → `1.4.*`); upper bounds move past the new version (`>=1.0 <2.0` → `>=2.3 <3.0` for 2.3.0, `<=2.6` → `<=3.1.4`). Stability flags, `v` prefixes and the separators between comparators stay.",
			"`auto` is `update-lockfile`, as in Renovate: versions the constraint admits are lockfile-only updates from the version composer.lock locks (`, lockfile` in the report; composer.json is unchanged), others replace the constraint; a constraint with alternatives (`^1.0|^2.0`, `^1 || ^2`) is widened instead (`^1.0|^2.0` → `^1.0|^2.0|^3.0`), and so is it under `bump`. Renovate also widens for `\"type\": \"typo3-cms-extension\"`; deps does not (set `rangeStrategy: widen` for such packages). Without composer.lock only versions outside the constraint are proposed.",
			"`bump` raises the lower bounds of the first alternative admitting a new version (`^1.2` → `^1.4`, `>=1.2 <2.0` → `>=1.4 <2.0`), `in-range-only` only to versions the constraint admits; `widen` moves only the upper bounds of the last alternative (`>=1.0 <2.0` → `>=1.0 <3.0`), or appends the replaced last alternative (`^1.2` → `^1.2 || ^2.0`); `pin` writes the exact version. A constraint without a lower bound (`*`, `<2.0`) only gets lockfile-only updates.",
			"Versioning `composer` orders versions as Composer does: an optional `v`, up to four numeric parts, then `dev` < `alpha` (`a`) < `beta` (`b`) < `RC` < release < `patch` (`p`, `pl`) with their numbers (`1.0.0-beta2`, `2.0.0-RC.1`, `1.0.0-p1`). Branches (`dev-main`, `2.x-dev`) are not versions. Update types follow the first three parts; versions below stable are only proposed from one.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run Composer, and prints `note    composer.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `composer.lock` next to a scanned `composer.json` is refreshed with `composer update` (same options as below) in the temporary copy, which moves every locked package, transitive ones included, to the newest version the constraints admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `composer.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a `composer.json` is the `composer.lock` next to it; without one the file is only patched.\n" +
	"2. **Locked versions.** Before resolution each composer.lock is read: a package gets the version of its entry in `packages` or `packages-dev` (a branch such as `dev-main` counts as none). An unreadable lock is reported once and its packages are bumped.\n" +
	"3. **Temporary copy.** The directory is mirrored to a temp dir: composer.json and composer.lock are copied, every other entry but `vendor` (`auth.json`, path repositories, patches) is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so path repositories outside resolve as in the checkout.\n" +
	"4. **Update.** `composer update <name>:<version>… --with-dependencies --minimal-changes` moves every updated package, lockfile-only updates included, to exactly the update's version (the inline constraint keeps Composer from locking a newer one the rewritten constraint admits, e.g. when `minimumReleaseAge` held it back), and its dependencies only where the new version needs it. " +
	"Every run passes `--no-install --no-scripts --no-plugins --no-audit --no-interaction`: only composer.lock is written. Platform requirements are ignored (`--ignore-platform-reqs`), since the runner's PHP and extensions are not the project's; with a `config.platform.php` in composer.json only extensions and libraries are (`--ignore-platform-req=ext-* --ignore-platform-req=lib-*`), and the PHP requirements are checked against that version. The lock's `content-hash` is recomputed.\n" +
	"5. **Tool.** `composer` (2.7 or later, for `--minimal-changes`) must be on `PATH`, e.g. from `shivammathur/setup-php`; it runs with `COMPOSER` set to the copy, and `COMPOSER_AUTH` and `COMPOSER_HOME` in effect. Without it, the composer.json files fail with `composer not found (needed to update composer.lock)`. " +
	"`composer --version` is read once per binary: Composer 2.0 to 2.6 runs without `--minimal-changes`, so the updated packages still move to exactly their versions, but their dependencies may move to the newest versions the constraints admit; Composer 1 fails with `composer 1.x cannot update composer.lock: deps needs Composer 2.7 or later (2.0 to 2.6 without --minimal-changes)`. Lock file maintenance and `update --lock` run with any version.\n" +
	"6. **Failure.** A failed command reports `composer update in <dir>: <last 20 output lines>`, and the composer.json's changes are dropped."
