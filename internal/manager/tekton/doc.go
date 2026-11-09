package tekton

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the tekton manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates Tekton bundles, step images and the remote tasks and pipelines of Pipelines-as-Code annotations in Tekton resources.",
	Files: []string{
		"any `*.y(a)ml` file with a document of API group `tekton.dev` or a subgroup such as `triggers.tekton.dev` (Renovate's tekton manager matches no file unless `managerFilePatterns` are set; deps reads every YAML file and parses only documents mentioning `tekton.dev`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: TypeBundle, What: "the bundle of a `taskRef` or `pipelineRef` (TaskRun, PipelineRun, Pipeline `tasks` and `finally`): the `bundle` param of `resolver: bundles` (else of the deprecated `resource` list), else the legacy `bundle` field", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: TypeStepImage, What: "`image` of the `steps`, `sidecars` and `stepTemplate` of a Task, of a TaskRun's `taskSpec` and of inline `taskSpec`s of Pipeline tasks; a StepAction's `spec.image`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: TypeAnnotation, What: "URLs in the Pipelines-as-Code annotations `pipelinesascode.tekton.dev/task`, `task-<N>` and `pipeline` (one URL or a `[a, b]` list): `https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>` (name `<owner>/<repo>`), `https://github.com/<owner>/<repo>/raw/<ref>/<path>` and `https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>` (name `github.com/<owner>/<repo>`); the ref is rewritten in place", Datasource: "`github-releases` (release assets), `git-tags` (raw files)", Versioning: "`tag`", Datasources: []string{"github-releases", "git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in an image or bundle that no [`registryAliases`](configuration.md#registryaliases) entry resolves (`$(params.x)`); `$` or `{{` in an annotation ref"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "commit ref", When: "an annotation URL pinned to a 40-hex commit"},
		{Reason: "non-version ref", When: "an annotation URL whose ref is no version tag (a branch such as `main`)"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars, an annotation continued over lines)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Bundles and images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"Every document is read, a `List`'s `items`, a PipelineRun's inline `pipelineSpec` and a TriggerTemplate's `resourcetemplates` too. Like Renovate, a spec is read for the shapes of all kinds at once. References by name or through other resolvers (git, hub, cluster, http) are not deps.",
			"Annotation values that are no such URL (Artifact Hub names such as `git-clone:0.9`, files of the repository) are not deps.",
		}},
	},
}
