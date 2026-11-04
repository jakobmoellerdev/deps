package helm

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// helmChartMediaType is the layer media type of Helm charts in OCI registries.
const helmChartMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

// helmRepository records a Flux HelmRepository (source.toolkit.fluxcd.io): spec.url, an OCI
// registry when spec.type is oci.
func (x *crdExtractor) helmRepository(n, spec *yaml.Node) {
	url := value(get(spec, "url"))
	if url == "" {
		return
	}
	if value(get(spec, "type")) == "oci" && !strings.HasPrefix(url, "oci://") {
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
	ns := value(get(get(n, "metadata"), "namespace"))
	if cr := get(spec, "chartRef"); cr != nil {
		if value(get(cr, "kind")) == "OCIRepository" {
			x.chartRefs = append(x.chartRefs, refKey(cr, ns))
		}
		return
	}
	cs := get(get(spec, "chart"), "spec")
	chartNode := str(get(cs, "chart"))
	if chartNode == nil || chartNode.Value == "" {
		return
	}
	src := get(cs, "sourceRef")
	switch kind := value(get(src, "kind")); kind {
	case "HelmRepository":
		i := x.add(TypeFlux, "", chartNode.Value, chartNode, get(cs, "version"), "")
		if x.deps[i].Skip == "" {
			x.refs = append(x.refs, ref{i: i, kind: refFlux, key: refKey(src, ns)})
		}
	case "":
		x.add(TypeFlux, "", chartNode.Value, chartNode, get(cs, "version"), "no sourceRef")
	default:
		x.add(TypeFlux, "", chartNode.Value, chartNode, get(cs, "version"), "chart from "+kind+" source")
	}
}

// ociRepository extracts a Flux OCIRepository (source.toolkit.fluxcd.io): the chart is the last
// path segment of spec.url, the version spec.ref.tag. An OCIRepository is a chart only when its
// spec.layerSelector.mediaType is the Helm chart media type or a HelmRelease's chartRef names
// it; Link drops the others (Kustomization artifacts).
func (x *crdExtractor) ociRepository(n, spec *yaml.Node) {
	urlNode := str(get(spec, "url"))
	if urlNode == nil || !strings.HasPrefix(urlNode.Value, "oci://") {
		return
	}
	repo, chart := splitOCI(urlNode.Value)
	if chart == "" {
		return
	}
	r := get(spec, "ref")
	var skip string
	switch {
	case get(r, "digest") != nil:
		skip = "pinned by digest"
	case get(r, "semver") != nil:
		skip = "version range"
	}
	i := x.add(TypeFlux, repo, chart, urlNode, get(r, "tag"), skip)
	if value(get(get(spec, "layerSelector"), "mediaType")) != helmChartMediaType {
		x.refs = append(x.refs, ref{i: i, kind: refOCI, key: objectKey(n)})
	}
}

// objectKey returns the namespace and name of the object n.
func objectKey(n *yaml.Node) key {
	md := get(n, "metadata")
	return key{namespace: value(get(md, "namespace")), name: value(get(md, "name"))}
}

// refKey returns the object a sourceRef/chartRef r names; its namespace defaults to ns.
func refKey(r *yaml.Node, ns string) key {
	k := key{namespace: value(get(r, "namespace")), name: value(get(r, "name"))}
	if k.namespace == "" {
		k.namespace = ns
	}
	return k
}
