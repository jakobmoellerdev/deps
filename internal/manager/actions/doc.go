package actions

import (
	"fmt"
	"maps"
	"slices"
	"strings"

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
		"plus the version inputs of known setup actions.",
	Files: []string{
		"`(workflow-templates|.github|.gitea|.forgejo)/(workflows|actions)/**/*.y(a)ml`",
		"any `action.y(a)ml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "action", What: "`uses: owner/repo[/path]@ref`", Versioning: "`tag`", Datasources: []string{"git-tags"}},
		{Name: "setup", What: "the version input of a setup action (see Setup action inputs)", Datasource: "per action (see Setup action inputs)", Versioning: "`semver` or `tag`", Datasources: setupDatasources()},
	},
	Skips: []refdoc.Skip{
		{Reason: "local action", When: "`./…`, `$/…`"},
		{Reason: "docker reference (docker manager)", When: "`docker://…`, handled by the docker manager"},
		{Reason: "unrecognized uses", When: "no `@`, missing owner/repo/ref, unparsable URL"},
		{Reason: "non-version ref", When: "a branch or other ref that is not a tag (`@main`)"},
		{Reason: "pinned without version comment", When: "a SHA followed by a comment that is no version (`# main`)"},
		{Reason: "cannot locate value", When: "the value's bytes could not be found on its line"},
		{Reason: "expression", When: "setup input: `${{ … }}`"},
		{Reason: "non-version value", When: "setup input: empty or a word such as `lts/*`, `stable`"},
		{Reason: "unsupported range", When: "setup input: a range rather than a literal version"},
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
			"An action in a subdirectory (`owner/repo/<path>`) may carry a per-action tag prefix, `<path>/` or `<last path element>/`, in the tag or the comment: `@get-vault-secrets/v1.3.1`, `@<sha> # get-vault-secrets/v1.3.1`.",
			"Anything else is skipped.",
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
			"With `pin: digest`, a tag becomes `@<sha> # <tag>`.",
			"A floating pin (`@<sha> # v4`) whose tag moved gets a digest refresh.",
			"A per-action tag prefix is kept: `@get-vault-secrets/v1.3.1` → `@get-vault-secrets/v1.4.0`, pinned as `@<sha> # get-vault-secrets/v1.4.0`.",
			"A `with: version:` input equal to the current version (the tag or the pinned comment version) is bumped in the same edit, e.g. `@<sha> # v0.2.5` with `version: v0.2.5`. A different value is left alone, and setup actions keep their own version input (DepType `setup`).",
		}},
		setupSection(),
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
			"A full `[v]X.Y.Z` uses `semver` and keeps the `v` style.",
			"A partial `26`, `1.27` or `v2.1` uses `tag` versioning, and its precision is kept (`26` → `27`).",
			"setup-python and setup-java are not extracted.",
			"Skips: `expression` (`${{ … }}`), `non-version value` (empty or a word such as `lts/*`, `stable`), `unsupported range`, `cannot locate value`.",
		},
	}
}
