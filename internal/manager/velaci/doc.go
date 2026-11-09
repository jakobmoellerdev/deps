package velaci

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the velaci manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images in Vela pipelines.",
	Files: []string{
		"`.vela.y(a)ml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`image` of the `steps` and of the `steps` of every `stages` entry", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "service", What: "`image` of the `services`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "secret", What: "`origin.image` of the `secrets` (the secret plugin)", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "invalid reference", When: "the image reference does not parse (an inline template such as `{{ .tag }}`)"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"Steps pulled in from `templates` are not read: they live in the template's repository.",
		}},
	},
}
