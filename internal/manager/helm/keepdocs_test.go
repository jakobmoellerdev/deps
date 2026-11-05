package helm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// manifestsWholeFile is manifests before it skipped documents without a CRD group, the oracle
// of the parity tests. parsed is false when a document of src does not parse.
func (m *helmManager) manifestsWholeFile(file string, src []byte) (deps []dep.Dep, parsed bool) {
	if !hasCRDGroup(src) {
		return nil, true
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
			return nil, false
		}
		if len(doc.Content) == 1 {
			x.document(doc.Content[0])
		}
	}
	m.links.add(file, &x.fileLinks)
	return x.deps, true
}

// checkCRDParity compares manifests with the oracle on files that parse as a whole.
func checkCRDParity(t *testing.T, file string, src []byte) {
	t.Helper()
	got, want := &helmManager{}, &helmManager{}
	wd, parsed := want.manifestsWholeFile(file, src)
	if !parsed {
		return
	}
	if gd := got.manifests(file, src); !reflect.DeepEqual(gd, wd) {
		t.Fatalf("%s: manifests = %+v\nwhole file = %+v", file, gd, wd)
	}
	g, w := &got.links, &want.links
	if !reflect.DeepEqual(g.k0sRepos, w.k0sRepos) || !reflect.DeepEqual(g.helmRepos, w.helmRepos) ||
		!reflect.DeepEqual(g.chartRefs, w.chartRefs) || !reflect.DeepEqual(g.refs, w.refs) {
		t.Fatalf("%s: links differ", file)
	}
}

var crdKeepDocsSeeds = []string{
	"apiVersion: v1\nkind: ConfigMap\ndata: {a: b}\n---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\n" +
		"metadata: {name: r, namespace: n}\nspec: {url: https://charts.example.com}\n---\n" +
		"apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: x, namespace: n}\n" +
		"spec:\n  chart:\n    spec: {chart: c, version: 1.2.3, sourceRef: {kind: HelmRepository, name: r}}\n",
	"apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nspec: {group: argoproj.io}\n---\n" +
		"apiVersion: argoproj.io/v1alpha1\nkind: Application\nspec:\n  source: {repoURL: https://charts.example.com, chart: c, targetRevision: 1.0.0}\n",
	"apiVersion: helm.k0sproject.io/v1beta1\nkind: Chart\nspec: {chartName: r/c, version: 1.0.0}\n---\nkind: X\n",
}

func TestManifestsKeepDocumentsParity(t *testing.T) {
	for i, s := range crdKeepDocsSeeds {
		checkCRDParity(t, "seed"+string(rune('0'+i))+".yaml", []byte(s))
	}
	paths, srcs := repoYAML(t)
	for i, p := range paths {
		checkCRDParity(t, p, srcs[i])
	}
}

func FuzzManifestsKeepDocuments(f *testing.F) {
	for _, s := range crdKeepDocsSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkCRDParity(t, "f.yaml", []byte(src))
	})
}

// TestManifestsUnparsableDocuments pins which parse errors still drop a file's chart deps: an
// error in a templated document does, one in a document without a CRD apiVersion (never
// parsed) no longer does.
func TestManifestsUnparsableDocuments(t *testing.T) {
	chart := "apiVersion: helm.k0sproject.io/v1beta1\nkind: Chart\nspec: {chartName: r/c, version: 1.0.0}\n"
	for src, want := range map[string]int{
		chart + "---\n{{ bad\n":           0,
		"x: [unterminated\n---\n" + chart: 1,
	} {
		if got := len((&helmManager{}).manifests("f.yaml", []byte(src))); got != want {
			t.Errorf("%q: %d deps, want %d", src, got, want)
		}
	}
}

// BenchmarkManifestsKeepDocuments extracts an Argo CD install manifest: large argoproj.io
// CustomResourceDefinitions, then a few Applications deploying charts.
func BenchmarkManifestsKeepDocuments(b *testing.B) {
	var src strings.Builder
	for i := range 10 {
		fmt.Fprintf(&src, "---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: x%d.argoproj.io\n"+
			"spec:\n  group: argoproj.io\n  versions:\n  - name: v1alpha1\n    schema:\n      openAPIV3Schema:\n        properties:\n", i)
		for j := range 300 {
			fmt.Fprintf(&src, "          field%d:\n            description: A field of the argoproj.io resource.\n            type: string\n", j)
		}
	}
	for i := range 3 {
		fmt.Fprintf(&src, "---\napiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: app%d\nspec:\n  source:\n"+
			"    repoURL: https://charts.example.com\n    chart: c%d\n    targetRevision: 1.%d.0\n", i, i, i)
	}
	data := []byte(src.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if deps := (&helmManager{}).manifests("install.yaml", data); len(deps) != 3 {
			b.Fatalf("deps = %d", len(deps))
		}
	}
}

// TestPrefiltersKeepEveryResource checks that no resource document dispatches on is dropped by
// a prefilter: its apiVersion passes hasCRDGroup (file) and mayHaveCRD (document).
func TestPrefiltersKeepEveryResource(t *testing.T) {
	for k := range crdResources {
		group, kind, _ := strings.Cut(k, " ")
		doc := []byte("apiVersion: " + group + "/v1\nkind: " + kind + "\n")
		if !hasCRDGroup(doc) || !mayHaveCRD(doc) {
			t.Errorf("%s: hasCRDGroup %v, mayHaveCRD %v", k, hasCRDGroup(doc), mayHaveCRD(doc))
		}
		if bare := []byte("apiVersion: " + group + "\nkind: " + kind + "\n"); !mayHaveCRD(bare) {
			t.Errorf("%s without a version: mayHaveCRD false", k)
		}
	}
	if mayHaveCRD([]byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nspec: {group: argoproj.io}\n")) {
		t.Error("a CRD of an extracted group: want skipped")
	}
}
