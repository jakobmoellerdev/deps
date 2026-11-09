package buildkite

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the buildkite manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates plugin versions and container images in Buildkite pipelines.",
	Files: []string{
		"paths ending in `buildkite.y(a)ml` (`pipeline.buildkite.yml`), `.buildkite/**/*.y(a)ml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "plugin", What: "`<plugin>#<version>`: a key (a plugin with options, in `plugins` or anchored anywhere) or an item of a `plugins` list. `<name>` is `github.com/buildkite-plugins/<name>-buildkite-plugin`, `<org>/<name>` `github.com/<org>/<name>-buildkite-plugin`, a git URL (`https://<host>/<path>`, `ssh://git@<host>/<path>`) that repository", Datasource: "`git-tags`", Versioning: "`tag`", Datasources: []string{"git-tags"}},
		{Name: "image", What: "`image` values outside `agents`: of the pipeline and steps, of the docker plugin, of agent-stack-k8s containers and sidecars", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the plugin reference, or in the image without a matching [`registryAliases`](configuration.md#registryaliases) entry"},
		{Reason: "invalid plugin", When: "a plugin location with more than two path segments that is no `https://` or `ssh://git@` URL"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Plugin versions are tags of the plugin's repository; only the version after `#` is rewritten. A version that is no tag version (a branch, a commit) is skipped by the engine as a non-version value.",
			"`image` below `agents` is the agent's VM image (Elastic CI Stack, GCP image families), not a container, and is not read.",
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)).",
			"YAML anchors are extracted where they are written; aliases add nothing.",
		}},
	},
}
