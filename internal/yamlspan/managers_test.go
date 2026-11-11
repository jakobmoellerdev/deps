package yamlspan_test

import (
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/crossplane"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/droneci"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/glasskube"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// discardLogs drops the log lines the managers write for the files they cannot parse, which
// a Helm template is.
func discardLogs(tb testing.TB) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	tb.Cleanup(func() { slog.SetDefault(old) })
}

// bundle is an install manifest as Flux, Argo CD and Crossplane ship them: large
// CustomResourceDefinitions no manager reads, HelmReleases every one of the docker and the
// helm manager reads (their values hold an image), Deployments, Crossplane packages and
// Glasskube packages.
func bundle() []byte {
	var b strings.Builder
	for i := range 10 {
		fmt.Fprintf(&b, "---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: x%d.argoproj.io\n"+
			"spec:\n  group: argoproj.io\n  versions:\n  - name: v1alpha1\n    schema:\n      openAPIV3Schema:\n        properties:\n", i)
		for j := range 200 {
			fmt.Fprintf(&b, "          field%d:\n            description: A field of the resource, which may name an image.\n            type: string\n", j)
		}
	}
	for i := range 6 {
		fmt.Fprintf(&b, "---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: r%d\n  namespace: flux\n"+
			"spec:\n  chart:\n    spec:\n      chart: c%d\n      version: 1.%d.0\n      sourceRef:\n        kind: HelmRepository\n        name: charts\n"+
			"  values:\n    image:\n      repository: ghcr.io/example/app%d\n      tag: v1.%d.0\n", i, i, i, i, i)
	}
	fmt.Fprint(&b, "---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata:\n  name: charts\n  namespace: flux\n"+
		"spec:\n  url: https://charts.example.com\n")
	for i := range 4 {
		fmt.Fprintf(&b, "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d%d\nspec:\n  template:\n    spec:\n      containers:\n"+
			"      - name: app\n        image: quay.io/example/controller:v2.%d.0\n", i, i)
	}
	for i := range 3 {
		fmt.Fprintf(&b, "---\napiVersion: pkg.crossplane.io/v1\nkind: Provider\nmetadata:\n  name: p%d\nspec:\n"+
			"  package: xpkg.upbound.io/example/provider-%d:v0.4.0\n", i, i)
	}
	for i := range 3 {
		fmt.Fprintf(&b, "---\napiVersion: packages.glasskube.dev/v1alpha1\nkind: ClusterPackage\nmetadata:\n  name: g%d\nspec:\n"+
			"  packageInfo:\n    name: pkg%d\n    version: 1.2.%d\n", i, i, i)
	}
	return []byte(b.String())
}

// templates are the templates of a Helm chart, which are no valid YAML: the docker manager
// reads their image lines one by one, and the helm manager reads the ones holding a custom
// resource of a group it extracts.
func templates() (paths []string, srcs [][]byte) {
	for i := range 6 {
		var b strings.Builder
		fmt.Fprintf(&b, "{{- if .Values.app%d.enabled }}\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n"+
			"  name: {{ include \"chart.fullname\" . }}-%d\nspec:\n  replicas: {{ .Values.replicas }}\n  template:\n    spec:\n      containers:\n"+
			"      - name: app\n        image: \"{{ .Values.image.repository }}:{{ .Values.image.tag }}\"\n"+
			"      - name: sidecar\n        image: ghcr.io/example/sidecar:v1.%d.0\n{{- end }}\n", i, i, i)
		paths, srcs = append(paths, fmt.Sprintf("charts/app/templates/deployment%d.yaml", i)), append(srcs, []byte(b.String()))
	}
	for i := range 6 {
		var b strings.Builder
		fmt.Fprintf(&b, "{{- range .Values.releases }}\n---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n"+
			"  name: {{ .name }}-%d\nspec:\n  chart:\n    spec:\n      chart: {{ .chart }}\n      version: {{ .version }}\n"+
			"  values:\n    image:\n      repository: {{ .image }}\n      tag: v1.%d.0\n{{- end }}\n", i, i)
		paths, srcs = append(paths, fmt.Sprintf("charts/platform/templates/release%d.yaml", i)), append(srcs, []byte(b.String()))
	}
	return paths, srcs
}

// yamlManagers returns the managers reading every YAML file, in the order the engine runs
// them over one file.
func yamlManagers() []manager.Manager {
	return []manager.Manager{docker.New(), helm.New(), crossplane.New(), glasskube.New(), droneci.New()}
}

// extractAll extracts file with every manager, sharing one parse of it as the engine does
// when share is set, and a parse per manager else.
func extractAll(tb testing.TB, ms []manager.Manager, file string, src []byte, share bool) [][]dep.Dep {
	shared := &yamlspan.Parsed{}
	out := make([][]dep.Dep, len(ms))
	for i, m := range ms {
		if !m.Match(file) {
			continue
		}
		p := shared
		if !share {
			p = &yamlspan.Parsed{}
		}
		deps, err := m.Extract(file, src, manager.Env{YAML: p})
		if err != nil && tb != nil {
			tb.Fatalf("%s: %s: %v", file, m.Name(), err)
		}
		out[i] = deps
	}
	return out
}

// TestSharedDocumentsKeepDeps checks that the managers of one file extract the same deps,
// with the same spans, from one shared parse as from a parse of their own.
func TestSharedDocumentsKeepDeps(t *testing.T) {
	discardLogs(t)
	paths, srcs := templates()
	paths, srcs = append(paths, "clusters/bundle.yaml"), append(srcs, bundle())
	paths, srcs = append(paths, ".drone.yml"), append(srcs, []byte(
		"kind: pipeline\nsteps:\n- name: build\n  image: golang:1.27\n---\nkind: secret\ndata: {a: b}\n"))
	for i, p := range paths {
		own := extractAll(t, yamlManagers(), p, srcs[i], false)
		shared := extractAll(t, yamlManagers(), p, srcs[i], true)
		if !reflect.DeepEqual(shared, own) {
			t.Errorf("%s:\nshared parse = %+v\nown parse    = %+v", p, shared, own)
		}
	}
	// Every manager of the bundle must extract from it: the deps of a corpus none of them
	// reads are equal for a trivial reason.
	for i, deps := range extractAll(t, yamlManagers(), "clusters/bundle.yaml", bundle(), true) {
		if len(deps) == 0 && i < 4 {
			t.Errorf("%s: no deps of the bundle", yamlManagers()[i].Name())
		}
	}
}

// BenchmarkBundleManagers runs the managers of a YAML file over an install manifest, as the
// engine does: with one parse of the file for all of them.
func BenchmarkBundleManagers(b *testing.B) {
	src := bundle()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		extractAll(nil, yamlManagers(), "clusters/bundle.yaml", src, true)
	}
}

// BenchmarkTemplateManagers runs them over the templates of a Helm chart, which no manager
// can parse.
func BenchmarkTemplateManagers(b *testing.B) {
	discardLogs(b)
	paths, srcs := templates()
	var n int
	for _, src := range srcs {
		n += len(src)
	}
	b.SetBytes(int64(n))
	b.ReportAllocs()
	for b.Loop() {
		ms := yamlManagers()
		for i, p := range paths {
			extractAll(nil, ms, p, srcs[i], true)
		}
	}
}

// BenchmarkManySmallDocuments runs the docker manager over a manifest of many small
// documents, every one of which it reads: the shape a parse per document would cost most.
func BenchmarkManySmallDocuments(b *testing.B) {
	var s strings.Builder
	for i := range 200 {
		fmt.Fprintf(&s, "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d%d\nspec:\n  template:\n    spec:\n"+
			"      containers:\n      - name: app\n        image: quay.io/example/controller:v1.%d.0\n", i, i)
	}
	src := []byte(s.String())
	m := docker.New()
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		if deps, _ := m.Extract("k8s/all.yaml", src, manager.Env{YAML: &yamlspan.Parsed{}}); len(deps) != 200 {
			b.Fatalf("deps = %d", len(deps))
		}
	}
}
