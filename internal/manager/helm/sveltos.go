package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// sveltosCharts extracts the helmCharts[] of a Sveltos ClusterProfile, Profile or
// EventTrigger spec (a ClusterPromotion's spec.profileSpec), as Renovate's sveltos manager
// does: the version is chartVersion. For an oci:// repositoryURL that URL is the chart
// reference (repository and chart); otherwise the chart is chartName without its
// `<repositoryName>/` prefix, in the repository at repositoryURL; only those can be renamed.
func (x *crdExtractor) sveltosCharts(spec *yaml.Node) {
	for _, c := range yamlspan.Seq(yamlspan.Get(spec, "helmCharts")) {
		repoNode := yamlspan.Str(yamlspan.Get(c, "repositoryURL"))
		chartNode := yamlspan.Str(yamlspan.Get(c, "chartName"))
		repo, chart := yamlspan.Value(repoNode), yamlspan.Value(chartNode)
		oci := strings.HasPrefix(repo, "oci://")
		if oci {
			repo, chart = splitOCI(repo)
		} else {
			chart = strings.TrimPrefix(chart, yamlspan.Value(yamlspan.Get(c, "repositoryName"))+"/")
			chart = strings.TrimRight(chart, "/")
		}
		if chart == "" {
			continue
		}
		var skip string
		switch {
		case strings.Contains(repo, "{{") || strings.Contains(chart, "{{"):
			skip = "template value"
		case repo == "":
			skip = "no repositoryURL"
		}
		at := chartNode
		if at == nil {
			at = repoNode
		}
		i := x.add(TypeSveltos, repo, chart, at, yamlspan.Get(c, "chartVersion"), skip)
		// An OCI entry names the chart in both repositoryURL and chartName, so a rename
		// (NameParts) is only offered for repository charts.
		if skip == "" && !oci {
			x.rename(i, chartNode, false, repoNode)
		}
	}
}
