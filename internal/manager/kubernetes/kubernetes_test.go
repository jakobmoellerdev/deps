package kubernetes

import (
	"os"
	"reflect"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// got is what the tests compare of a dep.
type got struct {
	Name, Version, Ref string
	Line               int
}

func extract(t *testing.T, file string, src []byte) []got {
	t.Helper()
	deps, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) == 0 {
		return nil
	}
	out := make([]got, len(deps))
	for i, d := range deps {
		if d.Manager != Name || d.Datasource != "kubernetes-api" || d.Versioning != "kubernetes-api" || d.DepType != DepTypeAPIVersion || d.File != file {
			t.Errorf("dep %+v", d)
		}
		out[i] = got{d.Name, d.Version, string(src[d.RefSpan.Start:d.RefSpan.End]), d.Line}
	}
	return out
}

// TestExtractRealManifests reads manifests of argoproj/argo-cd (a9e68a3, 2026-11-06): two
// CustomResourceDefinitions of applicationset/examples/matrix/cluster-addons/argo-workflows/
// namespace-install.yaml, gitops-engine/pkg/utils/kube/testdata/extensionsdeployment.yaml and
// a Flux Bucket of resource_customizations/source.toolkit.fluxcd.io/Bucket/testdata/
// healthy.yaml, joined into one multi-document file. Nested `kind:` keys (the CRD names) are
// not the document's kind.
func TestExtractRealManifests(t *testing.T) {
	src, err := os.ReadFile("testdata/manifests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []got{
		{"CustomResourceDefinition", "apiextensions.k8s.io/v1beta1", "apiextensions.k8s.io/v1beta1", 2},
		{"CustomResourceDefinition", "apiextensions.k8s.io/v1beta1", "apiextensions.k8s.io/v1beta1", 23},
		{"Deployment", "extensions/v1beta1", "extensions/v1beta1", 44},
		{"Bucket", "source.toolkit.fluxcd.io/v1beta2", "source.toolkit.fluxcd.io/v1beta2", 58},
	}
	if g := extract(t, "deploy/manifests.yaml", src); !reflect.DeepEqual(g, want) {
		t.Errorf("deps =\n%+v\nwant\n%+v", g, want)
	}
}

// TestExtractSkipsTemplates: the Helm template util/helm/testdata/minio/templates/ingress.yaml
// of argoproj/argo-cd hard-codes extensions/v1beta1 next to `{{` directives; Helm charts choose
// the apiVersion by cluster version in such documents, so they have no deps.
func TestExtractSkipsTemplates(t *testing.T) {
	src, err := os.ReadFile("testdata/ingress-template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if g := extract(t, "templates/ingress.yaml", src); len(g) != 0 {
		t.Errorf("deps = %+v, want none", g)
	}
}

func TestExtract(t *testing.T) {
	for _, tt := range []struct {
		name, src string
		want      []got
	}{
		{"quoted with comment", "kind: Ingress\napiVersion: \"networking.k8s.io/v1beta1\"  # old\n",
			[]got{{"Ingress", "networking.k8s.io/v1beta1", "networking.k8s.io/v1beta1", 2}}},
		{"single quotes, CRLF", "apiVersion: 'batch/v1beta1'\r\nkind: CronJob\r\n",
			[]got{{"CronJob", "batch/v1beta1", "batch/v1beta1", 1}}},
		{"current version is a dep", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [a.yaml]\n",
			[]got{{"Kustomization", "kustomize.config.k8s.io/v1beta1", "kustomize.config.k8s.io/v1beta1", 1}}},
		{"document markers", "--- \napiVersion: policy/v1beta1\nkind: PodDisruptionBudget\n...\n---\napiVersion: policy/v1\nkind: PodDisruptionBudget\n",
			[]got{{"PodDisruptionBudget", "policy/v1beta1", "policy/v1beta1", 2}, {"PodDisruptionBudget", "policy/v1", "policy/v1", 6}}},
		{"version the table does not list", "apiVersion: autoscaling/v1\nkind: HorizontalPodAutoscaler\n---\napiVersion: v1\nkind: Event\n", nil},
		{"unknown kind", "apiVersion: monitoring.coreos.com/v1\nkind: ServiceMonitor\n", nil},
		{"indented keys", "items:\n  - apiVersion: apps/v1beta1\n    kind: Deployment\n", nil},
		{"flow and escaped values", "apiVersion: [apps/v1beta1]\nkind: Deployment\n---\napiVersion: \"apps\\/v1beta1\"\nkind: Deployment\n", nil},
		{"no kind", "apiVersion: apps/v1beta1\nmetadata: {}\n", nil},
		{"no keys", "image: nginx\n", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if g := extract(t, "x.yaml", []byte(tt.src)); !reflect.DeepEqual(g, tt.want) {
				t.Errorf("deps =\n%+v\nwant\n%+v", g, tt.want)
			}
		})
	}
}

func TestMatchAndEdits(t *testing.T) {
	m := New()
	if m.Name() != Name {
		t.Errorf("Name = %q", m.Name())
	}
	for path, want := range map[string]bool{"k8s/deploy.yaml": true, "a.yml": true, "Chart.lock": false, "values.json": false} {
		if m.Match(path) != want {
			t.Errorf("Match(%s) = %t", path, !want)
		}
	}
	src := []byte("apiVersion: \"apps/v1beta2\"\nkind: Deployment\n")
	deps, err := m.Extract("d.yaml", src, manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatalf("Extract = %+v, %v", deps, err)
	}
	edits, err := m.Edits(dep.Update{Dep: deps[0], NewVersion: "apps/v1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, edits)
	if err != nil || string(out) != "apiVersion: \"apps/v1\"\nkind: Deployment\n" {
		t.Errorf("patched = %q, %v", out, err)
	}
}
