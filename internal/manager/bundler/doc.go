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
	Summary: "Updates gem requirements and the `ruby` directive in `Gemfile`s (and `gems.rb`) and the dependencies of `*.gemspec` files, and moves `Gemfile.lock` (`gems.locked`) with `bundle lock` (in-range updates only touch the lock, as Renovate's `update-lockfile`). " +
		"The files are read with a small Ruby lexer (strings, symbols, hash labels, `%i[]` literals, comments, heredocs, blocks); only the requirement strings that change are rewritten, and of those only the version where the operator stays, so quotes, operators and formatting are kept.",
	Files: []string{"`Gemfile`, `gems.rb` (Bundler's alternative name, locked in `gems.locked`)", "`*.gemspec` (beyond Renovate, whose bundler manager reads Gemfiles only)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "`gem` statements outside `group` blocks and without a `group:`/`groups:` option: " + gemForms + " (updates use commit type `fix`)", Datasource: datasources, Versioning: versionings, Datasources: []string{Datasource}},
		{Name: DepTypeGroup, What: "`gem` statements in a `group :development, :test do … end` block or with a `group:`/`groups:` option", Datasource: datasources, Versioning: versionings, Datasources: []string{Datasource}},
		{Name: DepTypeRuby, What: "the `ruby` directive (name `ruby`): `ruby \"3.4.7\"`, `ruby \"~> 3.4.0\"`, `ruby(\">= 3.2\", \"< 5\")`, with `engine: \"ruby\"`; a gemspec's `required_ruby_version` (skipped)", Versioning: versionings, Datasources: []string{RubyDatasource}},
		{Name: DepTypeRuntime, What: "gemspec `<spec>.add_dependency` and `add_runtime_dependency` calls: `\"name\"` and the requirements as for `gem` (updates use commit type `fix`)", Datasource: "`rubygems` (rubygems.org)", Versioning: versionings, Datasources: []string{Datasource}},
		{Name: DepTypeDevelopment, What: "gemspec `<spec>.add_development_dependency` calls", Datasource: "`rubygems` (rubygems.org)", Versioning: versionings, Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipGit, When: "`git:`, `github:`, `gitlab:`, `bitbucket:`, `gist:` or a `git_source(:name)` option, or a gem in a `git`/`github` block"},
		{Reason: skipPath, When: "`path:`, or a gem in a `path` block"},
		{Reason: skipNonLiteralSource, When: "`source:` or an enclosing `source … do` block that is not a literal string (`source: ENV[\"GEMS\"]`)"},
		{Reason: skipNonLiteral, When: "a requirement that is not a literal string: a variable, a method call, an interpolated or escaped string"},
		{Reason: skipUnsupported, When: "a requirement outside Gem::Requirement's grammar (`latest`, `~> v1`, `=> 1.0`)"},
		{Reason: skipNoLock, When: "a gem without requirements, or with only upper bounds and exclusions (`< 2`, `!= 1.3`), that the Gemfile.lock next to it does not lock (or that has no Gemfile.lock)"},
		{Reason: skipRubyFile, When: "`ruby file: \".ruby-version\"` (or `.tool-versions`, `mise.toml`): the file's own dep (ruby-version, asdf, mise) is updated, and Gemfile.lock's RUBY VERSION with it"},
		{Reason: skipRubyEngine, When: "a `ruby` directive with another `engine:` (`jruby`, `truffleruby`)"},
		{Reason: skipRubyPatchlevel, When: "a `ruby` directive with `patchlevel:`, which the new release would not have"},
		{Reason: skipRubyNoFloor, When: "a `ruby` directive without a version, or with only upper bounds (`< 4`)"},
		{Reason: skipRequiredRuby, When: "a gemspec's `required_ruby_version`: the Ruby versions a gem supports are its authors' decision, not an update"},
	},
	Sections: []refdoc.Section{
		{Title: "Gemfile", Items: []string{
			"Statements are `gem`, `source`, `group`, `git_source` and blocks (`… do … end`, `if`/`unless`/`case`/`begin`/`def` … `end`). Gems inside `if`, `platforms`, `install_if` and `env` blocks are extracted as any other.",
			"`source \"<url>\"` statements outside blocks are the global sources of every gem, looked up in order (the first that has the gem answers; none = rubygems.org). A `source \"<url>\" do … end` block or a `source: \"<url>\"` option is the only source of its gems. The legacy `source :rubygems` is rubygems.org.",
			"Gem names are case-sensitive, as on the gem server.",
		}},
		{Title: "Requirements", Items: []string{
			"Gem::Requirement's grammar: `=`, `!=`, `>`, `<`, `>=`, `<=` and the pessimistic `~>` (`~> 7.1` is `>= 7.1, < 8`; `~> 7.1.2` is `>= 7.1.2, < 7.2`), none meaning `=`, on a Ruby version (`2.241.2`, `7.0.8.4`, `1.0.0.rc1`, `1.0.0-beta` = `1.0.0.pre.beta`). Several requirements must all hold; a prerelease only satisfies requirements naming one, as Bundler resolves.",
			"Requirements are rewritten per [rangeStrategy](configuration.md#rangestrategy) (top level or per rule): a single one (`2.241.2`, `= 1.2`, `~> 7.1`, `>= 1.2`, `< 2`) as well as a list (`\"~> 7.1\", \">= 7.1.2\"`, `\">= 1.2\", \"< 2\"`, or an array). An exact version takes the new version as published (`2.241.2` → `2.242.0`, `7.0.8` → `7.0.8.4`); `~>`, `>=` and `>` keep their precision when the version leaves them (`~> 7.1` → `~> 8.0` and `~> 7.1.2` → `~> 8.0.1` for 8.0.1; Renovate writes `~> 8.0.0` for the latter; `\">= 1.2\", \"< 2\"` → `\">= 2.1\", \"< 3\"` for 2.1.0).",
			"`auto` is `update-lockfile` for a Gemfile, as in Renovate: versions the requirements admit are lockfile-only updates from the version Gemfile.lock locks (`, lockfile` in the report; the Gemfile is unchanged), others replace the requirements; without a Gemfile.lock only versions outside them are proposed. For a gemspec `auto` is `replace`: a gemspec states which versions a gem supports, so only versions outside its requirements are proposed.",
			"`bump` raises the floors to every newer version (`\"~> 7.1\"` → `\"~> 7.2\", \">= 7.2.3\"` for 7.2.3, `>= 1.2` → `>= 1.4.5`), `in-range-only` only to versions the requirements admit; `widen` moves only the upper bounds (`\"~> 7.1\"` → `\">= 7.1\", \"< 9\"` for 8.0.1, `\">= 1.2\", \"< 2\"` → `\">= 1.2\", \"< 3\"`), since Ruby requirements have no `||` alternatives; `pin` writes `= <version>`, joining a list into one string (not when a comment separates its strings: such a list is not pinned). The exact rules: [rangeStrategy](configuration.md#rangestrategy).",
			"A gem without requirements (`gem \"rake\"`, `>= 0`) is never rewritten: it only gets lockfile-only updates (`update-lockfile`, `bump`, `in-range-only`), none under `replace`, `widen` and `pin`.",
			"Versioning `ruby` orders versions as Gem::Version does: numeric segments compare numerically, a letter segment marks a prerelease below its release (`1.0.0.rc1` < `1.0.0`), trailing zeros do not count (`1.2` = `1.2.0`). Update types follow the first three segments (a fourth, as in `7.0.8.4`, is a patch). Prereleases are only proposed from a prerelease.",
		}},
		{Title: "gemspec and gems.rb", Items: []string{
			"In a `*.gemspec`, every `<spec>.add_dependency`, `add_runtime_dependency` and `add_development_dependency` call with a literal name is a dep, at any depth of the `Gem::Specification.new do |spec| … end` block; its requirements are read and rewritten as a Gemfile gem's (under `auto` as `replace`). Names that are not literal strings (`%q<rails>.freeze` in generated gemspecs) are not extracted. Gems are looked up on rubygems.org.",
			"A gemspec's lock is the one of the Gemfile (or gems.rb) in its directory, which pulls it in with `gemspec`: locked versions come from there, and its updates run Bundler there together with the Gemfile's. A gemspec in another directory than its Gemfile is only patched.",
			"`gems.rb` is read as a Gemfile and locked in `gems.locked`; where both exist, Bundler and deps use gems.rb.",
		}},
		{Title: "Ruby version", Items: []string{
			"The `ruby` directive is the dep `ruby` of the `ruby-version` datasource (the ruby-lang.org release list). Its requirement is read and rewritten like a gem's (`ruby \"3.4.7\"` → `ruby \"4.0.9\"`, `~> 3.4.0` → `~> 4.0.9`); a Gemfile.lock never locks it, so versions the requirement admits are not proposed under `update-lockfile`.",
			"Every dep of the `ruby-version` datasource (this directive, [`.ruby-version`](#ruby-version), the [asdf](#asdf) and [mise](#mise) `ruby` tool) is in the built-in group `ruby` (see [monorepos](configuration.md#builtinsmonorepos)), so one PR moves all files naming the Ruby version. A rule setting `group` overrides it; `ruby` Docker images (`FROM ruby:3.4-slim`, CI `image: ruby:3.4`) stay [docker](#docker) deps, and a rule such as `{managers: [docker], names: [ruby], group: ruby}` adds them to that PR.",
			"Gemfile.lock's `RUBY VERSION` moves with it in process (Bundler is not needed for a Ruby-only update): an update of the directive rewrites the version of the lock next to the Gemfile, an update of `.ruby-version`, `.tool-versions` or a mise config does so for the lock whose Gemfile reads it with `ruby file:` in the same directory. The version is written as current Bundlers record it (`ruby 4.0.9`; a patchlevel such as `p108` is dropped, a preview's `-` becomes `.`); a lock without `RUBY VERSION` and a partial version (`4.0`) leave the lock alone.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run Bundler, and prints `note    Gemfile.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `Gemfile.lock` (`gems.locked`) next to a scanned `Gemfile` (`gems.rb`) or gemspec is refreshed, once, with `bundle lock --update` in the temporary copy, which moves every locked gem, transitive ones included, to the newest version the requirements admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `Gemfile.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a `Gemfile` is the `Gemfile.lock` next to it (of a `gems.rb` the `gems.locked`, of a gemspec the one of the Gemfile in its directory); without one the file is only patched.\n" +
	"2. **Locked versions.** Before resolution each Gemfile.lock is read: a gem gets the version of its entry in a `GEM` section (platform suffixes such as `-x86_64-linux-gnu` dropped); `GIT` and `PATH` gems are skipped anyway. An unreadable lock is reported once and its gems are bumped.\n" +
	"3. **Temporary copy.** The Gemfile's directory is mirrored to a temp dir: the Gemfile, patched gemspecs and the lockfile are copied, every other entry (`.bundle`, `*.gemspec`, `eval_gemfile` files) is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so `path:` gems outside resolve as in the checkout.\n" +
	"4. **Update.** Every updated gem that the lock has is pinned for one run: its requirement is written as `= <new version>` into the copied Gemfile and gemspecs (a gem without requirements gets one), and `bundle lock --update <gems…> --conservative` moves exactly those gems, and their dependencies only where the new versions need it. " +
	"When a requirement was pinned, the patched Gemfile is put back and `bundle lock` runs again: Bundler keeps the locked versions and writes the Gemfile's requirements into the lock's `DEPENDENCIES`. So the lock holds the version the pull request names, also when `minimumReleaseAge` held back a newer one the rewritten requirement admits. " +
	"Lockfile-only updates are pinned the same way, with the Gemfile unchanged.\n" +
	"5. **Tool.** `bundle` must be on `PATH`; it runs with `BUNDLE_GEMFILE` set to the copy and the repository's `.bundle/config` in effect. Bundler records its own version under `BUNDLED WITH` (and in `CHECKSUMS`): provide the version the lock names to keep those lines unchanged. Without `bundle`, the Gemfiles fail with `bundle not found (needed to update Gemfile.lock)`.\n" +
	"6. **Failure.** A failed command reports `bundle lock in <dir>: <last 20 output lines>`, and the Gemfile's changes are dropped."
