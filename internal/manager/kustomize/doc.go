package kustomize

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the kustomize manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates image tags and digests, Helm chart versions and the `?ref=` of remote git resources in Kustomize kustomization files.",
	Files:   []string{"`kustomization.yaml`, `kustomization.yml`, `Kustomization`"},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`images[]` `newTag` (`1.2` or `1.2@sha256:…`) and `digest` of `newName`, else `name`", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "helmChart", What: "`helmCharts[]` `version` of chart `name` from `repo` (`https://`, `http://` or `oci://`)", Datasource: "`helm`", Versioning: "`semver`"},
		{Name: "resource", What: "remote git targets in `resources[]`, `components[]` and `bases[]` with a `ref` (or `version`) query parameter: `[git::]https://host/org/repo[.git][//path]`, `ssh://[user@]host/…`, `git@host:org/repo[.git][//path]`, `host/org/repo[//path]`; the span covers the ref value", Datasource: "`git-tags`", Versioning: "`tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "no tag", When: "image: no `newTag` and no `digest` (a rename only)"},
		{Reason: "digest without tag", When: "image: a `digest` without `newTag`"},
		{Reason: "invalid reference", When: "image: the name and tag are no valid image reference"},
		{Reason: "invalid digest", When: "image: a `digest` field that is not `sha256:…`"},
		{Reason: "variable reference", When: "image: the name or tag contains `$`"},
		{Reason: "no repo", When: "helmChart: no `repo` (a chart from `chartHome`)"},
		{Reason: "unknown chart repository <repo>", When: "helmChart: a `repo` that is no `https://`, `http://` or `oci://` URL"},
		{Reason: "no version", When: "helmChart: the version is missing or empty"},
		{Reason: "version range", When: "helmChart: anything but a full `MAJOR.MINOR.PATCH[-pre][+build]` (optionally `v`-prefixed)"},
		{Reason: "no ref", When: "resource: an empty `ref=`"},
		{Reason: "commit ref", When: "resource: the ref is a full commit SHA"},
		{Reason: "non-version ref", When: "resource: the ref is a branch or a tag that is no `v?N[.N[.N]]` version"},
		{Reason: "invalid repository URL", When: "resource: no repository path could be derived from the target"},
		{Reason: "cannot locate value", When: "the value's bytes could not be found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Remote resources", Items: []string{
			"Without `//`, the repository ends at `.git`, after `_git/<repo>` (Azure DevOps), after `org/repo` on github.com, gitlab.com and bitbucket.org, or at the end of the path.",
			"The dep name is `host/org/repo`; tags are listed from `https://host/org/repo` (also for `ssh://` and scp-like targets). Targets without a `ref` track a branch and are no deps.",
		}},
		{Title: "Replacements", Body: "An `images` entry is [replaced](configuration.md#rulesreplacementname) by rewriting its `newName`, or by inserting `newName: <image>` after `name` (which the manifests match on); flow-style entries are not replaced. A `helmCharts` entry takes a chart name, or `<repository URL>/<chart>` to move it, which also rewrites `repo`."},
		{Title: "Edits", Body: "The tag, chart version or ref is replaced in place; a digest-pinned image also gets its new digest. Images without a digest are not pinned: kustomize would need a new `digest` field."},
	},
}
