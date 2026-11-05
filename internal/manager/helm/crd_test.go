package helm

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// linked extracts files in path order with one manager, links them like the engine and
// returns every dep rendered as "<file>:<line> <depType> <name> <source> <version> <skip>".
// It checks that the span of every updatable dep holds its version.
func linked(t *testing.T, m manager.Manager, files map[string]string) []string {
	t.Helper()
	var out []string
	for _, ds := range linkedDeps(t, m, files) {
		for _, d := range ds {
			if d.Manager != Name || d.Datasource != Datasource || d.Versioning != Versioning {
				t.Errorf("%+v: wrong manager/datasource/versioning", d)
			}
			if d.Skip == "" {
				if got := files[d.File][d.RefSpan.Start:d.RefSpan.End]; got != d.Version {
					t.Errorf("%s:%d: span text %q, want %q", d.File, d.Line, got, d.Version)
				}
			}
			out = append(out, fmt.Sprintf("%s:%d %s %s %s %s %s", d.File, d.Line, d.DepType, d.Name, d.Source, d.Version, d.Skip))
		}
	}
	return out
}

// linkedDeps extracts files in path order with one manager and links them like the engine.
// It checks that every NamePart holds its text.
func linkedDeps(t *testing.T, m manager.Manager, files map[string]string) [][]dep.Dep {
	t.Helper()
	paths := slices.Sorted(maps.Keys(files))
	var per [][]dep.Dep
	for _, p := range paths {
		if !m.Match(p) {
			t.Fatalf("%s not matched", p)
		}
		ds, err := m.Extract(p, []byte(files[p]), manager.Env{})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for _, d := range ds {
			for _, np := range d.NameParts {
				if got := files[p][np.Span.Start:np.Span.End]; got != np.Text {
					t.Errorf("%s:%d: name part %q at %v, want %q", p, d.Line, got, np.Span, np.Text)
				}
			}
		}
		per = append(per, ds)
	}
	m.(manager.Linker).Link(per)
	return per
}

func check(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("deps:\n%s\nwant:\n%s", lines(got), lines(want))
	}
}

func lines(s []string) string {
	var b []byte
	for _, l := range s {
		b = fmt.Appendf(b, "  %q\n", l)
	}
	return string(b)
}

func TestK0s(t *testing.T) {
	files := map[string]string{
		"k0s/k0s.yaml": `apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
spec:
  extensions:
    helm:
      repositories:
        - name: stable
          url: https://charts.example.com/stable/
        - name: dup
          url: https://own.example.com
        - {name: incomplete}
      charts:
        - name: a
          chartname: stable/alpha
          version: "1.2.3"
        - name: b
          chartname: oci://registry.example.com/charts/beta
          version: 2.0.0
        - name: c
          chartname: dup/gamma
          version: 3.0.0
        - name: d
          chartname: /var/lib/k0s/charts/delta.tgz
          version: 0.1.0
        - name: e
          chartname: missing/epsilon
          version: 1.0.0
        - name: f
          chartname: stable/zeta
          version: ~1.2.0
        - name: g
          chartname: stable/sub/eta
          version: 1.0.0
        - name: h
          version: 1.0.0
        - name: i
          chartname: oci://nopath
          version: 1.0.0
`,
		"k0s/manifests/charts.yaml": `apiVersion: v1
kind: Namespace
metadata:
  name: x
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
metadata:
  name: cert-manager
spec:
  chartName: jetstack/cert-manager
  version: "v1.21.3"
  repository:
    url: https://charts.jetstack.io
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
spec:
  chartName: stable/alpha
  version: 1.0.0
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
spec:
  chartName: dup/gamma
  version: 3.0.0
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
spec:
  chartName: "{{ .Chart }}"
  version: 1.0.0
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
spec:
  chartName: stable/alpha
`,
		"other/k0s.yaml": `apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
spec:
  extensions:
    helm:
      repositories:
        - name: dup
          url: https://other.example.com
`,
		"k0sctl.yaml": `apiVersion: k0sctl.k0sproject.io/v1beta1
kind: Cluster
spec:
  k0s:
    config:
      spec:
        extensions:
          helm:
            charts:
              - chartname: stable/theta
                version: 0.9.0
`,
	}
	check(t, linked(t, New(), files), []string{
		"k0s/k0s.yaml:15 k0s alpha https://charts.example.com/stable 1.2.3 ",
		"k0s/k0s.yaml:18 k0s beta oci://registry.example.com/charts 2.0.0 ",
		"k0s/k0s.yaml:21 k0s gamma https://own.example.com 3.0.0 ",
		"k0s/k0s.yaml:24 k0s /var/lib/k0s/charts/delta.tgz  0.1.0 local chart",
		"k0s/k0s.yaml:27 k0s epsilon  1.0.0 unknown chart repository missing",
		"k0s/k0s.yaml:30 k0s zeta  ~1.2.0 version range",
		"k0s/k0s.yaml:33 k0s stable/sub/eta  1.0.0 invalid chart reference",
		"k0s/k0s.yaml:38 k0s oci://nopath  1.0.0 invalid chart reference",
		"k0s/manifests/charts.yaml:12 k0s cert-manager https://charts.jetstack.io v1.21.3 ",
		"k0s/manifests/charts.yaml:20 k0s alpha https://charts.example.com/stable 1.0.0 ",
		"k0s/manifests/charts.yaml:26 k0s gamma  3.0.0 ambiguous chart repository dup",
		"k0s/manifests/charts.yaml:32 k0s {{ .Chart }}  1.0.0 template value",
		"k0s/manifests/charts.yaml:37 k0s alpha   no version",
		"k0sctl.yaml:11 k0s theta https://charts.example.com/stable 0.9.0 ",
	})
}

func TestFlux(t *testing.T) {
	files := map[string]string{
		"clusters/sources.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: podinfo
  namespace: flux-system
spec:
  url: https://stefanprodan.github.io/podinfo
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: bitnami
  namespace: apps
spec:
  type: oci
  url: registry-1.docker.io/bitnamicharts
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: kustomized
spec:
  url: https://kustomized.example.com
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: twice
  namespace: a
spec:
  url: https://a.example.com
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: twice
  namespace: b
spec:
  url: https://b.example.com
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: nourl
`,
		"clusters/releases.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
  namespace: apps
spec:
  chart:
    spec:
      chart: podinfo
      version: "6.5.0"
      sourceRef:
        kind: HelmRepository
        name: podinfo
        namespace: flux-system
---
apiVersion: helm.toolkit.fluxcd.io/v2beta2
kind: HelmRelease
metadata:
  name: redis
  namespace: apps
spec:
  chart:
    spec:
      chart: redis
      version: 18.0.0
      sourceRef: {kind: HelmRepository, name: bitnami}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: k
  namespace: apps
spec:
  chart:
    spec:
      chart: k
      version: 1.0.0
      sourceRef: {kind: HelmRepository, name: kustomized}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  chart:
    spec:
      chart: t
      version: 1.0.0
      sourceRef: {kind: HelmRepository, name: twice}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  chart:
    spec:
      chart: ./charts/local
      sourceRef: {kind: GitRepository, name: repo}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  chart:
    spec:
      chart: nosource
      version: 1.0.0
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  namespace: apps
spec:
  chart:
    spec:
      chart: unknown
      version: 1.0.0
      sourceRef: {kind: HelmRepository, name: nope}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  chart:
    spec:
      chart: latest
      sourceRef: {kind: HelmRepository, name: podinfo, namespace: flux-system}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  namespace: apps
spec:
  chartRef:
    kind: OCIRepository
    name: podinfo-oci
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  chartRef: {kind: HelmChart, name: x}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  chart:
    spec: {}
`,
		"clusters/oci.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: podinfo-oci
  namespace: apps
spec:
  url: oci://ghcr.io/stefanprodan/charts/podinfo
  ref:
    tag: 6.4.0
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: manifests
spec:
  url: oci://ghcr.io/org/manifests
  ref:
    tag: 1.0.0
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: by-media-type
spec:
  url: oci://ghcr.io/org/charts/app
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
  ref:
    semver: ">=1.0.0"
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
spec:
  url: oci://ghcr.io/org/charts/pinned
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
  ref:
    digest: sha256:0000
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
spec:
  url: https://not-oci.example.com/x
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
spec:
  url: oci://
`,
	}
	check(t, linked(t, New(), files), []string{
		"clusters/oci.yaml:9 flux podinfo oci://ghcr.io/stefanprodan/charts 6.4.0 ",
		"clusters/oci.yaml:25 flux app oci://ghcr.io/org/charts  version range",
		"clusters/oci.yaml:34 flux pinned oci://ghcr.io/org/charts  pinned by digest",
		"clusters/releases.yaml:10 flux podinfo https://stefanprodan.github.io/podinfo 6.5.0 ",
		"clusters/releases.yaml:25 flux redis oci://registry-1.docker.io/bitnamicharts 18.0.0 ",
		"clusters/releases.yaml:37 flux k https://kustomized.example.com 1.0.0 ",
		"clusters/releases.yaml:46 flux t  1.0.0 ambiguous HelmRepository twice",
		"clusters/releases.yaml:54 flux ./charts/local   chart from GitRepository source",
		"clusters/releases.yaml:63 flux nosource  1.0.0 no sourceRef",
		"clusters/releases.yaml:73 flux unknown  1.0.0 unknown HelmRepository apps/nope",
		"clusters/releases.yaml:81 flux latest   no version",
	})
}

func TestArgoCD(t *testing.T) {
	files := map[string]string{
		"argo/apps.yaml": `apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  source:
    repoURL: https://charts.bitnami.com/bitnami/
    chart: nginx
    targetRevision: 15.0.0
---
apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  sources:
    - repoURL: registry-1.docker.io/bitnamicharts
      chart: redis
      targetRevision: '18.1.0'
    - repoURL: oci://ghcr.io/org/charts
      chart: app
      targetRevision: 1.x
    - repoURL: https://github.com/org/values.git
      targetRevision: main
      ref: values
    - chart: norepo
      targetRevision: 1.0.0
---
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
spec:
  template:
    spec:
      source:
        repoURL: https://charts.example.com
        chart: '{{ .chart }}'
        targetRevision: 1.0.0
---
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
spec:
  template:
    spec:
      source:
        repoURL: https://charts.example.com
        chart: fixed
        targetRevision: 2.0.0
`,
	}
	check(t, linked(t, New(), files), []string{
		"argo/apps.yaml:7 argocd nginx https://charts.bitnami.com/bitnami 15.0.0 ",
		"argo/apps.yaml:15 argocd redis oci://registry-1.docker.io/bitnamicharts 18.1.0 ",
		"argo/apps.yaml:18 argocd app oci://ghcr.io/org/charts 1.x version range",
		"argo/apps.yaml:23 argocd norepo  1.0.0 no repoURL",
		"argo/apps.yaml:33 argocd {{ .chart }} https://charts.example.com 1.0.0 template value",
		"argo/apps.yaml:43 argocd fixed https://charts.example.com 2.0.0 ",
	})
}

func TestManifestsSkipsUnrelatedAndUnparsable(t *testing.T) {
	m := New()
	for name, src := range map[string]string{
		"deploy.yaml": "apiVersion: apps/v1\nkind: Deployment\n",
		"tmpl.yaml":   "apiVersion: argoproj.io/v1alpha1\nkind: Application\nspec: {{ .Values }}\n  bad: [\n",
		"list.yaml":   "- apiVersion: argoproj.io/v1alpha1\n",
	} {
		ds, err := m.Extract(name, []byte(src), manager.Env{})
		if err != nil || ds != nil {
			t.Errorf("%s: deps = %+v, err = %v", name, ds, err)
		}
	}
}

// Link forgets the definitions of a run: a second run does not see the first run's aliases.
func TestLinkResets(t *testing.T) {
	m := New()
	cfg := "apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\nspec:\n  extensions:\n    helm:\n      repositories:\n        - {name: r, url: https://r.example.com}\n"
	chart := "apiVersion: helm.k0sproject.io/v1beta1\nkind: Chart\nspec:\n  chartName: r/c\n  version: 1.0.0\n"
	check(t, linked(t, m, map[string]string{"a.yaml": cfg, "b.yaml": chart}), []string{"b.yaml:5 k0s c https://r.example.com 1.0.0 "})
	check(t, linked(t, m, map[string]string{"b.yaml": chart}), []string{"b.yaml:5 k0s c  1.0.0 unknown chart repository r"})
}

func TestSveltos(t *testing.T) {
	files := map[string]string{
		"sveltos/profiles.yaml": `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  group: config.projectsveltos.io
---
apiVersion: config.projectsveltos.io/v1beta1
kind: ClusterProfile
spec:
  helmCharts:
    - repositoryURL: https://kyverno.github.io/kyverno/
      repositoryName: kyverno
      chartName: kyverno/kyverno
      chartVersion: v3.0.1
    - repositoryURL: oci://registry-1.docker.io/bitnamicharts/vault
      repositoryName: oci-vault
      chartName: oci://registry-1.docker.io/bitnamicharts/vault
      chartVersion: 0.7.2
    - repositoryURL: https://charts.example.com
      repositoryName: ex
      chartName: '{{ .chart }}'
      chartVersion: 1.0.0
    - repositoryName: none
      chartName: none/app
      chartVersion: 1.0.0
---
apiVersion: config.projectsveltos.io/v1beta1
kind: Profile
spec:
  helmCharts:
  - {repositoryURL: "https://prometheus-community.github.io/helm-charts", repositoryName: prom, chartName: prometheus, chartVersion: "25.8.0"}
---
apiVersion: config.projectsveltos.io/v1beta1
kind: ClusterPromotion
spec:
  profileSpec:
    helmCharts:
    - repositoryURL: https://charts.jetstack.io
      repositoryName: jetstack
      chartName: jetstack/cert-manager
      chartVersion: v1.14.0
---
apiVersion: lib.projectsveltos.io/v1beta1
kind: EventTrigger
spec:
  helmCharts:
  - repositoryURL: https://grafana.github.io/helm-charts
    repositoryName: grafana
    chartName: grafana/loki
    chartVersion: 6.0.0
`,
	}
	check(t, linked(t, New(), files), []string{
		"sveltos/profiles.yaml:13 sveltos kyverno https://kyverno.github.io/kyverno v3.0.1 ",
		"sveltos/profiles.yaml:17 sveltos vault oci://registry-1.docker.io/bitnamicharts 0.7.2 ",
		"sveltos/profiles.yaml:21 sveltos {{ .chart }} https://charts.example.com 1.0.0 template value",
		"sveltos/profiles.yaml:24 sveltos app  1.0.0 no repositoryURL",
		"sveltos/profiles.yaml:30 sveltos prometheus https://prometheus-community.github.io/helm-charts 25.8.0 ",
		"sveltos/profiles.yaml:40 sveltos cert-manager https://charts.jetstack.io v1.14.0 ",
		"sveltos/profiles.yaml:49 sveltos loki https://grafana.github.io/helm-charts 6.0.0 ",
	})
	ds := linkedDeps(t, New(), files)[0]
	if n := len(ds[0].NameParts); n != 2 {
		t.Errorf("kyverno: %d name parts, want chart and repository", n)
	}
	if ds[1].NameParts != nil {
		t.Errorf("OCI chart: name parts %+v, want none", ds[1].NameParts)
	}
}
