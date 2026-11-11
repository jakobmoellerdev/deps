package helm

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// crdResources are the resources document extracts, by "<apiVersion group> <kind>". Both
// prefilters (hasCRDGroup for files, mayHaveCRD for documents) derive their groups from it.
var crdResources = map[string]func(x *crdExtractor, n, spec *yaml.Node){
	"helm.k0sproject.io Chart":        func(x *crdExtractor, _, spec *yaml.Node) { x.k0sChart(spec) },
	"k0s.k0sproject.io ClusterConfig": func(x *crdExtractor, _, spec *yaml.Node) { x.k0sConfig(spec) },
	"k0sctl.k0sproject.io Cluster": func(x *crdExtractor, _, spec *yaml.Node) {
		x.k0sConfig(yamlspan.Get(yamlspan.Get(yamlspan.Get(spec, "k0s"), "config"), "spec"))
	},
	"helm.toolkit.fluxcd.io HelmRelease":      (*crdExtractor).helmRelease,
	"source.toolkit.fluxcd.io HelmRepository": (*crdExtractor).helmRepository,
	"source.toolkit.fluxcd.io OCIRepository":  (*crdExtractor).ociRepository,
	"source.toolkit.fluxcd.io GitRepository":  func(x *crdExtractor, _, spec *yaml.Node) { x.gitRepository(spec) },
	"argoproj.io Application":                 func(x *crdExtractor, _, spec *yaml.Node) { x.argoSpec(spec) },
	"argoproj.io ApplicationSet": func(x *crdExtractor, _, spec *yaml.Node) {
		x.argoSpec(yamlspan.Get(yamlspan.Get(spec, "template"), "spec"))
	},
	"config.projectsveltos.io ClusterProfile": func(x *crdExtractor, _, spec *yaml.Node) { x.sveltosCharts(spec) },
	"config.projectsveltos.io Profile":        func(x *crdExtractor, _, spec *yaml.Node) { x.sveltosCharts(spec) },
	"config.projectsveltos.io ClusterPromotion": func(x *crdExtractor, _, spec *yaml.Node) {
		x.sveltosCharts(yamlspan.Get(spec, "profileSpec"))
	},
	"lib.projectsveltos.io EventTrigger": func(x *crdExtractor, _, spec *yaml.Node) { x.sveltosCharts(spec) },
	"fleet.cattle.io GitRepo":            func(x *crdExtractor, _, spec *yaml.Node) { x.fleetGitRepo(spec) },
}

// crdGroups are the distinct API groups of crdResources.
var crdGroups = func() [][]byte {
	var gs [][]byte
	for k := range crdResources {
		g, _, _ := strings.Cut(k, " ")
		if !strings.HasSuffix(g, ".io") {
			panic("helm: hasCRDGroup scans for .io/, group " + g + " does not end in .io")
		}
		if g := []byte(g); !slices.ContainsFunc(gs, func(h []byte) bool { return bytes.Equal(g, h) }) {
			gs = append(gs, g)
		}
	}
	slices.SortFunc(gs, bytes.Compare)
	return gs
}()

// hasCRDGroup reports whether src contains "<group>/" for one of crdGroups, the file
// prefilter: a file without one is not parsed. One scan for ".io/", where every group ends,
// replaces a scan of src per group.
func hasCRDGroup(src []byte) bool {
	for {
		i := bytes.Index(src, []byte(".io/"))
		if i < 0 {
			return false
		}
		for _, g := range crdGroups {
			if bytes.HasSuffix(src[:i+len(".io")], g) {
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
	file    string
	src     []byte
	lines   []int
	aliases map[string]string // config registryAliases
	deps    []dep.Dep
	fileLinks
}

// manifests extracts chart deps from the k0s, Flux and Argo CD resources of a Kubernetes YAML
// file, from the documents of env.YAML, the file's shared parse. Unparsable files (templates)
// yield no deps.
func (m *helmManager) manifests(file string, src []byte, env manager.Env) []dep.Dep {
	if !hasCRDGroup(src) {
		return nil
	}
	x := crdExtractor{file: file, src: src, lines: yamlspan.LineStarts(src), aliases: env.RegistryAliases}
	err := env.YAML.Documents(src, mayHaveCRD, func(doc *yaml.Node) {
		if len(doc.Content) == 1 {
			x.document(doc.Content[0])
		}
	})
	if err != nil {
		slog.Debug("skipping unparsable yaml", "file", file, "err", err)
		return nil
	}
	m.links.add(file, &x.fileLinks)
	return x.deps
}

// mayHaveCRD reports whether manifests must parse a document: document dispatches on an
// apiVersion group of crdGroups, so the group follows "apiVersion" on its line or the next (a
// value continued or in a block scalar). A CustomResourceDefinition of those groups names them
// in its spec, not its apiVersion, and is skipped. A templated document ("{{") is parsed too,
// so an unparsable template still yields no deps.
func mayHaveCRD(doc []byte) bool {
	for rest := doc; ; {
		i := bytes.Index(rest, []byte("apiVersion"))
		if i < 0 {
			break
		}
		rest = rest[i+len("apiVersion"):]
		value := rest
		if j := bytes.IndexByte(value, '\n'); j >= 0 {
			if k := bytes.IndexByte(value[j+1:], '\n'); k >= 0 {
				value = value[:j+1+k]
			}
		}
		for _, g := range crdGroups {
			if bytes.Contains(value, g) {
				return true
			}
		}
	}
	return bytes.Contains(doc, []byte("{{"))
}

// document dispatches one YAML document on its apiVersion group and kind (crdResources).
func (x *crdExtractor) document(n *yaml.Node) {
	group, _, _ := strings.Cut(yamlspan.Value(yamlspan.Get(n, "apiVersion")), "/")
	if extract := crdResources[group+" "+yamlspan.Value(yamlspan.Get(n, "kind"))]; extract != nil {
		extract(x, n, yamlspan.Get(n, "spec"))
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
