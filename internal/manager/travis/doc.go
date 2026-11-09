package travis

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the travis manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the Node.js versions of Travis CI configurations.",
	Files: []string{
		"`.travis.y(a)ml` at the repository root",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNode, What: "each version of `node_js` (a version or a list) at the top level and in the `jobs.include` and `matrix.include` entries (name `node`)", Versioning: "as [nvm](#nvm): `semver`; partial versions `tag`", Datasources: []string{"node-version"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-version value", When: "an alias such as `node`, `stable`, `lts/*`, `lts/iron`: it floats to the newest matching release by itself"},
		{Reason: "unsupported range", When: "anything else that is not a version with an optional operator or x-range"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Major updates are not allowed by default, as in Renovate: a `node_js` list is a test matrix, and moving each entry to the newest major would collapse it. `20.11` still moves to the newest `20.x` minor; a rule `{managers: [travis], allowedUpdateTypes: [major, minor, patch]}` allows majors.",
			"Versions are classified as in the [nvm](#nvm) manager: a partial `20` keeps its precision, a full `20.11.1` uses `semver`, and only the version token is replaced (quotes stay).",
		}},
	},
}
