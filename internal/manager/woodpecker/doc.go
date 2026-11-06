package woodpecker

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the woodpecker manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images in Woodpecker CI workflows.",
	Files: []string{
		"`.woodpecker.y(a)ml`, `.woodpecker/<name>.y(a)ml` at the repository root",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`image` of the `steps`, `clone` and legacy `pipeline` steps (a list, or a mapping of step names)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "service", What: "`image` of the `services` (list or mapping)", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image (matrix or environment variables) that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"An image written once as an anchor (`variables: [&golang_image 'golang:1.27']`) and used as `image: *golang_image` is one dep, at the anchor.",
		}},
	},
}
