package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// helmChartMediaType is the layer media type of Helm charts in OCI registries.
const helmChartMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

// helmRepository records a Flux HelmRepository (source.toolkit.fluxcd.io): spec.url, an OCI
// registry when spec.type is oci.
func (x *crdExtractor) helmRepository(n, spec *yaml.Node) {
	url := yamlspan.Value(yamlspan.Get(spec, "url"))
	if url == "" {
		return
	}
	if yamlspan.Value(yamlspan.Get(spec, "type")) == "oci" && !strings.HasPrefix(url, "oci://") {
		url = "oci://" + url
	}
	if x.helmRepos == nil {
		x.helmRepos = map[key]string{}
	}
	x.helmRepos[objectKey(n)] = strings.TrimSuffix(url, "/")
}

// helmRelease extracts the chart of a Flux HelmRelease (helm.toolkit.fluxcd.io):
// spec.chart.spec.{chart,version} from the sourceRef HelmRepository, whose namespace defaults
// to the HelmRelease's. A spec.chartRef to an OCIRepository marks that OCIRepository as a chart.
func (x *crdExtractor) helmRelease(n, spec *yaml.Node) {
	ns := yamlspan.Value(yamlspan.Get(yamlspan.Get(n, "metadata"), "namespace"))
	if cr := yamlspan.Get(spec, "chartRef"); cr != nil {
		if yamlspan.Value(yamlspan.Get(cr, "kind")) == "OCIRepository" {
			x.chartRefs = append(x.chartRefs, refKey(cr, ns))
		}
		return
	}
	cs := yamlspan.Get(yamlspan.Get(spec, "chart"), "spec")
	chartNode := yamlspan.Str(yamlspan.Get(cs, "chart"))
	if chartNode == nil || chartNode.Value == "" {
		return
	}
	src := yamlspan.Get(cs, "sourceRef")
	switch kind := yamlspan.Value(yamlspan.Get(src, "kind")); kind {
	case "HelmRepository":
		i := x.add(TypeFlux, "", chartNode.Value, chartNode, yamlspan.Get(cs, "version"), "")
		if x.deps[i].Skip == "" {
			x.refs = append(x.refs, ref{i: i, kind: refFlux, key: refKey(src, ns)})
		}
	case "":
		x.add(TypeFlux, "", chartNode.Value, chartNode, yamlspan.Get(cs, "version"), "no sourceRef")
	default:
		x.add(TypeFlux, "", chartNode.Value, chartNode, yamlspan.Get(cs, "version"), "chart from "+kind+" source")
	}
}

// ociRepository extracts a Flux OCIRepository (source.toolkit.fluxcd.io): the chart is the last
// path segment of spec.url, the version spec.ref.tag. An OCIRepository is a chart only when its
// spec.layerSelector.mediaType is the Helm chart media type or a HelmRelease's chartRef names
// it; Link drops the others (Kustomization artifacts).
func (x *crdExtractor) ociRepository(n, spec *yaml.Node) {
	urlNode := yamlspan.Str(yamlspan.Get(spec, "url"))
	if urlNode == nil || !strings.HasPrefix(urlNode.Value, "oci://") {
		return
	}
	repo, chart := splitOCI(urlNode.Value)
	if chart == "" {
		return
	}
	r := yamlspan.Get(spec, "ref")
	var skip string
	switch {
	case yamlspan.Get(r, "digest") != nil:
		skip = "pinned by digest"
	case yamlspan.Get(r, "semver") != nil:
		skip = "version range"
	}
	i := x.add(TypeFlux, repo, chart, urlNode, yamlspan.Get(r, "tag"), skip)
	if yamlspan.Value(yamlspan.Get(yamlspan.Get(spec, "layerSelector"), "mediaType")) != helmChartMediaType {
		x.refs = append(x.refs, ref{i: i, kind: refOCI, key: objectKey(n)})
	}
}

// objectKey returns the namespace and name of the object n.
func objectKey(n *yaml.Node) key {
	md := yamlspan.Get(n, "metadata")
	return key{namespace: yamlspan.Value(yamlspan.Get(md, "namespace")), name: yamlspan.Value(yamlspan.Get(md, "name"))}
}

// refKey returns the object a sourceRef/chartRef r names; its namespace defaults to ns.
func refKey(r *yaml.Node, ns string) key {
	k := key{namespace: yamlspan.Value(yamlspan.Get(r, "namespace")), name: yamlspan.Value(yamlspan.Get(r, "name"))}
	if k.namespace == "" {
		k.namespace = ns
	}
	return k
}
