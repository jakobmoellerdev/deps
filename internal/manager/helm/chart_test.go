package helm

import (
	"cmp"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// wantChartDep is the expected outcome of one extracted dep; span is the text under RefSpan.
type wantChartDep struct {
	name, source, version, span, skip string
	line                              int
}

func checkChartDeps(t *testing.T, src string, got []dep.Dep, want []wantChartDep, depType string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		d := got[i]
		if d.Manager != Name || d.Datasource != Datasource || d.Versioning != Versioning || d.DepType != depType {
			t.Errorf("dep %d: manager/datasource/versioning/type = %s/%s/%s/%s", i, d.Manager, d.Datasource, d.Versioning, d.DepType)
		}
		if d.Name != w.name || d.Source != w.source || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("dep %d = {name %q source %q skip %q line %d}, want %+v", i, d.Name, d.Source, d.Skip, d.Line, w)
		}
		if w.skip == "" {
			if d.Version != w.version || src[d.RefSpan.Start:d.RefSpan.End] != w.span {
				t.Errorf("dep %d: version %q span %q, want %q %q", i, d.Version, src[d.RefSpan.Start:d.RefSpan.End], w.version, w.span)
			}
		}
	}
}

func TestChartDeps(t *testing.T) {
	const src = `apiVersion: v2
name: app
version: 0.1.0 # the chart's own version is not a dep
dependencies:
  - name: cert-manager
    version: v1.14.0
    repository: https://charts.jetstack.io/
  - name: redis
    version: "18.0.0"
    repository: oci://registry-1.docker.io/bitnamicharts
    alias: cache
  - name: ranged
    version: ~1.2.0
    repository: https://example.com/charts
  - name: partial
    version: "1.2"
    repository: https://example.com/charts
  - name: local
    version: 1.0.0
    repository: file://../local
  - name: subchart
    version: 1.0.0
  - name: aliased
    version: 1.0.0
    repository: "@stable"
  - name: aliased2
    version: 1.0.0
    repository: alias:stable
  - name: odd
    version: 1.0.0
    repository: stable
  - name: noversion
    repository: https://example.com/charts
  - name: escaped
    version: "1.0.\u0030"
    repository: https://example.com/charts
  - {name: "", version: 1.0.0}
  - just-a-string
`
	m := New()
	if !m.Match("charts/app/Chart.yaml") || !m.Match("Chart.yaml") {
		t.Fatal("Chart.yaml not matched")
	}
	got, err := m.Extract("charts/app/Chart.yaml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	checkChartDeps(t, src, got, []wantChartDep{
		{name: "cert-manager", source: "https://charts.jetstack.io", version: "v1.14.0", span: "v1.14.0", line: 6},
		{name: "redis", source: "oci://registry-1.docker.io/bitnamicharts", version: "18.0.0", span: "18.0.0", line: 9},
		{name: "ranged", source: "https://example.com/charts", skip: "version range", line: 13},
		{name: "partial", source: "https://example.com/charts", skip: "version range", line: 16},
		{name: "local", skip: "local chart", line: 18},
		{name: "subchart", skip: "local chart", line: 21},
		{name: "aliased", skip: "repository alias", line: 23},
		{name: "aliased2", skip: "repository alias", line: 26},
		{name: "odd", skip: "unknown chart repository stable", line: 29},
		{name: "noversion", source: "https://example.com/charts", skip: "no version", line: 32},
		{name: "escaped", source: "https://example.com/charts", skip: "cannot locate value", line: 35},
	}, TypeChart)
}

func TestChartDepsEdgeCases(t *testing.T) {
	m := New()
	for _, src := range []string{"", "apiVersion: v2\n", "dependencies: {}\n", "- a\n"} {
		got, err := m.Extract("Chart.yaml", []byte(src), manager.Env{})
		if err != nil || len(got) != 0 {
			t.Errorf("%q: %v, %v", src, got, err)
		}
	}
	if _, err := m.Extract("Chart.yaml", []byte("dependencies: [\n"), manager.Env{}); err == nil {
		t.Error("broken Chart.yaml accepted")
	}
}

func TestVersionSkip(t *testing.T) {
	for v, want := range map[string]string{
		"1.2.3": "", "v1.2.3": "", "1.2.3-rc.1+build.5": "", "0.0.0": "",
		"": "no version", "{{ .Values.v }}": "template value",
		"1.2": "version range", "^1.2.3": "version range", "~1.2.0": "version range", ">=1.0.0 <2": "version range",
		"1.2.x": "version range", "*": "version range", "01.2.3": "version range", "1.2.3 || 2.0.0": "version range",
	} {
		if got := versionSkip(v); got != want {
			t.Errorf("versionSkip(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestEdits(t *testing.T) {
	src := "version: v1.14.0\n"
	d := dep.Dep{Version: "v1.14.0", RefSpan: dep.Span{Start: 9, End: 16}}
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "v1.15.1"})
	if err != nil || len(edits) != 1 || edits[0].Old != "v1.14.0" || edits[0].New != "v1.15.1" || src[edits[0].Start:edits[0].End] != "v1.14.0" {
		t.Fatalf("Edits = %+v, %v", edits, err)
	}
}

func TestRename(t *testing.T) {
	const chart = `dependencies:
  - name: cert-manager
    version: v1.14.0
    repository: https://charts.jetstack.io/
  - name: redis
    version: "18.0.0"
    repository: "oci://registry-1.docker.io/bitnamicharts"
`
	const helmfile = `repositories:
  - name: bitnami
    url: https://charts.bitnami.com/bitnami
releases:
  - name: cache
    chart: bitnami/redis
    version: 18.0.0
  - name: db
    chart: oci://registry-1.docker.io/bitnamicharts/postgresql
    version: 15.0.0
`
	const argo = `apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  sources:
    - repoURL: registry-1.docker.io/bitnamicharts
      chart: redis
      targetRevision: 18.0.0
    - repoURL: https://charts.jetstack.io
      chart: cert-manager
      targetRevision: v1.14.0
    - repoURL: https://charts.example.com
      chart: '{{ .chart }}'
      targetRevision: 1.0.0
`
	const flux = `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: podinfo
spec:
  url: https://stefanprodan.github.io/podinfo
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
spec:
  chart:
    spec:
      chart: podinfo
      version: 6.5.0
      sourceRef:
        kind: HelmRepository
        name: podinfo
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: cilium
spec:
  url: oci://ghcr.io/home-operations/charts-mirror/cilium
  ref:
    tag: 1.18.6
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
`
	const k0s = `apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
spec:
  extensions:
    helm:
      repositories:
        - name: stable
          url: https://charts.example.com
      charts:
        - name: a
          chartname: stable/alpha
          version: 1.2.3
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
spec:
  chartName: beta
  version: 2.0.0
  repository:
    url: https://beta.example.com
`
	for _, c := range []struct {
		name, file, src, old, to, version string
		repl                              []string // old, new text pairs the update writes
		wantName, wantSource, wantErr     string
	}{
		{name: "chart name", file: "Chart.yaml", src: chart, old: "cert-manager", to: "cm", version: "v1.15.0",
			repl: []string{"name: cert-manager", "name: cm", "v1.14.0", "v1.15.0"}, wantName: "cm", wantSource: "https://charts.jetstack.io"},
		{name: "chart same repository", file: "Chart.yaml", src: chart, old: "cert-manager", to: "https://charts.jetstack.io/cm",
			repl: []string{"name: cert-manager", "name: cm"}, wantName: "cm", wantSource: "https://charts.jetstack.io"},
		{name: "chart OCI move", file: "Chart.yaml", src: chart, old: "redis", to: "oci://ghcr.io/valkey-io/charts/valkey", version: "8.0.0",
			repl:     []string{"name: redis", "name: valkey", "oci://registry-1.docker.io/bitnamicharts", "oci://ghcr.io/valkey-io/charts", "18.0.0", "8.0.0"},
			wantName: "valkey", wantSource: "oci://ghcr.io/valkey-io/charts"},
		{name: "chart OCI move without scheme", file: "Chart.yaml", src: chart, old: "redis", to: "ghcr.io/valkey-io/charts/valkey",
			repl:     []string{"name: redis", "name: valkey", "oci://registry-1.docker.io/bitnamicharts", "oci://ghcr.io/valkey-io/charts"},
			wantName: "valkey", wantSource: "oci://ghcr.io/valkey-io/charts"},
		{name: "chart move to classic repository", file: "Chart.yaml", src: chart, old: "redis", to: "https://charts.example.com/valkey",
			repl:     []string{"name: redis", "name: valkey", "oci://registry-1.docker.io/bitnamicharts", "https://charts.example.com"},
			wantName: "valkey", wantSource: "https://charts.example.com"},
		{name: "invalid chart", file: "Chart.yaml", src: chart, old: "redis", to: "Valkey Chart", wantErr: "invalid chart name"},
		{name: "empty chart", file: "Chart.yaml", src: chart, old: "redis", to: "oci://ghcr.io/x/", wantErr: "invalid chart name"},
		{name: "invalid repository", file: "Chart.yaml", src: chart, old: "redis", to: "ftp://example.com/valkey", wantErr: "invalid chart repository"},
		{name: "helmfile alias name", file: "helmfile.yaml", src: helmfile, old: "redis", to: "valkey",
			repl: []string{"bitnami/redis", "bitnami/valkey"}, wantName: "valkey", wantSource: "https://charts.bitnami.com/bitnami"},
		{name: "helmfile alias move", file: "helmfile.yaml", src: helmfile, old: "redis", to: "oci://ghcr.io/x/valkey", wantErr: "only its name can change"},
		{name: "helmfile OCI name", file: "helmfile.yaml", src: helmfile, old: "postgresql", to: "pg",
			repl: []string{"bitnamicharts/postgresql", "bitnamicharts/pg"}, wantName: "pg", wantSource: "oci://registry-1.docker.io/bitnamicharts"},
		{name: "helmfile OCI move", file: "helmfile.yaml", src: helmfile, old: "postgresql", to: "oci://ghcr.io/x/pg", version: "16.0.0",
			repl: []string{"oci://registry-1.docker.io/bitnamicharts/postgresql", "oci://ghcr.io/x/pg", "15.0.0", "16.0.0"}, wantName: "pg", wantSource: "oci://ghcr.io/x"},
		{name: "helmfile OCI to classic", file: "helmfile.yaml", src: helmfile, old: "postgresql", to: "https://charts.example.com/pg", wantErr: "no OCI repository"},
		{name: "argocd OCI move", file: "app.yaml", src: argo, old: "redis", to: "oci://ghcr.io/valkey-io/charts/valkey",
			repl:     []string{"registry-1.docker.io/bitnamicharts", "ghcr.io/valkey-io/charts", "chart: redis", "chart: valkey"},
			wantName: "valkey", wantSource: "oci://ghcr.io/valkey-io/charts"},
		{name: "argocd name", file: "app.yaml", src: argo, old: "cert-manager", to: "cm",
			repl: []string{"chart: cert-manager", "chart: cm"}, wantName: "cm", wantSource: "https://charts.jetstack.io"},
		{name: "argocd template", file: "app.yaml", src: argo, old: "{{ .chart }}", to: "x", version: "1.0.0", wantErr: "cannot locate the name"},
		{name: "flux HelmRelease name", file: "flux.yaml", src: flux, old: "podinfo", to: "podinfo2", version: "6.6.0",
			repl: []string{"chart: podinfo", "chart: podinfo2", "6.5.0", "6.6.0"}, wantName: "podinfo2", wantSource: "https://stefanprodan.github.io/podinfo"},
		{name: "flux HelmRelease move", file: "flux.yaml", src: flux, old: "podinfo", to: "oci://ghcr.io/stefanprodan/charts/podinfo", wantErr: "only its name can change"},
		{name: "flux OCIRepository move", file: "flux.yaml", src: flux, old: "cilium", to: "quay.io/cilium/charts/cilium",
			repl: []string{"ghcr.io/home-operations/charts-mirror/cilium", "quay.io/cilium/charts/cilium"}, wantName: "cilium", wantSource: "oci://quay.io/cilium/charts"},
		{name: "k0s alias name", file: "k0s.yaml", src: k0s, old: "alpha", to: "alpha2",
			repl: []string{"stable/alpha", "stable/alpha2"}, wantName: "alpha2", wantSource: "https://charts.example.com"},
		{name: "k0s inline repository move", file: "k0s.yaml", src: k0s, old: "beta", to: "https://charts.beta.io/beta",
			repl: []string{"https://beta.example.com", "https://charts.beta.io"}, wantName: "beta", wantSource: "https://charts.beta.io"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			d, ok := findDep(linkedDeps(t, m, map[string]string{c.file: c.src}), c.old, "")
			if !ok {
				t.Fatalf("no dep %s", c.old)
			}
			version := cmp.Or(c.version, d.Version)
			nd, err := m.(manager.Renamer).Rename(d, c.to)
			var edits []patch.Edit
			if err == nil {
				edits, err = m.Edits(dep.Update{Dep: d, Type: dep.Replacement, NewName: c.to, NewVersion: version})
			}
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if nd.Name != c.wantName || nd.Source != c.wantSource {
				t.Errorf("Rename = %s %s, want %s %s", nd.Name, nd.Source, c.wantName, c.wantSource)
			}
			out, err := patch.Apply([]byte(c.src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.NewReplacer(c.repl...).Replace(c.src); string(out) != want {
				t.Fatalf("file =\n%s\nwant\n%s", out, want)
			}
			// The written file holds the dep Rename returned.
			if got, ok := findDep(linkedDeps(t, New(), map[string]string{c.file: string(out)}), nd.Name, nd.Source); !ok || got.Version != version {
				t.Errorf("renamed dep %s %s %s not extracted: %+v", nd.Name, nd.Source, version, got)
			}
		})
	}
}

// findDep returns the first dep of files named name (and from source, if set).
func findDep(files [][]dep.Dep, name, source string) (dep.Dep, bool) {
	for _, ds := range files {
		for _, d := range ds {
			if d.Name == name && (source == "" || d.Source == source) {
				return d, true
			}
		}
	}
	return dep.Dep{}, false
}
