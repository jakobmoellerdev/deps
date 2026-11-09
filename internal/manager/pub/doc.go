package pub

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const (
	pkgDatasources = "`dart` (pub.dev, or the `hosted` repository), `git-tags` for a `git` package pinned to a tag"
	pkgVersionings = "`pub`; `tag` for git packages"
)

// Doc is the reference documentation of the pub manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the version constraints of Dart and Flutter packages and the Dart and Flutter SDK constraints in `pubspec.yaml`, and moves `pubspec.lock` with `dart pub upgrade`. " +
		"pubspec.yaml is read as YAML; only the constraint strings that change are rewritten, so formatting, comments and key order are kept.",
	Files: []string{"`pubspec.yaml`, `pubspec.yml` (its lock: the `pubspec.lock` next to it, or the workspace root's for a `resolution: workspace` member)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependencies, What: "entries of `dependencies`: `name: <constraint>`, `name: {version: <constraint>, hosted: <url>}` (also the `hosted: {name, url}` form), `name: {git: {url, ref}}` (updates use commit type `fix`)", Datasource: pkgDatasources, Versioning: pkgVersionings, Datasources: []string{"dart", "git-tags"}},
		{Name: DepTypeDevDependencies, What: "entries of `dev_dependencies`, in the same forms", Datasource: pkgDatasources, Versioning: pkgVersionings, Datasources: []string{"dart", "git-tags"}},
		{Name: DepTypeEnvironment, What: "`environment.sdk` (name `dart`) and `environment.flutter` (name `flutter`)", Datasource: "`dart-version` for `dart`, `flutter-version` for `flutter`", Versioning: "`pub`", Datasources: []string{"dart-version", "flutter-version"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipSDK, When: "a package of an SDK: `flutter: {sdk: flutter}`, `flutter_test`, `flutter_localizations`, …"},
		{Reason: skipPath, When: "a `path` package"},
		{Reason: skipGitNoRef, When: "a `git` package without `ref` (it tracks the default branch)"},
		{Reason: skipGitCommit, When: "a `git` package whose `ref` is a full commit hash"},
		{Reason: skipGitURL, When: "a `git` URL that is not an http(s), ssh or scp-style git URL"},
		{Reason: skipUnversioned, When: "no constraint: `name:`, `name: any`, or a detailed dependency without `version`"},
		{Reason: skipUnsupported, When: "a constraint outside pub's grammar (`~1.2.3`, `^1.2`, `1.x`)"},
		{Reason: skipEscaped, When: "a constraint written with YAML escapes or as a block scalar"},
		{Reason: skipFlutterPin, When: "`meta` in a Flutter package: the Flutter SDK pins it exactly, as Renovate does"},
		{Reason: skipNoLock, When: "a package constraint without a lower bound (`<2.0.0`) that its pubspec.lock does not lock (or without pubspec.lock)"},
		{Reason: skipNoFloor, When: "an SDK constraint without a lower bound (`<4.0.0`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Constraints", Items: []string{
			"pub's constraint grammar (pub_semver): `^1.2.3` (`>=1.2.3 <2.0.0`; `^0.4.1` is `>=0.4.1 <0.5.0` and `^0.0.3` is `>=0.0.3 <0.1.0`), comparisons `>=`, `>`, `<=`, `<` combined with blanks or written back to back (`>=1.2.3 <2.0.0`, `>=1.2.3<2.0.0`), and exact versions (`1.2.3`). As in pub, an exclusive upper bound that is a release leaves out its prereleases (`<2.0.0` excludes `2.0.0-dev.1`).",
			"Versioning `pub` orders strict semantic versions as pub does: build metadata sorts after its release (`1.2.3+1` > `1.2.3`, pub's hotfix builds). Update types follow pub's caret: a new major, or a new minor of a 0.x version, is `major`; versions below stable are only proposed from one.",
			"Constraints are rewritten per [rangeStrategy](configuration.md#rangestrategy) (top level or per rule): an exact version takes the new version; a version the constraint admits raises its lower bound (`^1.2.3` → `^1.4.0`, `>=1.2.3 <2.0.0` → `>=1.4.0 <2.0.0`; `>` becomes `>=`); `replace` replaces it (`^1.2.3` → `^2.1.0`, `>=1.0.0 <2.0.0` → `>=2.1.0 <3.0.0`), `widen` keeps the lower bound and moves the upper ones (`^1.2.3` → `>=1.2.3 <3.0.0` for 2.1.0, `<=2.0.0` → `<=2.1.0`), `pin` writes the exact version. An exclusive upper bound moves to the first version `^<new>` excludes. A plain YAML value that would not stay one (`>=…`) is written single-quoted.",
			"`auto` is `replace`, as in Renovate (its pub manager has no lockfile-only updates): versions the constraint admits are not proposed, so `^1.2.3` gets an update for 2.0.0, not 1.4.0. `update-lockfile` proposes them as lockfile-only updates from the version pubspec.lock locks (`, lockfile` in the report; pubspec.yaml is unchanged), `bump` and `in-range-only` raise the lower bound.",
			"The SDK constraints `environment.sdk` and `environment.flutter` are deps `dart` and `flutter` of the same grammar, rewritten the same way; nothing locks them, so `^3.5.0` only changes for Dart 4 under `replace`.",
			"`git` packages with a `ref` that is a tag are `git-tags` deps of the `url` (any `path` inside the repository is kept); the ref is replaced by the new tag. Other refs (branches) are reported as non-versions.",
			"`dependency_overrides` are not read, as in Renovate (its pub manager extracts only `dependencies` and `dev_dependencies`): an override is a local pin or fork that the package's constraints should not move. A package an override replaces still gets updates of its `dependencies` or `dev_dependencies` entry, but pub resolves it to the override, so pubspec.lock keeps the overridden version.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run pub, and prints `note    pubspec.lock changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `pubspec.lock` of a scanned package or workspace root is refreshed with `dart pub upgrade` (same tool and options as below) in the temporary copy, which moves every locked package, transitive ones included, to the newest version the constraints admit. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `pubspec.lock` files; `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a pubspec is the `pubspec.lock` next to it; for a workspace member (`resolution: workspace`), the one of the nearest ancestor whose pubspec.yaml lists a `workspace`. Without one the file is only patched.\n" +
	"2. **Locked versions.** Before resolution each pubspec.lock is read: a hosted package gets the version of its `packages` entry. An unreadable lock is reported once and its packages are bumped.\n" +
	"3. **Temporary copy.** The package (or workspace root) directory is mirrored to a temp dir: the pubspecs of the root and every workspace member and pubspec.lock are copied, the directories leading to them recreated, and every other entry but `.dart_tool` and `build` symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so path dependencies outside resolve as in the checkout.\n" +
	"4. **Update.** In the copy, the constraint of every updated hosted package, lockfile-only updates included, is set to exactly the update's version, and `dart pub upgrade <name>… --no-precompile --no-example` moves those packages (and git packages with a new tag), and their dependencies only where the new versions need it; the exact constraint keeps pub from locking a newer version the rewritten constraint admits (e.g. when `minimumReleaseAge` held it back). Updates of only the SDK constraints run `dart pub get`. Only pubspec.lock is taken back.\n" +
	"5. **Tool.** `dart` must be on `PATH` (e.g. from `dart-lang/setup-dart`). A Flutter scope (its lock has `sdks: flutter`, or a pubspec depends on `sdk: flutter` or constrains `environment.flutter`) needs `flutter` on `PATH` (e.g. `subosito/flutter-action`): deps runs the Dart SDK bundled with it with `FLUTTER_ROOT` set, which resolves the Flutter SDK packages as `flutter pub` does without regenerating platform files. The SDK must satisfy the pubspecs' SDK constraints, new ones included. `PUB_HOSTED_URL` and `PUB_CACHE` are in effect. Without the tool, the pubspecs fail with `dart not found (needed to update pubspec.lock)` or `flutter not found (…)`.\n" +
	"6. **Failure.** A failed command reports `dart pub <command> in <dir>: <last 20 output lines>`, and the pubspec's changes are dropped. This includes updates of packages the Flutter SDK pins (`material_color_utilities`, `test_api` through `test`), which `flutter pub` rejects too."
