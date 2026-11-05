package actions

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

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
		{Name: "action", What: "`uses: owner/repo[/path]@ref`", Datasource: "`git-tags`", Versioning: "`tag`"},
		{Name: "setup", What: "the version input of a setup action (see Setup action inputs)", Datasource: "per action (see Setup action inputs)", Versioning: "`semver` or `tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "local action", When: "`./…`, `$/…`"},
		{Reason: "docker reference (docker manager)", When: "`docker://…`, handled by the docker manager"},
		{Reason: "unrecognized uses", When: "no `@`, missing owner/repo/ref, unparsable URL"},
		{Reason: "non-version ref", When: "a branch or other ref that is not a tag (`@main`)"},
		{Reason: "pinned without version comment", When: "a SHA without a parsable `# vX` comment"},
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
			"An action in a subdirectory (`owner/repo/<path>`) may carry a per-action tag prefix, `<path>/` or `<last path element>/`, in the tag or the comment: `@get-vault-secrets/v1.3.1`, `@<sha> # get-vault-secrets/v1.3.1`.",
			"Anything else is skipped.",
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
