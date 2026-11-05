package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// k0sChart extracts a k0s Chart resource (helm.k0sproject.io): spec.chartName and spec.version,
// with the repository from spec.repository.url or the chart name's alias.
func (x *crdExtractor) k0sChart(spec *yaml.Node) {
	x.k0sChartRef(yamlspan.Get(spec, "chartName"), yamlspan.Get(spec, "version"), yamlspan.Value(yamlspan.Get(yamlspan.Get(spec, "repository"), "url")))
}

// k0sConfig extracts the charts of a k0s ClusterConfig spec (spec.extensions.helm.charts) and
// records its repository aliases (spec.extensions.helm.repositories) for every file of the run.
func (x *crdExtractor) k0sConfig(spec *yaml.Node) {
	h := yamlspan.Get(yamlspan.Get(spec, "extensions"), "helm")
	for _, r := range seq(yamlspan.Get(h, "repositories")) {
		name, url := yamlspan.Value(yamlspan.Get(r, "name")), yamlspan.Value(yamlspan.Get(r, "url"))
		if name == "" || url == "" {
			continue
		}
		if x.k0sRepos == nil {
			x.k0sRepos = map[string]string{}
		}
		x.k0sRepos[name] = strings.TrimSuffix(url, "/")
	}
	for _, c := range seq(yamlspan.Get(h, "charts")) {
		x.k0sChartRef(yamlspan.Get(c, "chartname"), yamlspan.Get(c, "version"), "")
	}
}

// k0sChartRef adds the k0s chart named by chartName: <alias>/<chart>, oci://<repo>/<chart>,
// or a local archive. inline is the repository URL given with the chart, if any.
func (x *crdExtractor) k0sChartRef(chartName, version *yaml.Node, inline string) {
	name := yamlspan.Value(chartName)
	switch {
	case name == "":
		return
	case strings.Contains(name, "{{"):
		x.add(TypeK0s, "", name, chartName, version, "template value")
	case strings.HasPrefix(name, "oci://"):
		repo, chart := splitOCI(name)
		if chart == "" {
			x.add(TypeK0s, "", name, chartName, version, "invalid chart reference")
			return
		}
		x.add(TypeK0s, repo, chart, chartName, version, "")
	case inline != "":
		x.add(TypeK0s, inline, name[strings.LastIndexByte(name, '/')+1:], chartName, version, "")
	case strings.HasPrefix(name, "/") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tgz"):
		x.add(TypeK0s, "", name, chartName, version, "local chart")
	default:
		alias, chart, _ := strings.Cut(name, "/")
		if chart == "" || strings.Contains(chart, "/") {
			x.add(TypeK0s, "", name, chartName, version, "invalid chart reference")
			return
		}
		i := x.add(TypeK0s, "", chart, chartName, version, "")
		if x.deps[i].Skip == "" {
			x.refs = append(x.refs, ref{i: i, kind: refK0s, key: key{name: alias}})
		}
	}
}

// seq returns the items of sequence n (nil if n is not a sequence).
func seq(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}
