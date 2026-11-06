package helm

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestHelmfileDeps(t *testing.T) {
	const src = `repositories:
  - name: jetstack
    url: https://charts.jetstack.io/
  - name: bitnami
    url: registry-1.docker.io/bitnamicharts
    oci: true
  - name: ghcr
    url: oci://ghcr.io/org/charts
    oci: true
  - name: incomplete
releases:
  - name: cert-manager
    chart: jetstack/cert-manager
    version: v1.14.0
  - name: redis
    chart: bitnami/redis
    version: '18.0.0'
---
releases:
  - name: direct
    chart: oci://ghcr.io/org/charts/app
    version: 2.0.0
  - name: via-ghcr
    chart: ghcr/other
    version: 3.0.0
  - name: local
    chart: ./charts/local
  - name: bare
    chart: mychart
  - name: unknown
    chart: stable/thing
    version: 1.0.0
  - name: deep
    chart: jetstack/a/b
  - name: broken-oci
    chart: oci://nohost
  - name: ranged
    chart: jetstack/x
    version: ">=1.0.0"
  - name: nochart
`
	m := New()
	for _, p := range []string{"helmfile.yaml", "env/helmfile.yml", "helmfile.d/10-apps.yaml", "helmfile.yaml.gotmpl", "helmfile.d/a.yaml.gotmpl"} {
		if !m.Match(p) {
			t.Errorf("%s not matched", p)
		}
	}
	if m.Match("helmfile.lock") || m.Match("README.md") {
		t.Error("non-YAML matched")
	}
	got, err := m.Extract("helmfile.yaml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	checkChartDeps(t, src, got, []wantChartDep{
		{name: "cert-manager", source: "https://charts.jetstack.io", version: "v1.14.0", span: "v1.14.0", line: 14},
		{name: "redis", source: "oci://registry-1.docker.io/bitnamicharts", version: "18.0.0", span: "18.0.0", line: 17},
		{name: "app", source: "oci://ghcr.io/org/charts", version: "2.0.0", span: "2.0.0", line: 22},
		{name: "other", source: "oci://ghcr.io/org/charts", version: "3.0.0", span: "3.0.0", line: 25},
		{name: "./charts/local", skip: "local chart", line: 27},
		{name: "mychart", skip: "local chart", line: 29},
		{name: "thing", skip: "unknown chart repository stable", line: 31},
		{name: "jetstack/a/b", skip: "invalid chart reference", line: 34},
		{name: "oci://nohost", skip: "invalid chart reference", line: 36},
		{name: "x", source: "https://charts.jetstack.io", version: "1.0.0", span: ">=1.0.0", line: 39},
	}, TypeHelmfile)
}

func TestHelmfileTemplate(t *testing.T) {
	const src = "repositories:\n  - name: jetstack\n    url: https://charts.jetstack.io\nreleases:\n{{- range .Values.apps }}\n  - name: cert-manager\n    chart: jetstack/cert-manager\n    version: {{ .Values.version }}\n  - name: fixed\n    chart: jetstack/fixed\n    version: 1.0.0\r\n{{- end }}\n"
	got, err := New().Extract("helmfile.yaml.gotmpl", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	checkChartDeps(t, src, got, []wantChartDep{
		{name: "cert-manager", source: "https://charts.jetstack.io", skip: "no version", line: 7},
		{name: "fixed", source: "https://charts.jetstack.io", version: "1.0.0", span: "1.0.0", line: 11},
	}, TypeHelmfile)
	if _, err := New().Extract("helmfile.yaml", []byte(src), manager.Env{}); err == nil {
		t.Error("template without .gotmpl parsed")
	}
}
