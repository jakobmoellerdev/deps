package kubernetesapi

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the kubernetes-api datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "the resource `kind` (`Deployment`, `Ingress`, `HelmRelease`)",
	Endpoints:    "none: a table of the API versions of each kind in deps, Renovate's (the Kubernetes [API deprecation guide](" + GuideURL + "), Flux and External Secrets resources) plus `autoscaling/v2beta2` and `flowcontrol.apiserver.k8s.io/v1beta3`/`v1` the guide added since",
	Auth:         "none",
	ReleaseTimes: "none",
	Notes: []refdoc.Section{{Title: "Deprecated API versions", Items: []string{
		"An API version Kubernetes no longer serves (the guide's \"Removed APIs by release\") or that a newer version of its API group supersedes is reported as deprecated, with the version to use: the newest of its group, else the newest version of the kind Kubernetes still serves.",
		"New API versions and removals arrive with deps releases.",
	}}},
}
