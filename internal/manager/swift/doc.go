package swift

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the swift manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the package requirements of Swift Package Manager manifests and moves their pins in `Package.resolved` in process (no Swift toolchain needed). " +
		"Only the version strings of a requirement are rewritten; its form, comments and formatting are kept.",
	Files: []string{"`Package.swift` at any depth (version-specific manifests `Package@swift-<v>.swift` are not read)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "every `.package(url: …, <requirement>)` call (also with `name:` and `traits:`, and `Package.Dependency.package(…)`); the name is the URL path (`owner/repo`), the source its http(s) URL (https for `ssh://`, `git://` and `git@host:` URLs) without `.git`", Versioning: "`semver`; tags are read as SwiftPM reads them: full `MAJOR.MINOR.PATCH[-pre][+build]` versions with an optional leading `v`, which is dropped (the dep's `extractVersion`)", Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipLocal, When: "`.package(path: …)`"},
		{Reason: skipRegistry, When: "`.package(id: …)` (a package registry identity)"},
		{Reason: skipURL, When: "`url` is not a string literal with an https/http/ssh/git or `git@host:path` URL with an owner/repo path (`file://`, a variable)"},
		{Reason: skipBranch, When: "`branch: …` / `.branch(…)`"},
		{Reason: skipRevision, When: "`revision: …` / `.revision(…)`"},
		{Reason: skipRequirement, When: "a requirement outside the forms below: a variable, `Version(1, 2, 3)`, an interpolated string, a partial version (`1.2`), a missing requirement"},
	},
	Sections: []refdoc.Section{
		{Title: "Requirements", Items: []string{
			"`exact: \"1.2.3\"` and `.exact(\"1.2.3\")` are plain versions, replaced by the new one.",
			"Ranges: `from: \"1.2.3\"` and `.upToNextMajor(from: \"1.2.3\")` (`>=1.2.3 <2.0.0`, also for 0.x versions, as SwiftPM), `.upToNextMinor(from: \"1.2.3\")` (`>=1.2.3 <1.3.0`), `\"1.2.3\"..<\"2.0.0\"` and `\"1.2.3\"...\"1.9.0\"`. The version a bump raises is the lower bound.",
			"`rangeStrategy` (default `auto` = `update-lockfile`): versions the range admits are lockfile-only updates from the pinned version (`, lockfile` in the report; `Package.swift` is unchanged), newer ones rewrite the requirement, so a `from:` moves with new major versions. Without a `Package.resolved` only versions outside the range are proposed. `bump` (Renovate's `auto` for Swift) rewrites the lower bound for every newer version. " +
				"A rewrite sets the lower bound to the new version; an upper bound the new version reaches moves past it, `...` to the new version and `..<` to the next boundary of its precision (`\"1.2.3\"..<\"2.0.0\"` → `\"3.1.0\"..<\"4.0.0\"` for 3.1.0). `widen` keeps the lower bound of `..<` and `...` ranges; `from:` and `.upToNext…` cannot be widened without changing their form, so they take the new version as under `replace`. " +
				"`pin` makes the requirement exact: a pin update to the pinned version (else the newest stable version the range admits), then each newer version. Only the requirement's tokens change. From swift-tools-version 5.6 on every range becomes `exact: \"1.4.0\"` (`from: \"1.2.3\"` changes only its label and version string); `.exact(…)` is deprecated there and does not combine with `traits:`. Before 5.6 (no `exact:` label), without a tools version and in calls with `name:` (there is no `package(name:url:exact:)`) the range becomes `.exact(\"1.4.0\")`.",
		}},
		{Title: "Package.resolved", Body: resolvedBody},
	},
}

const resolvedBody = "1. **Root packages.** A `Package.swift` with a `Package.resolved` next to it is a root package. Its local graph is its own directory plus every package it reaches through `.package(path:)` dependencies (followed transitively, within `--dir`); the root's `Package.resolved` pins the remote dependencies of all of them. A manifest belongs to every root whose graph holds its directory, so updating a dependency of a local library package (`../shared/Kit`) also moves the pin in the app's `Package.resolved`.\n" +
	"2. **Locked versions.** The pinned version of a range requirement is the lowest pin it admits among its roots' `Package.resolved` files (formats v1, v2 and v3). Pins are matched by repository URL (case-insensitive, without scheme, user and `.git`), not by identity, so a fork with the same last path component is not mistaken for the package. An unreadable `Package.resolved` is reported once and its requirements are bumped.\n" +
	"3. **Pins.** With an update (also in `plan`, which prints the `Package.resolved` diff) the pin of the package in each root's `Package.resolved` gets the new version and the commit of its tag, from the tags the `git-tags` datasource already listed: no Swift toolchain, no clone. A pin the new requirement admits that is at or above the new version is kept, so pins only move down to the exact version of a `pin` rewrite. Only the version, revision and origin hash strings are replaced; the file keeps its formatting. Pins of other packages stay: when the new version needs other versions of its own dependencies, SwiftPM notices on the next `swift package resolve` or build that the pins no longer satisfy the graph and re-resolves them, as it does for a manually edited manifest.\n" +
	"4. **Origin hash.** SwiftPM 5.10+ (`Package.resolved` v3) stores `originHash`, the SHA-256 of the root manifest. When the root's own `Package.swift` changes and the stored hash matched it before, the hash of the new manifest is written, so `-only-use-versions-from-resolved-file` / `-disableAutomaticPackageResolution` builds accept the file. A hash that did not match (a version-specific manifest, an already stale file) is left alone.\n" +
	"5. **Failure.** A commit that cannot be looked up fails the update, and the other Swift updates built with it, with `<dir>/Package.resolved: commit of <name> <version>: <error>`. Lock file maintenance is not supported."
