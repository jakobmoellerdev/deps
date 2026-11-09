// Package kubernetesapi lists the API versions of Kubernetes resource kinds (`Deployment`:
// `extensions/v1beta1`, `apps/v1beta1`, `apps/v1beta2`, `apps/v1`) from a table kept in the
// code, as Renovate's kubernetes-api datasource does, and reports the deprecated ones.
package kubernetesapi

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the datasource name (Renovate's).
const Name = "kubernetes-api"

// Versioning is the versioning scheme of its deps (Renovate's kubernetes-api versioning).
const Versioning = "kubernetes-api"

// GuideURL is the Kubernetes API deprecation guide the table follows.
const GuideURL = "https://kubernetes.io/docs/reference/using-api/deprecation-guide/"

// apis are the API versions of each kind, oldest first, as Renovate's data/kubernetes-api.json5
// lists them (the Kubernetes deprecation guide, Flux and External Secrets custom resources),
// plus the versions the guide added since: autoscaling/v2beta2 (HorizontalPodAutoscaler) and
// flowcontrol.apiserver.k8s.io/v1beta3 and v1 (FlowSchema, PriorityLevelConfiguration).
var apis = map[string][]string{
	"NetworkPolicy":                  {"extensions/v1beta1", "networking.k8s.io/v1"},
	"DaemonSet":                      {"extensions/v1beta1", "apps/v1beta2", "apps/v1"},
	"Deployment":                     {"extensions/v1beta1", "apps/v1beta1", "apps/v1beta2", "apps/v1"},
	"StatefulSet":                    {"apps/v1beta1", "apps/v1beta2", "apps/v1"},
	"ReplicaSet":                     {"extensions/v1beta1", "apps/v1beta1", "apps/v1beta2", "apps/v1"},
	"PodSecurityPolicy":              {"extensions/v1beta1", "policy/v1beta1"},
	"MutatingWebhookConfiguration":   {"admissionregistration.k8s.io/v1beta1", "admissionregistration.k8s.io/v1"},
	"ValidatingWebhookConfiguration": {"admissionregistration.k8s.io/v1beta1", "admissionregistration.k8s.io/v1"},
	"CustomResourceDefinition":       {"apiextensions.k8s.io/v1beta1", "apiextensions.k8s.io/v1"},
	"APIService":                     {"apiregistration.k8s.io/v1beta1", "apiregistration.k8s.io/v1"},
	"TokenReview":                    {"authentication.k8s.io/v1beta1", "authentication.k8s.io/v1"},
	"LocalSubjectAccessReview":       {"authorization.k8s.io/v1beta1", "authorization.k8s.io/v1"},
	"SelfSubjectAccessReview":        {"authorization.k8s.io/v1beta1", "authorization.k8s.io/v1"},
	"SubjectAccessReview":            {"authorization.k8s.io/v1beta1", "authorization.k8s.io/v1"},
	"SelfSubjectRulesReview":         {"authorization.k8s.io/v1beta1", "authorization.k8s.io/v1"},
	"CertificateSigningRequest":      {"certificates.k8s.io/v1beta1", "certificates.k8s.io/v1"},
	"Lease":                          {"coordination.k8s.io/v1beta1", "coordination.k8s.io/v1"},
	"Ingress":                        {"extensions/v1beta1", "networking.k8s.io/v1beta1", "networking.k8s.io/v1"},
	"IngressClass":                   {"networking.k8s.io/v1beta1", "networking.k8s.io/v1"},
	"ClusterRole":                    {"rbac.authorization.k8s.io/v1beta1", "rbac.authorization.k8s.io/v1"},
	"ClusterRoleBinding":             {"rbac.authorization.k8s.io/v1beta1", "rbac.authorization.k8s.io/v1"},
	"Role":                           {"rbac.authorization.k8s.io/v1beta1", "rbac.authorization.k8s.io/v1"},
	"RoleBinding":                    {"rbac.authorization.k8s.io/v1beta1", "rbac.authorization.k8s.io/v1"},
	"PriorityClass":                  {"scheduling.k8s.io/v1beta1", "scheduling.k8s.io/v1"},
	"CSIDriver":                      {"storage.k8s.io/v1beta1", "storage.k8s.io/v1"},
	"CSINode":                        {"storage.k8s.io/v1beta1", "storage.k8s.io/v1"},
	"StorageClass":                   {"storage.k8s.io/v1beta1", "storage.k8s.io/v1"},
	"VolumeAttachment":               {"storage.k8s.io/v1beta1", "storage.k8s.io/v1"},
	"CronJob":                        {"batch/v1beta1", "batch/v1"},
	"EndpointSlice":                  {"discovery.k8s.io/v1beta1", "discovery.k8s.io/v1"},
	"Event":                          {"events.k8s.io/v1beta1", "events.k8s.io/v1"},
	"HorizontalPodAutoscaler":        {"autoscaling/v2beta1", "autoscaling/v2beta2", "autoscaling/v2"},
	"PodDisruptionBudget":            {"policy/v1beta1", "policy/v1"},
	"RuntimeClass":                   {"node.k8s.io/v1beta1", "node.k8s.io/v1"},
	"FlowSchema":                     {"flowcontrol.apiserver.k8s.io/v1beta1", "flowcontrol.apiserver.k8s.io/v1beta2", "flowcontrol.apiserver.k8s.io/v1beta3", "flowcontrol.apiserver.k8s.io/v1"},
	"PriorityLevelConfiguration":     {"flowcontrol.apiserver.k8s.io/v1beta1", "flowcontrol.apiserver.k8s.io/v1beta2", "flowcontrol.apiserver.k8s.io/v1beta3", "flowcontrol.apiserver.k8s.io/v1"},
	"CSIStorageCapacity":             {"storage.k8s.io/v1beta1", "storage.k8s.io/v1"},

	// Flux (https://fluxcd.io).
	"Alert":                 {"notification.toolkit.fluxcd.io/v1beta2", "notification.toolkit.fluxcd.io/v1beta3"},
	"Bucket":                {"source.toolkit.fluxcd.io/v1alpha1", "source.toolkit.fluxcd.io/v1beta1", "source.toolkit.fluxcd.io/v1beta2", "source.toolkit.fluxcd.io/v1"},
	"GitRepository":         {"source.toolkit.fluxcd.io/v1alpha1", "source.toolkit.fluxcd.io/v1beta1", "source.toolkit.fluxcd.io/v1beta2", "source.toolkit.fluxcd.io/v1"},
	"HelmChart":             {"source.toolkit.fluxcd.io/v1alpha1", "source.toolkit.fluxcd.io/v1beta1", "source.toolkit.fluxcd.io/v1"},
	"HelmRelease":           {"helm.toolkit.fluxcd.io/v2beta1", "helm.toolkit.fluxcd.io/v2beta2", "helm.toolkit.fluxcd.io/v2"},
	"HelmRepository":        {"source.toolkit.fluxcd.io/v1alpha1", "source.toolkit.fluxcd.io/v1beta1", "source.toolkit.fluxcd.io/v1beta2", "source.toolkit.fluxcd.io/v1"},
	"ImagePolicy":           {"image.toolkit.fluxcd.io/v1beta2", "image.toolkit.fluxcd.io/v1"},
	"ImageRepository":       {"image.toolkit.fluxcd.io/v1beta2", "image.toolkit.fluxcd.io/v1"},
	"ImageUpdateAutomation": {"image.toolkit.fluxcd.io/v1beta1", "image.toolkit.fluxcd.io/v1beta2", "image.toolkit.fluxcd.io/v1"},
	"OCIRepository":         {"source.toolkit.fluxcd.io/v1beta2", "source.toolkit.fluxcd.io/v1"},
	"Provider":              {"notification.toolkit.fluxcd.io/v1beta2", "notification.toolkit.fluxcd.io/v1beta3"},
	"Receiver":              {"notification.toolkit.fluxcd.io/v1beta2", "notification.toolkit.fluxcd.io/v1"},
	// Flux's Kustomization and kustomize's kustomization.yaml share the kind.
	"Kustomization": {"kustomize.toolkit.fluxcd.io/v1beta2", "kustomize.toolkit.fluxcd.io/v1", "kustomize.config.k8s.io/v1beta1"},

	// External Secrets (https://external-secrets.io/latest/api/spec/).
	"ExternalSecret":        {"external-secrets.io/v1alpha1", "external-secrets.io/v1beta1", "external-secrets.io/v1"},
	"ClusterExternalSecret": {"external-secrets.io/v1alpha1", "external-secrets.io/v1beta1", "external-secrets.io/v1"},
	"SecretStore":           {"external-secrets.io/v1alpha1", "external-secrets.io/v1beta1", "external-secrets.io/v1"},
	"ClusterSecretStore":    {"external-secrets.io/v1alpha1", "external-secrets.io/v1beta1", "external-secrets.io/v1"},
}

// removed are the Kubernetes releases that stopped serving an API version of a kind
// ("<kind> <apiVersion>"), from the deprecation guide's "Removed APIs by release".
var removed = func() map[string]string {
	m := map[string]string{}
	add := func(release string, apiVersion string, kinds ...string) {
		for _, k := range kinds {
			m[k+" "+apiVersion] = release
		}
	}
	add("1.16", "extensions/v1beta1", "NetworkPolicy", "DaemonSet", "Deployment", "ReplicaSet", "PodSecurityPolicy")
	add("1.16", "apps/v1beta1", "Deployment", "StatefulSet", "ReplicaSet")
	add("1.16", "apps/v1beta2", "DaemonSet", "Deployment", "StatefulSet", "ReplicaSet")
	add("1.22", "admissionregistration.k8s.io/v1beta1", "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration")
	add("1.22", "apiextensions.k8s.io/v1beta1", "CustomResourceDefinition")
	add("1.22", "apiregistration.k8s.io/v1beta1", "APIService")
	add("1.22", "authentication.k8s.io/v1beta1", "TokenReview")
	add("1.22", "authorization.k8s.io/v1beta1", "LocalSubjectAccessReview", "SelfSubjectAccessReview", "SubjectAccessReview", "SelfSubjectRulesReview")
	add("1.22", "certificates.k8s.io/v1beta1", "CertificateSigningRequest")
	add("1.22", "coordination.k8s.io/v1beta1", "Lease")
	add("1.22", "extensions/v1beta1", "Ingress")
	add("1.22", "networking.k8s.io/v1beta1", "Ingress", "IngressClass")
	add("1.22", "rbac.authorization.k8s.io/v1beta1", "ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding")
	add("1.22", "scheduling.k8s.io/v1beta1", "PriorityClass")
	add("1.22", "storage.k8s.io/v1beta1", "CSIDriver", "CSINode", "StorageClass", "VolumeAttachment")
	add("1.25", "batch/v1beta1", "CronJob")
	add("1.25", "discovery.k8s.io/v1beta1", "EndpointSlice")
	add("1.25", "events.k8s.io/v1beta1", "Event")
	add("1.25", "autoscaling/v2beta1", "HorizontalPodAutoscaler")
	add("1.25", "policy/v1beta1", "PodDisruptionBudget", "PodSecurityPolicy")
	add("1.25", "node.k8s.io/v1beta1", "RuntimeClass")
	add("1.26", "flowcontrol.apiserver.k8s.io/v1beta1", "FlowSchema", "PriorityLevelConfiguration")
	add("1.26", "autoscaling/v2beta2", "HorizontalPodAutoscaler")
	add("1.27", "storage.k8s.io/v1beta1", "CSIStorageCapacity")
	add("1.29", "flowcontrol.apiserver.k8s.io/v1beta2", "FlowSchema", "PriorityLevelConfiguration")
	add("1.32", "flowcontrol.apiserver.k8s.io/v1beta3", "FlowSchema", "PriorityLevelConfiguration")
	return m
}()

// releases are the API versions of each kind as datasource releases.
var releases = func() map[string][]datasource.Release {
	m := make(map[string][]datasource.Release, len(apis))
	for kind, vs := range apis {
		rels := make([]datasource.Release, len(vs))
		for i, v := range vs {
			rels[i] = datasource.Release{Version: v}
		}
		m[kind] = rels
	}
	return m
}()

// Known reports whether apiVersion is one of the table's versions of kind: only those are deps,
// so a kind the table names with an API group it does not list (`Event` `v1`, a custom resource
// that shares a kind name) is none.
func Known(kind, apiVersion string) bool { return slices.Contains(apis[kind], apiVersion) }

// Source is the kubernetes-api datasource; it makes no request.
type Source struct{}

// New returns the kubernetes-api datasource.
func New() Source { return Source{} }

// Releases returns the API versions of kind d.Name.
func (Source) Releases(_ context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	rels, ok := releases[d.Name]
	if !ok {
		return nil, fmt.Errorf("%s: unknown kind %q", Name, d.Name)
	}
	return rels, nil
}

// Deprecation implements datasource.DeprecationSource: version of kind d.Name is no longer
// served by a Kubernetes release (the deprecation guide), or a newer version of its API group
// supersedes it (Flux, External Secrets). The message names the version to move to (see
// replacement).
func (Source) Deprecation(d dep.Dep, version string) string {
	if !Known(d.Name, version) {
		return ""
	}
	use := replacement(d.Name, version)
	release := removed[d.Name+" "+version]
	switch {
	case release != "" && use != "":
		return "apiVersion " + version + " of " + d.Name + " is no longer served since Kubernetes " + release + ": use " + use
	case release != "":
		return "apiVersion " + version + " of " + d.Name + " is no longer served since Kubernetes " + release
	case use != "":
		return "apiVersion " + version + " of " + d.Name + " is deprecated: use " + use
	}
	return ""
}

// replacement returns the version of kind to move to from version: the newest of its API
// group, when newer; else, for a version Kubernetes removed, the last version of the kind it
// still serves (in another group: `extensions/v1beta1` → `apps/v1`); "" when there is none.
func replacement(kind, version string) string {
	vs := apis[kind]
	scheme, _ := versioning.Get(Versioning)
	best := version
	for _, v := range vs {
		if group(v) == group(version) && scheme.Compare(v, best) > 0 {
			best = v
		}
	}
	if best != version {
		return best
	}
	if removed[kind+" "+version] == "" {
		return ""
	}
	for _, v := range slices.Backward(vs) {
		if removed[kind+" "+v] == "" {
			return v
		}
	}
	return ""
}

// group returns the API group of apiVersion ("" for the core group, `v1`).
func group(apiVersion string) string {
	g, _, ok := strings.Cut(apiVersion, "/")
	if !ok {
		return ""
	}
	return g
}

// Digest is unsupported: API versions have no digests.
func (Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}
