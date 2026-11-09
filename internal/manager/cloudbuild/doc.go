package cloudbuild

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the cloudbuild manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the builder images of Google Cloud Build configurations.",
	Files: []string{
		"`cloudbuild.y(a)ml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`name` (the builder image) of every entry of `steps`", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image (substitutions such as `${_REPOSITORY}`) that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"`images` and `artifacts` name what the build pushes, not what it uses, and are not read. Templates such as `cloudbuild.yaml.hbs` are not matched (Renovate's pattern is not anchored at the end).",
		}},
	},
}
