package bazel

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the bazel-module manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates Bazel module dependencies (bzlmod): `bazel_dep` versions from Bazel registries, git overrides and git repository rules, and the images, Maven artifacts and crates of the rules_oci, rules_img, rules_jvm_external and rules_rust module extensions, as Renovate's bazel-module manager. " +
		"Files are read with a small Starlark lexer (strings, lists, comments, nested calls and list comprehensions); only the string values that change are rewritten. `MODULE.bazel.lock` is regenerated with `bazel mod deps`.",
	Files: []string{"`MODULE.bazel` and the segments it includes (`<name>.MODULE.bazel`, Renovate's `(^|/|\\.)MODULE\\.bazel$`)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeBazelDep, What: "`bazel_dep(name = …, version = …)` (also with `repo_name`, `dev_dependency`, `max_compatibility_level`)", Versioning: "`bazel-module`", Datasources: []string{"bazel"}},
		{Name: DepTypeSingleVersionOverride, What: "`single_version_override(module_name = …, version = …)`: the pinned version is listed but not updated (Renovate: `ignored`); its `registry` becomes the registry of the module's bazel_dep", Versioning: "`bazel-module`", Datasources: []string{"bazel"}},
		{Name: DepTypeGitOverride, What: "`git_override(module_name = …, remote = …, commit = …)`: a commit tracks the default branch head (digest updates; Renovate's github-tags without a tag), a `tag` (with or without `commit`) the tags. Named after the module; any git host, not only github.com", Datasource: "`git-refs` (commit), `git-tags` (tag)", Versioning: "`tag`", Datasources: []string{"git-refs", "git-tags"}},
		{Name: DepTypeArchiveOverride, What: "`archive_override(module_name = …)`: listed and skipped", Datasource: "none", Versioning: "none", Datasources: []string{}},
		{Name: DepTypeLocalPathOverride, What: "`local_path_override(module_name = …)`: listed and skipped", Datasource: "none", Versioning: "none", Datasources: []string{}},
		{Name: DepTypeMultipleVersionOverride, What: "`multiple_version_override(module_name = …)`: listed and skipped, and its bazel_dep is not updated (the version must stay one of `versions`; beyond Renovate, which updates it)", Datasource: "none", Versioning: "none", Datasources: []string{}},
		{Name: DepTypeGitRepository, What: "`git_repository(remote = …, commit = …|tag = …)` repo rules of `use_repo_rule(\"@bazel_tools//tools/build_defs/repo:git.bzl\", \"git_repository\")`, as git_override; named after the remote's path", Datasource: "`git-refs` (commit), `git-tags` (tag)", Versioning: "`tag`", Datasources: []string{"git-refs", "git-tags"}},
		{Name: DepTypeNewGitRepository, What: "`new_git_repository(…)` repo rules, as git_repository", Datasource: "`git-refs` (commit), `git-tags` (tag)", Versioning: "`tag`", Datasources: []string{"git-refs", "git-tags"}},
		{Name: DepTypeOCIPull, What: "`oci.pull(image = …, tag = …, digest = …)` of `use_extension(\"@rules_oci//oci:extensions.bzl\", \"oci\")`: the tag, and the digest when there is one", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: DepTypeRulesImgPull, What: "rules_img `pull(registry = …, repository = …, tag = …, digest = …)` of `use_repo_rule(\"@rules_img//img:pull.bzl\", \"pull\")`; without `registry` the image is on Docker Hub", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: DepTypeMavenInstall, What: "`maven.install(artifacts = [\"group:artifact[:packaging[:classifier]]:version\", …])` and `maven.artifact(group = …, artifact = …, version = …)` of `use_extension(\"@rules_jvm_external//:extensions.bzl\", \"maven\")`; the repositories are the `repositories` of the installs with the same `name` (default `maven`), else Maven Central", Versioning: "`maven`", Datasources: []string{"maven"}},
		{Name: DepTypeCrateSpec, What: "`crate.spec(package = …, version = …)` of `use_extension(\"@rules_rust//crate_universe:extension.bzl\", \"crate\")`: the version is a Cargo requirement, rewritten as the [cargo](#cargo) manager's (`rangeStrategy`; without a lockfile deps reads, versions it admits are not proposed); `git` crates with a `tag` are git-tags deps", Datasource: "`crate` (crates.io), `git-tags` (git with tag)", Versioning: "`cargo`, `tag`", Datasources: []string{"crate", "git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNoVersion, When: "a bazel_dep or maven.artifact without `version` or with `version = \"\"`, a maven.install artifact without one (`group:artifact`, managed by a BOM)"},
		{Reason: skipNonLiteral, When: "a name, version, remote, image, tag or digest that is not a plain string literal (a variable, a concatenation, `None`, an escaped string)"},
		{Reason: skipPinned, When: "a bazel_dep whose module has a `single_version_override` with a `version`"},
		{Reason: "overridden by <override>", When: "a bazel_dep whose module has a `git_override`, `archive_override`, `local_path_override` or `multiple_version_override` (in the same file or in another module file of the workspace)"},
		{Reason: skipVersionPin, When: "the `single_version_override` itself: an override pins a version on purpose (often with `patches`), as in Renovate"},
		{Reason: skipArchive, When: "an `archive_override`"},
		{Reason: skipLocal, When: "a `local_path_override`"},
		{Reason: skipMultiple, When: "a `multiple_version_override`"},
		{Reason: skipRemote, When: "a `remote` that is not an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: skipBranch, When: "a git override or repository with `branch` and neither `commit` nor `tag`"},
		{Reason: skipGitRef, When: "a git override or repository without `commit` (a full 40-character SHA-1), `tag` or `branch`"},
		{Reason: skipImage, When: "an image that does not parse, or already carries a tag or digest"},
		{Reason: skipDigestNoTag, When: "an image with a `digest` but no `tag`"},
		{Reason: skipNoTag, When: "an image without `tag` and `digest`"},
		{Reason: skipCoordinates, When: "a maven artifact that is not `group:artifact[:packaging[:classifier]]:version`"},
		{Reason: skipCratePath, When: "a crate.spec with `path`"},
		{Reason: skipCrateGit, When: "a crate.spec with `git` and no `tag` (a `branch` or `rev`), or an unrecognized git URL"},
		{Reason: skipRequirement, When: "a crate.spec version outside the Cargo requirement forms of the cargo manager"},
	},
	Sections: []refdoc.Section{
		{Title: "Modules and overrides", Items: []string{
			"Only Bazel's module file functions and the extension tags and repo rules above are read; `module()`, `use_repo`, `include` and other extensions are ignored. Extension variables are recognized by their `use_extension`/`use_repo_rule` call in the same file (included segments cannot share variables), not by their name.",
			"Overrides apply to the bazel_dep of their `module_name` in every module file of the workspace: the files whose nearest directory holding a scanned `MODULE.bazel` is the same (the root module and its `*.MODULE.bazel` segments). A module with more than one override (an error in Bazel) keeps its bazel_dep updatable.",
			"Module versions are looked up in the workspace's registries: the `--registry` flags of the `.bazelrc` next to the `MODULE.bazel`, else the Bazel Central Registry; a `single_version_override(registry = …)` replaces them for its module. See the `bazel` datasource in [Datasources](#datasources).",
			"Versioning `bazel-module` orders versions as Bazel does: `RELEASE[-PRERELEASE][+BUILD]` with dot-separated identifiers, digit-only ones numerically and before the others (`1.2.3` < `1.2.3.1` < `1.2.3.bcr.1` < `1.2.4`; registry patches such as `1.8.2.bcr.1` are patch updates), a prerelease below its release, build metadata ignored. Update types follow the first three release identifiers. Prereleases (`1.5.6-9.bcr.2`, `2025-11-05.bcr.1`, whose release is `2025`) are only proposed from a prerelease.",
			"Bazel registries (the BCR included) publish no release times: `metadata.json` lists versions only. `minimumReleaseAge` cannot hold back a new module version: under the default `minimumReleaseAgeBehaviour: timestamp-optional` every version passes, under `timestamp-required` every version is held back. PRs show no release age for `bazel_dep` updates.",
		}},
		{Title: "MODULE.bazel.lock", Items: []string{
			"With an update of a module file or of the workspace's `.bazelversion` (the [bazelisk](#bazelisk) manager) whose workspace has a `MODULE.bazel.lock`, `apply` and `run` run `bazel --output_base=<tmp> mod deps --lockfile_mode=update` in a temporary mirror of the workspace with the patched files (other entries symlinked, so patches, `local_path_override` paths and `.bazelrc` resolve), then `bazel shutdown`. The new lockfile joins the update; a workspace without one gets none. Module files and `.bazelversion` updated in one PR regenerate the lockfile once, together.",
			"The tool is `bazel`, else `bazelisk` (Bazelisk uses the workspace's `.bazelversion`); for a `.bazelversion` update `bazelisk` comes first, so the lockfile is written by the new Bazel version (a `bazel` on `PATH` must be Bazelisk for that, as `bazel-contrib/setup-bazel` and npm's `@bazel/bazelisk` install it; a plain Bazel binary ignores `.bazelversion`).",
			"Without either the update fails with `bazel not found (needed to update MODULE.bazel.lock)`; `plan` does not run Bazel and notes `note    MODULE.bazel.lock changes are computed by apply/run`.",
			"`lockFileMaintenance` deletes and regenerates every `MODULE.bazel.lock` next to a scanned `MODULE.bazel` the same way, as Renovate does.",
		}},
	},
}

// BazeliskDoc is the reference documentation of the bazelisk manager.
var BazeliskDoc = refdoc.Manager{
	Name:    BazeliskName,
	Summary: "Updates the Bazel version Bazelisk reads from `.bazelversion` (its first line), as Renovate's bazelisk manager.",
	Files:   []string{"`.bazelversion` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeBazel, What: "the Bazel version (name `bazelbuild/bazel`)", Datasource: "`github-releases` (`bazelbuild/bazel`)", Versioning: "`semver`", Datasources: []string{"github-releases"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipFork, When: "`<fork>/<version>` (a Bazel fork Bazelisk downloads from another GitHub repository)"},
		{Reason: skipRolling, When: "`latest`, `latest-<n>`, `last_green`, `last_rc`, `7.x`, `*`: Bazelisk resolves them itself"},
	},
	Sections: []refdoc.Section{
		{Title: "MODULE.bazel.lock", Items: []string{
			"A `.bazelversion` update in a directory holding a `MODULE.bazel` and a `MODULE.bazel.lock` regenerates the lockfile with the new Bazel version, as module file updates do (see [bazel-module](#bazel-module)); Renovate's bazelisk manager leaves it alone. A `.bazelversion` elsewhere (Bazelisk only reads the one at the workspace root) or a workspace without a lockfile changes only `.bazelversion`.",
		}},
	},
}
