package actions

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubrunners"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// setupDatasources are the datasources of the known setup actions, sorted.
func setupDatasources() []string {
	seen := map[string]bool{}
	for _, in := range setupActions {
		seen[in.datasource] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// Doc is the reference documentation of the github-actions manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Finds `uses:` references to actions and reusable workflows in GitHub, Gitea and Forgejo workflow and action files, " +
		"the version inputs of known setup actions, and GitHub-hosted runner labels in `runs-on:`.",
	Files: []string{
		"`(workflow-templates|.github|.gitea|.forgejo)/(workflows|actions)/**/*.y(a)ml`",
		"any `action.y(a)ml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "action", What: "`uses: owner/repo[/path]@ref`", Datasource: "`git-tags`; `git-refs` for a branch ref (see Branch refs)", Versioning: "`tag`", Datasources: []string{"git-tags", "git-refs"}},
		{Name: "setup", What: "the version input of a setup action (see Setup action inputs)", Datasource: "per action (see Setup action inputs)", Versioning: "`semver` (full versions and ranges) or `tag`", Datasources: setupDatasources()},
		{Name: DepTypeRunner, What: "a GitHub-hosted runner label in a job's `runs-on:` (`ubuntu-24.04`, `macos-15`, `windows-2025`; see Runner labels)", Versioning: "`docker`", Datasources: []string{githubrunners.Name}},
	},
	Skips: []refdoc.Skip{
		{Reason: "local action", When: "`./…`, `$/…`"},
		{Reason: "docker reference (docker manager)", When: "`docker://…`, handled by the docker manager"},
		{Reason: "unrecognized uses", When: "no `@`, missing owner/repo/ref, unparsable URL"},
		{Reason: "non-version ref", When: "a ref that is neither a tag nor a branch name: an abbreviated SHA (`@abc1234`) or a ref that starts like a version but is none (`@v1.x`)"},
		{Reason: "pinned without version comment", When: "a SHA followed by a comment that is neither a version nor a single branch name (`# keep on main`)"},
		{Reason: "cannot locate value", When: "the value's bytes could not be found on its line"},
		{Reason: "expression", When: "setup input: `${{ … }}`"},
		{Reason: "non-version value", When: "setup input: empty, a word such as `lts/*`, `stable`, `pypy3.10`, `jruby-9.4`, or a file (`.ruby-version`)"},
		{Reason: "unsupported range", When: "setup input: outside npm's range grammar (`9.0.1xx`, `3.13t`), a multi-line list, or a setup-java range (`21.x`)"},
		{Reason: "unsupported distribution", When: "setup-java: `distribution` missing or other than `temurin`, `adopt`, `adopt-hotspot`"},
		{Reason: "non-version value", When: "runner label: `<runner>-latest` (`ubuntu-latest`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Extracted", Items: []string{
			"every scalar `uses:` outside `with:` and `env:`. This covers steps, job-level reusable workflows, `parallel:` and composite `runs.steps`.",
			"`owner/repo[/path]@ref` uses the [default host](configuration.md#actionsdefaulthost). `https://host/owner/repo@ref` names its host explicitly.",
			"The dep name is `owner/repo[/path]`. The source is `https://<host>/<owner>/<repo>`.",
		}},
		{Title: "Ref forms", Items: []string{
			"Tag: `@v4`, `@v4.2.0`.",
			"Pinned: `@<40/64-hex sha> # v4.2.0`, or `# ratchet:owner/repo@v4`.",
			"Commit pin: a bare `@<40/64-hex sha>` without a comment (see Commit pins).",
			"Branch: `@main`, `@release/v1`, or a SHA whose comment is only the branch name, `@<sha> # main` (see Branch refs).",
			"An action in a subdirectory (`owner/repo/<path>`) may carry a per-action tag prefix, `<path>/` or `<last path element>/`, in the tag or the comment: `@get-vault-secrets/v1.3.1`, `@<sha> # get-vault-secrets/v1.3.1`.",
			"Anything else is skipped.",
		}},
		{Title: "Branch refs", Items: []string{
			"A ref that is no version and looks like a branch name (not starting like a version, no abbreviated SHA) is a `git-refs` dep whose version is the branch, as Renovate's `github-digest` deps of such refs.",
			"It gets no updates unless `pin: digest` is set (skipped as `branch ref`): then it is pinned to the branch's commit (a `pin` update), `@main` → `@<sha> # main`. A ref that is no branch is looked up as a tag (`@stable`); one that is neither is skipped (`current version … not found upstream`).",
			"A SHA whose comment is a single branch name (`# main`, `# master`) follows that branch: a new commit on it is a `digest` update, and the comment stays. A comment token Renovate reads as a pinned version (`#get-vault-secrets-v1.2.0`) is none. Branch commits are not dated, so `minimumReleaseAge` does not hold them back.",
		}},
		{Title: "Commit pins", Items: []string{
			"A bare SHA that a tag points at is that tag's version (the newest one: `v4.2.0` rather than a floating `v4` on the same commit). It is updated like a pinned ref, and the new version is written as a comment: `@<sha>` → `@<new sha> # v4.3.0`. Rules match it with that version.",
			"A bare SHA no tag points at is ignored by default (`ignored by default (commit pin without version; …)`), as Renovate disables such unversioned references. A rule setting `ignore: false` (e.g. `managers: [github-actions]`, `names: [owner/repo]`) enables a `digest` update to the commit of the repository's default branch (its `HEAD`, listed with the tags in the same request), without a comment. The `digest` update type's `allowedUpdateTypes`, schedule and `minimumReleaseAge` (against the commit date, looked up only when a minimum applies) apply.",
			"Actions of the repository itself are not special-cased: once enabled, each new commit on its default branch is a new digest.",
			"Security advisories are not looked up for bare SHAs.",
		}},
		{Title: "Monorepo tags", Items: []string{
			"An action in a subdirectory of a repository that tags each action (`grafana/shared-workflows/actions/get-vault-secrets` with tags `get-vault-secrets/v1.3.1`) is versioned by the tags prefixed `<path>/` or `<last path element>/`; the version after the prefix is compared and written, digests and release times are looked up by the prefixed tag.",
			"Without such tags the repository's tags are the versions. A rule's `extractVersion` replaces this.",
		}},
		{Title: "Replacements", Body: "`uses: owner/repo[/path]@ref` can be [replaced](configuration.md#rulesreplacementname) by another `owner/repo[/path]` on the same host. A per-action tag prefix in the ref or the pinned version comment is rewritten in the same form (full path or last element); a one-element prefix moving to a multi-element path is rejected as ambiguous."},
		{Title: "Edits", Items: []string{
			"A tag is replaced by the new tag at the same precision (`@v4` → `@v5`, `@v4.1.0` → `@v4.2.0`).",
			"A pinned ref gets the new SHA, and the comment version is replaced.",
			"With `pin: digest`, a tag becomes `@<sha> # <tag>`, a branch `@<sha> # <branch>`.",
			"A floating pin (`@<sha> # v4`) whose tag moved gets a digest refresh.",
			"A per-action tag prefix is kept: `@get-vault-secrets/v1.3.1` → `@get-vault-secrets/v1.4.0`, pinned as `@<sha> # get-vault-secrets/v1.4.0`.",
			"A `with: version:` input equal to the current version (the tag or the pinned comment version) is bumped in the same edit, e.g. `@<sha> # v0.2.5` with `version: v0.2.5`. A different value is left alone, and setup actions keep their own version input (DepType `setup`).",
		}},
		setupSection(),
		{Title: "Runner labels", Items: []string{
			"A job's `runs-on:` label, or each label of a `runs-on:` list, of the form `<runner>-<version>` is a dep named `<runner>` (`ubuntu`, `macos`, `windows`, `xcode`) with that version, when the table of the `github-runners` datasource (see [Datasources](#datasources)) lists the version: `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15-large`, `windows-11-arm`. `<runner>-latest` is skipped. Other labels (`self-hosted`, larger-runner names, expressions other than `${{ matrix.<key> }}`) are not deps, as in Renovate. Beyond Renovate, the `labels` of a `runs-on: {group, labels}` mapping are read too, and a label `${{ matrix.<key> }}` stands for the scalar values of `<key>` in the job's `strategy.matrix` (its list and its `include` entries), which are updated in place.",
			"Updates keep the suffix: `ubuntu-22.04` → `ubuntu-24.04`, `ubuntu-22.04-arm` → `ubuntu-24.04-arm`, `macos-14-large` → `macos-15-large`. Deprecated images are never targets; a dep on one gets a `deprecated` warning.",
			"Only the version is rewritten; runner labels are never digest-pinned.",
			"Workflows under `.gitea/` and `.forgejo/` are skipped: their runners advertise labels of their own configuration (act_runner's `ubuntu-22.04` maps to an image the runner chose), so GitHub's images would be the wrong update source. Renovate extracts them too.",
		}},
	},
}

// setupSection documents setupActions as a table, sorted by action name.
func setupSection() refdoc.Section {
	var b strings.Builder
	b.WriteString("For these actions, the version input in `with:` is also a dep (DepType `setup`):\n\n")
	b.WriteString("| Action | Input | Datasource | Name |\n|---|---|---|---|\n")
	for _, a := range slices.Sorted(maps.Keys(setupActions)) {
		in := setupActions[a]
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` |\n", a, in.input, in.datasource, in.name)
	}
	return refdoc.Section{
		Title: "Setup action inputs",
		Body:  strings.TrimSuffix(b.String(), "\n"),
		Items: []string{
			"Only literal values are extracted.",
			"A full `[v]X.Y.Z` (with build metadata for setup-java: `17.0.7+7`) uses `semver` and keeps the `v` style.",
			"A partial `26`, `1.27` or `v2.1` uses `tag` versioning, and its precision is kept (`26` → `27`).",
			"A range in npm's grammar, which setup-node, setup-go and setup-python resolve with node-semver (`1.27.x`, `>=24`, `^3.12`, `20 || 22`), is a range dep (`semver` versioning, its anchor as the version) under [rangeStrategy](configuration.md#rangestrategy) (auto: replace, as there is no lockfile): a version the range admits is left alone, one outside it rewrites the range at its precision (`1.27.x` → `1.28.x`, `^3.12` → `^4.0`); `bump` raises an admitted range's floor (`>=24` → `>=26`), `widen` appends (`8.0.x || 10.0.x`). As in Renovate, which uses npm versioning for these inputs.",
			"setup-dotnet's partial values are channels (`8.0`, `8`): the dotnet-version datasource lists full SDK versions only, so they are ranges too (`8.0` → `10.0`). Feature-band wildcards (`9.0.1xx`) are skipped.",
			"setup-java is versioned by the java-version datasource (Eclipse Temurin) only for `distribution: temurin`, `adopt` or `adopt-hotspot`, as in Renovate; the name is `java-jre` for a `java-package` starting with `jre`, else `java-jdk`. Full and partial values only (Renovate skips java-version ranges too).",
			"Skips: `expression` (`${{ … }}`), `non-version value` (empty, a word such as `lts/*`, `stable`, `pypy3.10`, or a file such as `.ruby-version`), `unsupported range`, `unsupported distribution`, `cannot locate value`.",
		},
	}
}
