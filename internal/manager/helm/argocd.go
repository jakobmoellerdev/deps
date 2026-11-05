package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// argoSpec extracts the Helm sources of an Argo CD Application spec (or an ApplicationSet's
// spec.template.spec): spec.source and spec.sources[].
func (x *crdExtractor) argoSpec(spec *yaml.Node) {
	x.argoSource(yamlspan.Get(spec, "source"))
	for _, s := range seq(yamlspan.Get(spec, "sources")) {
		x.argoSource(s)
	}
}

// argoSource extracts one source when it is a Helm chart (chart is set): the version is
// targetRevision; a repoURL without a scheme is an OCI registry. chart and repoURL are the
// NameParts.
func (x *crdExtractor) argoSource(s *yaml.Node) {
	chartNode := yamlspan.Str(yamlspan.Get(s, "chart"))
	if chartNode == nil || chartNode.Value == "" {
		return
	}
	repoNode := yamlspan.Str(yamlspan.Get(s, "repoURL"))
	repo := yamlspan.Value(repoNode)
	var skip string
	switch {
	case strings.Contains(repo, "{{") || strings.Contains(chartNode.Value, "{{"):
		skip = "template value"
	case repo == "":
		skip = "no repoURL"
	case !strings.Contains(repo, "://"):
		repo = "oci://" + repo
	}
	i := x.add(TypeArgoCD, repo, chartNode.Value, chartNode, yamlspan.Get(s, "targetRevision"), skip)
	if skip == "" {
		x.rename(i, chartNode, false, repoNode)
	}
}
