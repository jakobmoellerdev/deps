package bundler

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// CocoaPodsDoc is the reference documentation of the cocoapods manager
// (docs/reference/managers.md).
var CocoaPodsDoc = refdoc.Manager{
	Name: NameCocoaPods,
	Summary: "Updates the pod requirements and git tags of CocoaPods `Podfile`s and moves `Podfile.lock` with `pod install`, as Renovate does. " +
		"Podfiles are read with the [bundler](#bundler) manager's Ruby lexer, so Ruby around the `pod` statements (methods, conditionals, `post_install` hooks, `require`s) is skipped; only the requirement strings that change are rewritten, and of those only the version where the operator stays.",
	Files: []string{"`Podfile` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypePods, What: "`pod \"Name\", \"~> 5.8\"` statements at any depth (`target`, `abstract_target`, `def` blocks), with any number of requirement strings (`\"> 0.1\", \"< 0.3\"`), parentheses, options (`:configurations`, `:modular_headers`, …) in `:key => value` or `key:` syntax; subspecs `Name/Sub` are named as written and looked up as their pod; " +
			"git pods `pod \"Name\", :git => \"<url>\", :tag => \"<tag>\"` (the tag is the version; any git host, the source is the repository's https URL)",
			Datasource: "`pod` (the Podfile's `source`s, else trunk; a pod's `:source`); `git-tags` for git pods with a tag", Versioning: "`ruby` (CocoaPods versions and requirements are Gem::Version and Gem::Requirement); `tag` for git pods", Datasources: []string{PodDatasource, GitDatasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipUnspecified, When: "a pod without a requirement (`pod \"Alamofire\"`), as Renovate"},
		{Reason: skipGit, When: "a git pod with `:branch` or `:commit`, without `:tag`, or with a `:git`/`:tag` that is not a literal string"},
		{Reason: skipGitURL, When: "a git pod whose `:git` is no https/http/ssh/git or `git@host:path` URL"},
		{Reason: skipPath, When: "`:path`"},
		{Reason: skipPodspec, When: "`:podspec`"},
		{Reason: skipExternal, When: "`:http`, `:hg` or `:svn`"},
		{Reason: skipNonLiteralSource, When: "a `:source` that is not a literal string"},
		{Reason: skipNonLiteral, When: "a requirement that is not a literal string: a variable, an interpolated or escaped string, the deprecated `:head`"},
		{Reason: skipUnsupported, When: "a requirement outside Gem::Requirement's grammar"},
		{Reason: skipPodNoLock, When: "a pod with only upper bounds and exclusions (`< 2`, `!= 1.3`) that the Podfile.lock next to it does not lock (or without a Podfile.lock)"},
	},
	Sections: []refdoc.Section{
		{Title: "Spec repositories", Items: []string{
			"`source \"<url>\"` statements are the spec repositories of every pod, in order (CocoaPods resolves a pod from the first that has it); without any, pods come from trunk. A pod's `:source` is its only spec repository. Trailing slashes are dropped.",
			"The `pod` datasource reads trunk and http(s) spec repositories in the CDN layout, and spec repositories on github.com through the GitHub API; other git spec repositories fail the pod.",
		}},
		{Title: "Requirements", Items: []string{
			"Requirements are read and rewritten as a [bundler](#bundler) gem's: `=`, `!=`, `>`, `<`, `>=`, `<=` and the optimistic `~>`, none meaning `=`, every [rangeStrategy](configuration.md#rangestrategy) (`~> 5.8` → `~> 6.0` for 6.0.1, `5.8.1` → `6.0.1`).",
			"`auto` is `replace`, as Renovate resolves it for CocoaPods (no lockfile-only updates): versions the requirements admit are left to `pod update`, others replace the requirements. With `update-lockfile`, `bump` or `in-range-only` admitted versions are lockfile-only updates from the version Podfile.lock locks (`, lockfile` in the report).",
			"Subspecs of one pod (`Firebase/Analytics`, `Firebase/Crashlytics`) are separate deps; CocoaPods resolves them to one version, so group them (`{managers: [cocoapods], names: [\"Firebase/**\"], group: firebase}`) when their requirements must move together.",
		}},
		{Title: "Podfile.lock (`apply`, `run`)", Body: podLockBody,
			Items: []string{"`plan` does not run CocoaPods, and prints `note    Podfile.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `Podfile.lock` next to a scanned `Podfile` is refreshed with `pod update` in the temporary copy, which moves every pod to the newest version the Podfile admits. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `Podfile.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const podLockBody = "1. **Lockfile.** The lockfile of a `Podfile` is the `Podfile.lock` next to it; without one the Podfile is only patched.\n" +
	"2. **Locked versions.** Before resolution each Podfile.lock is read: a pod (and its subspecs) gets the version of its entry under `PODS`. An unreadable lock is reported once and its pods are bumped.\n" +
	"3. **Temporary copy.** The Podfile's directory is mirrored to a temp dir: the Podfile, Podfile.lock and every file of the Xcode projects and workspaces below it are copied (`pod install` integrates the pods into them), `Pods`, `build` and `.git` are left out, every other entry is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so `:path` pods and the scripts a React Native Podfile requires (`node_modules`) resolve as in the checkout.\n" +
	"4. **Update.** `pod install` runs there (Renovate's command): CocoaPods resolves the pods whose requirements changed anew, to the newest version they admit, and keeps every other pod locked. Lockfile-only updates run `pod update <pods…>` instead, which unlocks those pods. Only `Podfile.lock` is taken back; a checked-in `Pods` directory is not updated.\n" +
	"5. **Tool.** `pod` must be on `PATH` (CocoaPods runs on Linux, it needs `git` and `rsync`), with the plugins the Podfile names installed; deps adds `--allow-root` when it runs as root. CocoaPods records its own version under `COCOAPODS:`: provide the version the lock names to keep that line unchanged. Without `pod`, the Podfiles fail with `pod not found (needed to update Podfile.lock)`.\n" +
	"6. **Failure.** A failed command reports `pod install in <dir>: <last 20 output lines>`, and the Podfile's changes are dropped."
