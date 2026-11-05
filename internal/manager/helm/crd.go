package helm

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// crdGroups prefilter Kubernetes YAML: a file containing none of these API groups followed by
// ".io/" is not parsed.
var crdGroups = [][]byte{[]byte("k0sproject"), []byte("fluxcd"), []byte("argoproj")}

// hasCRDGroup reports whether src contains "<group>.io/" for one of crdGroups. One scan for
// ".io/" replaces one scan of src per group.
func hasCRDGroup(src []byte) bool {
	for {
		i := bytes.Index(src, []byte(".io/"))
		if i < 0 {
			return false
		}
		for _, g := range crdGroups {
			if bytes.HasSuffix(src[:i], g) {
				return true
			}
		}
		src = src[i+len(".io/"):]
	}
}

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
	if !hasCRDGroup(src) {
		return nil
	}
	x := crdExtractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
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
	group, _, _ := strings.Cut(yamlspan.Value(yamlspan.Get(n, "apiVersion")), "/")
	kind := yamlspan.Value(yamlspan.Get(n, "kind"))
	spec := yamlspan.Get(n, "spec")
	switch group + " " + kind {
	case "helm.k0sproject.io Chart":
		x.k0sChart(spec)
	case "k0s.k0sproject.io ClusterConfig":
		x.k0sConfig(spec)
	case "k0sctl.k0sproject.io Cluster":
		x.k0sConfig(yamlspan.Get(yamlspan.Get(yamlspan.Get(spec, "k0s"), "config"), "spec"))
	case "helm.toolkit.fluxcd.io HelmRelease":
		x.helmRelease(n, spec)
	case "source.toolkit.fluxcd.io HelmRepository":
		x.helmRepository(n, spec)
	case "source.toolkit.fluxcd.io OCIRepository":
		x.ociRepository(n, spec)
	case "argoproj.io Application":
		x.argoSpec(spec)
	case "argoproj.io ApplicationSet":
		x.argoSpec(yamlspan.Get(yamlspan.Get(spec, "template"), "spec"))
	}
}

// add appends the dep of chart in repo (the resolved repository URL; "" when Link resolves it)
// with the version in v. at is the node whose line a dep without a version gets; skip, when
// set, is the skip reason. It returns the index of the dep in x.deps.
func (x *crdExtractor) add(depType, repo, chart string, at, v *yaml.Node, skip string) int {
	d := newDep(x.file, depType)
	d.Name, d.Source, d.Line = chart, strings.TrimSuffix(repo, "/"), at.Line
	switch v = yamlspan.Str(v); {
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

// rename sets the NameParts of dep i from its chart scalar n (see chartParts).
func (x *crdExtractor) rename(i int, n *yaml.Node, ociPrefix bool, repo *yaml.Node) {
	x.deps[i].NameParts = ChartParts(x.src, x.lines, n, x.deps[i].Name, ociPrefix, repo)
}

// splitOCI splits oci://host/path/chart into the repository oci://host/path and the chart.
func splitOCI(ref string) (repo, chart string) {
	i := strings.LastIndexByte(ref, '/')
	if i < len("oci://") {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}
