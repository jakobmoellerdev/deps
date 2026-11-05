package droneci

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the droneci manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images in Drone CI pipelines.",
	Files: []string{
		"`.drone.yml`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`image` of the `steps` (and the Drone 0.8 `pipeline` mapping) of every document", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "service", What: "`image` of the `services` (list or mapping) of every document", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "no tag", When: "an image without tag"},
		{Reason: "digest without tag", When: "an image pinned by digest only"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars, a double-quoted image continued over lines with `\\`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned.",
			"Every YAML document is scanned (`kind: pipeline` documents of any `type`); documents without `image` are not parsed.",
		}},
	},
}
