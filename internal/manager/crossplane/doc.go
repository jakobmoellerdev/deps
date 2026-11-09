package crossplane

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the crossplane manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the package images of Crossplane providers, configurations and functions.",
	Files: []string{
		"any `*.y(a)ml` containing `pkg.crossplane.io/`, parsed document by document (Renovate's crossplane matches no file unless `managerFilePatterns` are set; deps reads every YAML file and extracts nothing from files without such a resource)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "provider", What: "`spec.package` of a `Provider` (`pkg.crossplane.io/*`)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "configuration", What: "`spec.package` of a `Configuration`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "function", What: "`spec.package` of a `Function`", Versioning: "`docker`", Datasources: []string{"docker"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the package that no [`registryAliases`](configuration.md#registryaliases) entry resolves"},
		{Reason: "invalid reference", When: "the package reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Packages are OCI images (`xpkg.crossplane.io/crossplane-contrib/provider-nop:v0.4.0`) and are parsed and edited like the docker manager's images: tags update, digests are refreshed or pinned, and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes.",
			"A file that does not parse as YAML (a template) yields no deps. Container images of a `DeploymentRuntimeConfig` are Kubernetes containers, which the docker manager finds.",
		}},
	},
}
