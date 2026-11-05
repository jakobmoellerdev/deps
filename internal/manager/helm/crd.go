package helm

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// crdMarkers prefilter Kubernetes YAML: a file containing none of these API groups is not
// parsed.
var crdMarkers = [][]byte{[]byte("k0sproject.io/"), []byte("fluxcd.io/"), []byte("argoproj.io/")}

// crdExtractor extracts the chart deps of one multi-document Kubernetes YAML file. It records
// the definitions (repository aliases, HelmRepositories) and the references to resolve in
// Link in fileLinks.
type crdExtractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
	fileLinks
}

// manifests extracts chart deps from the k0s, Flux and Argo CD resources of a Kubernetes YAML
// file. Unparsable files (templates) yield no deps.
func (m *helmManager) manifests(file string, src []byte) []dep.Dep {
	if !slices.ContainsFunc(crdMarkers, func(b []byte) bool { return bytes.Contains(src, b) }) {
		return nil
	}
	x := crdExtractor{file: file, src: src, lines: lineStarts(src)}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Debug("skipping unparsable yaml", "file", file, "err", err)
			return nil
		}
		if len(doc.Content) == 1 {
			x.document(doc.Content[0])
		}
	}
	m.links.add(file, &x.fileLinks)
	return x.deps
}

// document dispatches one YAML document on its apiVersion group and kind.
func (x *crdExtractor) document(n *yaml.Node) {
	group, _, _ := strings.Cut(value(get(n, "apiVersion")), "/")
	kind := value(get(n, "kind"))
	spec := get(n, "spec")
	switch group + " " + kind {
	case "helm.k0sproject.io Chart":
		x.k0sChart(spec)
	case "k0s.k0sproject.io ClusterConfig":
		x.k0sConfig(spec)
	case "k0sctl.k0sproject.io Cluster":
		x.k0sConfig(get(get(get(spec, "k0s"), "config"), "spec"))
	case "helm.toolkit.fluxcd.io HelmRelease":
		x.helmRelease(n, spec)
	case "source.toolkit.fluxcd.io HelmRepository":
		x.helmRepository(n, spec)
	case "source.toolkit.fluxcd.io OCIRepository":
		x.ociRepository(n, spec)
	case "argoproj.io Application":
		x.argoSpec(spec)
	case "argoproj.io ApplicationSet":
		x.argoSpec(get(get(spec, "template"), "spec"))
	}
}

// add appends the dep of chart in repo (the resolved repository URL; "" when Link resolves it)
// with the version in v. at is the node whose line a dep without a version gets; skip, when
// set, is the skip reason. It returns the index of the dep in x.deps.
func (x *crdExtractor) add(depType, repo, chart string, at, v *yaml.Node, skip string) int {
	d := newDep(x.file, depType)
	d.Name, d.Source, d.Line = chart, strings.TrimSuffix(repo, "/"), at.Line
	switch v = str(v); {
	case skip == "":
		setVersion(&d, x.src, x.lines, v)
	case v != nil:
		d.Version, d.Line, d.Skip = v.Value, v.Line, skip
	default:
		d.Skip = skip
	}
	x.deps = append(x.deps, d)
	return len(x.deps) - 1
}

// splitOCI splits oci://host/path/chart into the repository oci://host/path and the chart.
func splitOCI(ref string) (repo, chart string) {
	i := strings.LastIndexByte(ref, '/')
	if i < len("oci://") {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}
