package glasskube

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the glasskube manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the package versions of Glasskube `Package` and `ClusterPackage` resources from the package repository their `PackageRepository` names.",
	Files: []string{
		"every `*.y(a)ml` containing `packages.glasskube.dev/`, parsed document by document (only documents whose `apiVersion` is in that group). Renovate's manager has no default file pattern and needs `managerFilePatterns`; deps reads every YAML file",
	},
	DepTypes: []refdoc.DepType{
		{Name: TypePackage, What: "`Package` (`packages.glasskube.dev/*`) `spec.packageInfo.version` of `spec.packageInfo.name`", Versioning: "`glasskube`", Datasources: []string{"glasskube-packages"}},
		{Name: TypeClusterPackage, What: "`ClusterPackage` (`packages.glasskube.dev/*`) `spec.packageInfo.version` of `spec.packageInfo.name`", Versioning: "`glasskube`", Datasources: []string{"glasskube-packages"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "unknown PackageRepository <name>", When: "no scanned `PackageRepository` has the `metadata.name` of the package's `spec.packageInfo.repositoryName` (Renovate's `unknown-registry`)"},
		{Reason: "no default PackageRepository", When: "the package has no (or an empty) `repositoryName` and no scanned `PackageRepository` carries the annotation `packages.glasskube.dev/default-repository: \"true\"` (Renovate's `unknown-registry`)"},
		{Reason: "no version", When: "`spec.packageInfo.version` is empty"},
		{Reason: "cannot locate value", When: "the version bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Package repositories", Items: []string{
			"A package is looked up in the `spec.url` of a `PackageRepository` (`packages.glasskube.dev/*`) of any scanned file, as Renovate's `extractAllPackageFiles`: the first, in path and document order, whose `metadata.name` is the package's `spec.packageInfo.repositoryName`, or, without one, the first carrying the default-repository annotation (Glasskube's install manifest declares `glasskube` at https://packages.dl.glasskube.dev/packages as the default).",
			"As in Renovate, a package is not looked up in the public repository when the scanned files declare no matching `PackageRepository`: the cluster's repositories are not known. Commit the `PackageRepository` resources (or Glasskube's install manifest) next to the packages.",
		}},
		{Title: "Edits", Items: []string{
			"The version is replaced in place (`v1.17.0+1` → `v1.19.1+1`); the `+N` package revision is part of the version (see the `glasskube` versioning).",
		}},
	},
}
