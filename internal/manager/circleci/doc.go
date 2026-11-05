package circleci

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the circleci manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates orbs and container images in CircleCI configuration.",
	Files: []string{
		"`*.y(a)ml` below a `.circleci/` directory",
	},
	DepTypes: []refdoc.DepType{
		{Name: "orb", What: "`orbs: {<alias>: <namespace>/<name>@<version>}`, also in inline orb definitions; full (`5.1.0`) and partial (`5`, `5.1`) versions keep their precision", Versioning: "`tag`", Datasources: []string{"orb"}},
		{Name: "image", What: "`docker[].image` of `executors` and `jobs` (also in inline orbs), and `image` of top-level `aliases[]` entries", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "parameter reference", When: "`<< … >>` (pipeline or job parameters) or `$` in the orb or image"},
		{Reason: "no version", When: "an orb without `@<version>`"},
		{Reason: "invalid orb", When: "an orb name without `<namespace>/`"},
		{Reason: "variable reference", When: "`$` in an image"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "no tag", When: "an image without tag"},
		{Reason: "digest without tag", When: "an image pinned by digest only"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned.",
			"`@volatile` and `@dev:<label>` orbs are skipped as non-version values. Renovate pins a partial orb version to the full one (its `rangeStrategy` `pin`); deps keeps the precision (`circleci/node@5` → `circleci/node@7`).",
			"`machine.image` values are CircleCI VM images, not container images, and are not extracted (as in Renovate).",
			"YAML anchors are extracted where they are written; aliases and merge keys (`<<: *defaults`) add nothing.",
		}},
	},
}
