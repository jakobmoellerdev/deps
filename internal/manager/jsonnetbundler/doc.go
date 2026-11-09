package jsonnetbundler

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the jsonnet-bundler manager
// (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the git dependencies of jsonnet-bundler (`jb`) packages in `jsonnetfile.json`: a version tag to the newest tag, a branch or commit to the newest commit, and moves `jsonnetfile.lock.json` (and a committed `vendor` directory) with `jb update <dependency>`, as Renovate's `jsonnet-bundler` manager does. " +
		"jsonnetfile.json is read as JSON; only the version strings that change are rewritten, so formatting and key order are kept.",
	Files: []string{"`jsonnetfile.json` at any depth outside `vendor` directories (its lock: the `jsonnetfile.lock.json` next to it)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeDependency, What: "the `dependencies[]` entries with a `source.git.remote`; the name is Renovate's depName `<host>/<path>[/<subdir>]` (the remote without `.git`, joined with `source.git.subdir`), the source the remote's `https://` URL, the version the `version` field: " +
			"a version tag (`v0.14.0`), a commit (40 hex digits; its version is `HEAD`, the default branch, and its digest the commit) or a branch (`main`; its digest the commit `jsonnetfile.lock.json` locks)",
			Datasource: "`git-tags` (a version tag); `git-refs` (a branch or commit)", Versioning: "`tag`", Datasources: []string{dsTags, dsRefs}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipLocal, When: "a `source.local` dependency (a directory of the repository)"},
		{Reason: skipURL, When: "a remote that is not an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: skipNoVersion, When: "an entry without `version` (jb tracks the default branch)"},
		{Reason: skipLocate, When: "a version jsonnetfile.json does not write verbatim (JSON escapes)"},
		{Reason: skipNoLock, When: "a branch dependency of a jsonnetfile.json without `jsonnetfile.lock.json` next to it"},
		{Reason: skipUnreadable, When: "a branch dependency whose `jsonnetfile.lock.json` is not JSON (also reported as an error)"},
		{Reason: skipNotLocked, When: "a branch dependency `jsonnetfile.lock.json` does not lock to a commit (run `jb install`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Updates", Items: []string{
			"A version tag gets updates to newer tags like any `git-tags` dep, with their release notes: the tag is rewritten in `jsonnetfile.json`.",
			"A branch (`master`, `main`, `release-0.14`) gets a `digest` update when the branch's commit differs from the one `jsonnetfile.lock.json` locks; `jsonnetfile.json` is unchanged and only the lock moves. Such PRs have no release notes.",
			"A commit gets `digest` updates to the newest commit of the default branch, rewritten in `jsonnetfile.json`, as deps does for pinned nix inputs. To keep such a pin, ignore the dep with a rule (`{managers: [jsonnet-bundler], names: [github.com/brancz/kubernetes-grafana/**], ignore: true}`).",
			"Only the `jsonnetfile.lock.json` next to a jsonnetfile.json is updated, as in Renovate: the jsonnetfile.json of a `source.local` package of another file (kube-prometheus' `jsonnet/kube-prometheus`) gets its version strings rewritten, and the lock of the file that includes it moves with the next update of that file or with lock file maintenance.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Body: lockBody,
			Items: []string{"`plan` does not run jb, and prints `note    jsonnetfile.lock.json changes are computed by apply/run`."}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `jsonnetfile.lock.json` next to a scanned `jsonnetfile.json` is refreshed with `jb update` in the temporary copy, which moves every dependency, transitive ones included, to the newest commit its version admits, as Renovate does. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`, which changes only `jsonnetfile.lock.json` files (and committed `vendor` directories); `minimumReleaseAge` does not apply to it."},
	},
}

const lockBody = "1. **Lockfile.** The lockfile of a `jsonnetfile.json` is the `jsonnetfile.lock.json` next to it; without one the file is only patched.\n" +
	"2. **Temporary copy.** The directory is mirrored to a temp dir: jsonnetfile.json (patched) and jsonnetfile.lock.json are copied, every other entry but `.git` and `vendor` is symlinked; the directories above it up to the repository top are recreated with their entries symlinked, so `source.local` dependencies resolve as in the checkout.\n" +
	"3. **Update.** `jb update <uri>…` re-locks every updated dependency, the uri being the remote as jsonnetfile.json writes it joined with the `subdir` (as Renovate): a branch moves to its newest commit at that time (which can be newer than the proposed digest, as in Renovate), a tag or commit jsonnetfile.json now names is locked. Other dependencies keep their lock. A patched jsonnetfile.json without such updates runs `jb install`.\n" +
	"4. **Vendor.** When the directory has a `vendor` directory (jb's default `--jsonnetpkg-home`, committed), the files jb wrote there that differ from the checkout's are added to the change and files it no longer writes are deleted, as Renovate commits every file jb changed; symlinks (`source.local` packages, `legacyImports`) are left alone. Without one only the lock is taken back.\n" +
	"5. **Tool.** `jb` must be on `PATH` (e.g. `go install github.com/jsonnet-bundler/jsonnet-bundler/cmd/jb@latest`); it downloads every dependency of the lock from its forge. Without jb, the jsonnetfile.json files fail with `jb not found (needed to update jsonnetfile.lock.json)`.\n" +
	"6. **Failure.** A failed command reports `jb update in <dir>: <last 20 output lines>`, and the jsonnetfile.json's changes are dropped."
