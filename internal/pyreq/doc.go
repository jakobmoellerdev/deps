package pyreq

import (
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// SpecifierSkips documents the skip reasons of PEP 440 specifiers (SetVersion).
var SpecifierSkips = []refdoc.Skip{
	{Reason: SkipUnconstrained, When: "no specifier, `*`, or only `!=` exclusions"},
	{Reason: SkipRange, When: "a specifier that does not parse: a version that is not PEP 440 (`>=1.0-SNAPSHOT`, `===foobar`), a wildcard other than a trailing `.*` of `==` or `!=` (`~=1.*`, `>=1.*`), Poetry syntax (`^1.2`, `>=1 <2`, `||`), or a bare version outside Pipfiles"},
	{Reason: SkipLocate, When: "a TOML string with escapes"},
}

// PoetrySkips documents the skip reasons of Poetry constraints (SetVersion).
var PoetrySkips = []refdoc.Skip{
	{Reason: SkipUnconstrained, When: "`*`, empty, only `!=` exclusions, or a table without `version`"},
	{Reason: SkipRange, When: "a constraint that does not parse: a version that is not PEP 440, a wildcard other than a trailing `.*` of `==`, `!=` or a bare version (`^1.*`), non-string values"},
	{Reason: SkipLocate, When: "a TOML string with escapes"},
}

// anchorItem explains dep.Dep.Version of a range.
const anchorItem = "The dep's version is the range's anchor: in its last `||` alternative the first clause that is not `<`, `<=` or `!=` (`>=1.2,<2` → `1.2`, `<1.0.0,>=0.114.2` → `0.114.2`), else its first upper bound (`<2` → `2`). The anchor's precision is kept: candidates with non-zero parts beyond it are skipped (`precision`), others are cut to it (`>=1.2,<2` sees 3.1.0 as `3.1`). With a lockfile, the locked version takes the anchor's place."

// SpecifierSection documents which PEP 440 specifiers are accepted.
var SpecifierSection = refdoc.Section{Title: "Specifiers", Items: []string{
	"Every PEP 440 specifier set is accepted: `==`, `===`, `~=`, `!=`, `<`, `<=`, `>` and `>=` clauses separated by commas, with a trailing `.*` after `==` or `!=` (in a Pipfile also a bare version, meaning `==`). How a range is updated is described under Ranges.",
	anchorItem,
	"Versioning is `pep440`: pre and dev releases are only considered from a pre or dev release.",
	"Names are kept as written; the datasource looks them up PEP 503-normalized.",
}}

// PoetrySection documents which Poetry constraints are accepted.
var PoetrySection = refdoc.Section{Title: "Constraints", Items: []string{
	"Every Poetry constraint is accepted: the PEP 440 clauses plus `^`, `~`, `=` and a bare version (an exact pin; `2.*` a wildcard), separated by commas or spaces (`>=1.2 <2`), with `||` (or `|`) alternatives (`^1 || ^2`). How a constraint is updated is described under Ranges.",
	anchorItem,
	"Versioning is `pep440`: pre and dev releases are only considered from a pre or dev release.",
}}

// RangeSection documents rangeStrategy for Python specifiers (NewRange, Edits).
var RangeSection = refdoc.Section{Title: "Ranges (`rangeStrategy`)", Items: []string{
	"`auto` (the default) is `update-lockfile`: a new version the range admits only moves the lockfile from the locked version (see Locked versions), and is not proposed without a lockfile deps reads; a version outside the range rewrites the range. Renovate's `auto` is `update-lockfile` for poetry and `replace` for pep621, pipenv and pip_requirements, which only differs where deps reads a lockfile.",
	"`bump` rewrites the range for every newer version, raising its lower bound (`>=1.2,<2` with 1.5.0 → `>=1.5.0,<2`, `^2.31` with 2.32.3 → `^2.32`, `>=2.31.0` → `>=2.32.3`). An admitted version a range has no bound to raise for (`<2`, `<=2`, `>1`, `==1.*`) is handled as under `update-lockfile`.",
	"Rewrites follow Renovate's pep440 `getNewValue` (`lib/modules/versioning/pep440/range.ts`): an admitted version gets its `bump` rules, any other its `replace` rules, under every strategy (Renovate's `bump` would write the full new version as lower bound there too). Clause by clause: `!=` exclusions are kept verbatim; `<V` moves past the new version at the range's precision (`>=1.2,<2` with 3.1.0 → `>=3.1,<4`; with two clauses the precision is the first part in which the bounds differ, one finer for a major followed by `0`: `>=1.2.0,<2.0.0` with 3.1.4 → `>=3.1.4,<3.2.0`); `>=` and `>` take the new version at their own precision (`>=1.2` → `>=3.1`, a new version at or below them `>=<new>`); trailing zeros are trimmed when a clause has fewer than three parts; `~=`, `^` and `~` take the new version cut or zero-padded to their precision (`~=7.2` with 8.0.1 → `~=8.0`); `==`, `===`, `=`, a bare version and `<=` become the new version; wildcards keep `.*` at their precision (`==1.0.*` → `==1.2.*`).",
	"A Poetry constraint with `||` alternatives gets a new alternative for a version outside all of them (`^1 || ^2` with 3.0.0 → `^1 || ^2 || ^3`, as Renovate widens npm `||` ranges); `bump` raises the alternative that admits the version.",
	"Only the changed versions (and operators, `>` → `>=`) are rewritten; the bytes between clauses are kept. A rewrite that would not admit the new version (it is excluded by `!=`) is not proposed. Renovate's removal of a `>=` that a `==X.*` makes redundant is not done.",
}}

// LockedSection documents the locked versions of the pep621, poetry and pipenv managers
// (pytool.ReadLocks).
var LockedSection = refdoc.Section{Title: "Locked versions", Body: "Before resolving, deps reads the lockfile the post-processor would update (see below) once per run: `[[package]]` `name` and `version` of `uv.lock`, `poetry.lock` and `pdm.lock`, and the `\"==<version>\"` pins of `Pipfile.lock` in the section of the dep's Pipfile table (`default` for `[packages]`, `develop` for `[dev-packages]`, else the category's name). Names compare PEP 503-normalized; a name locked at several versions (uv and pdm forks by marker) counts as not locked.",
	Items: []string{
		"The locked version is the current version (update types compare it with the new version), decides which versions the range admits are lockfile-only updates, and is what OSV is queried with, so an already locked fix is not proposed again. Without a lock, the range's anchor is used.",
		"A lockfile that does not parse is a run error (`<path>: <err>`); its deps count as not locked.",
	}}

// LockSection documents the lockfile post-processing of the pep621, poetry and pipenv managers
// (pytool.Locker); scope is the sentence naming the manager's lock directory.
func LockSection(scope string) refdoc.Section {
	return refdoc.Section{Title: "Lockfile post-processing (`apply`, `run`)", Body: scope + " Its lockfile is regenerated in a temporary copy of that directory's top-level files (up to 4 MiB each; subdirectories are not copied) with the patched manifests and the current lockfile:\n\n" +
		"| Lockfile | Command | Without the tool in `PATH` |\n" +
		"|---|---|---|\n" +
		"| `uv.lock` | `uv lock --no-progress` | error `uv not found (needed to update uv.lock)` |\n" +
		"| `poetry.lock` | `poetry lock` (Poetry 2), `poetry lock --no-update` (Poetry 1), with `POETRY_NO_INTERACTION=1` | `uvx --from poetry poetry lock` |\n" +
		"| `pdm.lock` | `pdm lock --update-reuse` (keeps the pins the manifest still admits), with `PDM_CHECK_UPDATE=false PDM_NON_INTERACTIVE=1` | `uvx --from pdm pdm lock --update-reuse` |\n" +
		"| `Pipfile.lock` | `pipenv lock`, with `PIPENV_VENV_IN_PROJECT=1 PIPENV_IGNORE_VIRTUALENVS=1 PIPENV_NOSPIN=1 PIPENV_YES=1 PIPENV_VERBOSITY=-1` | `uvx --from pipenv pipenv lock` |\n\n" +
		"A `pyproject.toml` without a lockfile next to it belongs to the nearest `uv.lock` above whose `[tool.uv.workspace]` `members` (minus `exclude`) match its directory; every member's `pyproject.toml` is copied as well. " +
		"Projects whose metadata is built (dynamic versions from VCS, local build backends) can fail to lock in the copy. " +
		"A failure reports `<command> in <dir>: <last 20 output lines>`, and the directory's changes are dropped.\n\n" +
		"Lockfile-only updates (`rangeStrategy` `update-lockfile`, see Ranges) run after that regeneration, which is skipped when no manifest of the directory changed, with the same tool, environment and fallback:\n\n" +
		"| Lockfile | Lockfile-only command |\n" +
		"|---|---|\n" +
		"| `uv.lock` | `uv lock --no-progress --upgrade-package <name>==<version>…` |\n" +
		"| `poetry.lock` | `poetry update --lock <name>…` (Poetry 1 and 2) |\n" +
		"| `pdm.lock` | `pdm update --update-reuse --no-sync -G <group> <name>…`, once per lock group of the names (pdm takes one group with package names) |\n" +
		"| `Pipfile.lock` | `pipenv upgrade --lock-only --categories <category> <name>==<version>…`, once per Pipfile category |\n\n" +
		"Poetry and pdm cannot pin a version there: they move the dep to the newest version its range admits, which can be newer than proposed.",
		Items: []string{"`plan` does not run lock commands, and prints `note    uv.lock, poetry.lock, pdm.lock and Pipfile.lock changes are computed by apply/run`."}}
}

// maintainCmds are the lockFileMaintenance commands of pytool.Locker.Maintain by lockfile.
var maintainCmds = map[string]string{
	"uv.lock":      "`uv lock --no-progress --upgrade`",
	"poetry.lock":  "`poetry update --lock` (Poetry 1 and 2)",
	"pdm.lock":     "`pdm update --no-sync --update-all` (the locked groups are kept)",
	"Pipfile.lock": "`pipenv lock` (pipenv resolves every package anew)",
}

// MaintenanceSection documents lockFileMaintenance of the pep621, poetry and pipenv managers
// (pytool.Locker.Maintain) for the manager's lockfiles; scope is the sentence naming which
// lockfiles the manager maintains.
func MaintenanceSection(scope string, lockfiles ...string) refdoc.Section {
	var b strings.Builder
	b.WriteString("With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, " + scope +
		" Each is refreshed in the temporary copy described above, with the same environment and `uvx` fallback, which moves every locked package (transitive ones too) to the newest version the manifests admit; the manifests are not changed:\n\n" +
		"| Lockfile | Maintenance command |\n|---|---|\n")
	for _, l := range lockfiles {
		b.WriteString("| `" + l + "` | " + maintainCmds[l] + " |\n")
	}
	b.WriteString("\nAll lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only lockfiles; `minimumReleaseAge` does not apply to it.")
	return refdoc.Section{Title: "Lock file maintenance (`run`)", Body: b.String()}
}
