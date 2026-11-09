package kubernetes

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/kubernetesapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the kubernetes manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the `apiVersion` of Kubernetes manifests within its API group and flags API versions that Kubernetes no longer serves or that a newer version supersedes. " +
		"The manifests' container images are the docker manager's `kubernetes` deps.",
	Files: []string{"`*.y(a)ml` with a top-level `apiVersion` and `kind`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeAPIVersion, What: "a YAML document's top-level `apiVersion` when the `kubernetes-api` table lists it for the document's `kind` (the dep name): Kubernetes built-in kinds of the [API deprecation guide](" + kubernetesapi.GuideURL + "), Flux and External Secrets resources", Versioning: "`kubernetes-api`", Datasources: []string{kubernetesapi.Name}},
	},
	Sections: []refdoc.Section{
		{Title: "Extraction", Items: []string{
			"Only `apiVersion` and `kind` lines at column 0 count, per document (`---` separates documents); nothing else is parsed. A document containing `{{` is a template and has no dep: Helm charts choose the apiVersion by cluster version there.",
			"An apiVersion the table does not list for the kind (`Event` `v1`, `HorizontalPodAutoscaler` `autoscaling/v1`, a custom resource sharing a kind name) is no dep, so it is never moved to another API version.",
		}},
		{Title: "Updates and warnings", Items: []string{
			"As in Renovate, an update stays within the API group (`kubernetes-api` versioning: `apps/v1beta2` → `apps/v1`, `autoscaling/v2beta2` → `autoscaling/v2`). Only the `apiVersion` value is rewritten: fields that changed between the versions (an Ingress backend, a Deployment `selector`) need a manual fix in the PR.",
			"A version Kubernetes no longer serves, or that a newer version of its group supersedes, gets a `deprecated` warning on the dependency dashboard and in its update PR, naming the version to use: `apiVersion extensions/v1beta1 of Deployment is no longer served since Kubernetes 1.16: use apps/v1`. Moves between groups (`extensions/v1beta1` → `apps/v1`) are only warned about, never proposed.",
		}},
	},
}
