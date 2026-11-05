package bundler

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	gemForms    = "`gem \"name\"`, `gem \"name\", \"2.241.2\"`, `gem \"name\", \"~> 7.1\", \">= 7.1.2\"` (also as an array), with options (`require:`, `platforms:`, `source:`, …), spread over lines, with parentheses or the `:key => value` hash syntax"
	datasources = "`rubygems` (rubygems.org, or the gem's `source`)"
	versionings = "`ruby`"
)

// Doc is the reference documentation of the bundler manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates gem requirements in `Gemfile`s and moves `Gemfile.lock` with `bundle lock` (in-range updates only touch the lock, as Renovate's `update-lockfile`). " +
		"The Gemfile is read with a small Ruby lexer (strings, symbols, hash labels, `%i[]` literals, comments, heredocs, blocks); only the version bytes of a requirement are rewritten, so quotes, operators and formatting are kept. " +
		"The `ruby` directive, `.ruby-version`, `gemspec` and `*.gemspec` dependencies are not extracted.",
	Files: []string{"`Gemfile` (`gems.rb` is not matched)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`gem` statements outside `group` blocks and without a `group:`/`groups:` option: " + gemForms + " (updates use commit type `fix`)", Datasource: datasources, Versioning: versionings},
		{Name: DepTypeGroup, What: "`gem` statements in a `group :development, :test do … end` block or with a `group:`/`groups:` option", Datasource: datasources, Versioning: versionings},
	},
	Skips: []refdoc.Skip{
		{Reason: skipGit, When: "`git:`, `github:`, `gitlab:`, `bitbucket:`, `gist:` or a `git_source(:name)` option, or a gem in a `git`/`github` block"},
		{Reason: skipPath, When: "`path:`, or a gem in a `path` block"},
		{Reason: skipNonLiteralSource, When: "`source:` or an enclosing `source … do` block that is not a literal string (`source: ENV[\"GEMS\"]`)"},
		{Reason: skipNonLiteral, When: "a requirement that is not a literal string: a variable, a method call, an interpolated or escaped string"},
		{Reason: skipUnsupported, When: "a requirement outside Gem::Requirement's grammar (`latest`, `~> v1`, `=> 1.0`)"},
		{Reason: skipNoLock, When: "a gem without requirements, or with only upper bounds and exclusions (`< 2`, `!= 1.3`), that the Gemfile.lock next to it does not lock (or that has no Gemfile.lock)"},
	},
	Sections: []refdoc.Section{
		{Title: "Gemfile", Items: []string{
			"Statements are `gem`, `source`, `group`, `git_source` and blocks (`… do … end`, `if`/`unless`/`case`/`begin`/`def` … `end`). Gems inside `if`, `platforms`, `install_if` and `env` blocks are extracted as any other.",
			"`source \"<url>\"` statements outside blocks are the global sources of every gem, looked up in order (the first that has the gem answers; none = rubygems.org). A `source \"<url>\" do … end` block or a `source: \"<url>\"` option is the only source of its gems. The legacy `source :rubygems` is rubygems.org.",
			"Gem names are case-sensitive, as on the gem server.",
		}},
		{Title: "Requirements", Items: []string{
			"Gem::Requirement's grammar: `=`, `!=`, `>`, `<`, `>=`, `<=` and the pessimistic `~>` (`~> 7.1` is `>= 7.1, < 8`; `~> 7.1.2` is `>= 7.1.2, < 7.2`), none meaning `=`, on a Ruby version (`2.241.2`, `7.0.8.4`, `1.0.0.rc1`, `1.0.0-beta` = `1.0.0.pre.beta`). Several requirements must all hold; a prerelease only satisfies requirements naming one, as Bundler resolves.",
			"A single requirement with a lower bound (`2.241.2`, `= 1.2`, `~> 7.1`, `>= 1.2`, `> 1.2`) is rewritten: an exact version takes the new version as published (`2.241.2` → `2.242.0`, `7.0.8` → `7.0.8.4`), any other operator keeps its precision (`~> 7.1` → `~> 8.0` for 8.0.1, `~> 1.2.0` → `~> 2.0.0` for 2.0, `>= 1.2` → `>= 2.0`). Requirement lists (`\"~> 7.1\", \">= 7.1.2\"`) and requirements without a lower bound (`< 2`) are never rewritten: only Gemfile.lock moves within them.",
			"`rangeStrategy` (top level or per rule, default `auto` = `update-lockfile`, as in Renovate): versions the requirements admit are lockfile-only updates from the version Gemfile.lock locks (`, lockfile` in the report; the Gemfile is unchanged), newer ones rewrite the requirement. A gem without requirements (`gem \"rake\"`, `>= 0`) only gets lockfile-only updates. With `bump` every newer version rewrites a rewritable requirement, with `in-range-only` only those it admits; `replace` leaves admitted versions alone; `widen` and `pin` act as `replace`. Without a Gemfile.lock, `update-lockfile` proposes only versions outside the requirements.",
			"Versioning `ruby` orders versions as Gem::Version does: numeric segments compare numerically, a letter segment marks a prerelease below its release (`1.0.0.rc1` < `1.0.0`), trailing zeros do not count (`1.2` = `1.2.0`). Update types follow the first three segments (a fourth, as in `7.0.8.4`, is a patch). Prereleases are only proposed from a prerelease.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run Bundler, and prints `note    Gemfile.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `Gemfile.lock` next to a scanned `Gemfile` is refreshed with `bundle lock --update` in the temporary copy, which moves every locked gem, transitive ones included, to the newest version the requirements admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `Gemfile.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a `Gemfile` is the `Gemfile.lock` next to it; without one the Gemfile is only patched.\n" +
	"2. **Locked versions.** Before resolution each Gemfile.lock is read: a gem gets the version of its entry in a `GEM` section (platform suffixes such as `-x86_64-linux-gnu` dropped); `GIT` and `PATH` gems are skipped anyway. An unreadable lock is reported once and its gems are bumped.\n" +
	"3. **Temporary copy.** The Gemfile's directory is mirrored to a temp dir: `Gemfile` (patched) and `Gemfile.lock` are copied, every other entry (`.bundle`, `*.gemspec`, `eval_gemfile` files) is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so `path:` gems outside resolve as in the checkout.\n" +
	"4. **Update.** Every updated gem that the lock has is pinned for one run: its requirement is written as `= <new version>` into the copied Gemfile (a gem without requirements gets one), and `bundle lock --update <gems…> --conservative` moves exactly those gems, and their dependencies only where the new versions need it. " +
	"When a requirement was pinned, the patched Gemfile is put back and `bundle lock` runs again: Bundler keeps the locked versions and writes the Gemfile's requirements into the lock's `DEPENDENCIES`. So the lock holds the version the pull request names, also when `minimumReleaseAge` held back a newer one the rewritten requirement admits. " +
	"Lockfile-only updates are pinned the same way, with the Gemfile unchanged.\n" +
	"5. **Tool.** `bundle` must be on `PATH`; it runs with `BUNDLE_GEMFILE` set to the copy and the repository's `.bundle/config` in effect. Bundler records its own version under `BUNDLED WITH` (and in `CHECKSUMS`): provide the version the lock names to keep those lines unchanged. Without `bundle`, the Gemfiles fail with `bundle not found (needed to update Gemfile.lock)`.\n" +
	"6. **Failure.** A failed command reports `bundle lock in <dir>: <last 20 output lines>`, and the Gemfile's changes are dropped."
