package bitbucketpipelines

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the bitbucket-pipelines manager
// (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images and pipes in Bitbucket Pipelines configuration.",
	Files: []string{
		"`*-pipelines.yml` (`bitbucket-pipelines.yml`, shared pipeline configurations), except the files of the azure-pipelines manager",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`image` (string or `name:`) at top level and in steps, wherever they nest (`pipelines.<kind>`, `parallel`, `stage`, `definitions.steps`)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "service", What: "`definitions.services.<name>.image` (string or `name:`)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "pipe", What: "`pipe: <account>/<repo>:<version>` (Source `https://bitbucket.org/<account>/<repo>`), `pipe: docker://<image>` as an image", Datasource: "`git-tags`; `docker` for `docker://`", Versioning: "`tag`; `docker` for `docker://`", Datasources: []string{"git-tags", "docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image or pipe that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "no version", When: "a pipe without `:<version>`"},
		{Reason: "invalid pipe", When: "a pipe that is not `<account>/<repo>[:<version>]` or `docker://<image>`"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"Pipes are Bitbucket repositories; their tags are listed with `git-tags` (Renovate's `bitbucket-tags`), and only the version after `:` is rewritten.",
			"YAML anchors (`&step`) are extracted where they are written; aliases (`*step`, `<<: *step`) add nothing.",
		}},
	},
}
