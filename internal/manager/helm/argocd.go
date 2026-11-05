package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"
)

// argoSpec extracts the Helm sources of an Argo CD Application spec (or an ApplicationSet's
// spec.template.spec): spec.source and spec.sources[].
func (x *crdExtractor) argoSpec(spec *yaml.Node) {
	x.argoSource(get(spec, "source"))
	for _, s := range seq(get(spec, "sources")) {
		x.argoSource(s)
	}
}

// argoSource extracts one source when it is a Helm chart (chart is set): the version is
// targetRevision; a repoURL without a scheme is an OCI registry.
func (x *crdExtractor) argoSource(s *yaml.Node) {
	chartNode := str(get(s, "chart"))
	if chartNode == nil || chartNode.Value == "" {
		return
	}
	repo := value(get(s, "repoURL"))
	var skip string
	switch {
	case strings.Contains(repo, "{{") || strings.Contains(chartNode.Value, "{{"):
		skip = "template value"
	case repo == "":
		skip = "no repoURL"
	case !strings.Contains(repo, "://"):
		repo = "oci://" + repo
	}
	x.add(TypeArgoCD, repo, chartNode.Value, chartNode, get(s, "targetRevision"), skip)
}
