package kubernetesapi

import (
	"context"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

var (
	_ datasource.Datasource        = Source{}
	_ datasource.DeprecationSource = Source{}
)

func TestReleases(t *testing.T) {
	rels, err := New().Releases(context.Background(), dep.Dep{Name: "Deployment"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(rels))
	for i, r := range rels {
		got[i] = r.Version
	}
	if want := []string{"extensions/v1beta1", "apps/v1beta1", "apps/v1beta2", "apps/v1"}; !slices.Equal(got, want) {
		t.Errorf("Deployment = %v, want %v", got, want)
	}
	if _, err := New().Releases(context.Background(), dep.Dep{Name: "ServiceMonitor"}, ""); err == nil || !strings.Contains(err.Error(), `unknown kind "ServiceMonitor"`) {
		t.Errorf("unknown kind: err = %v", err)
	}
	if _, err := New().Digest(context.Background(), dep.Dep{Name: "Deployment"}, "apps/v1"); err == nil {
		t.Error("Digest: no error")
	}
}

// TestUpdateTargets checks the table against kubernetes-api versioning: updates stay in the API
// group, as in Renovate.
func TestUpdateTargets(t *testing.T) {
	s, ok := versioning.Get(Versioning)
	if !ok {
		t.Fatal("no kubernetes-api versioning")
	}
	for _, tt := range []struct{ kind, cur, want string }{
		{"Deployment", "apps/v1beta1", "apps/v1"},
		{"Deployment", "apps/v1beta2", "apps/v1"},
		{"Deployment", "extensions/v1beta1", ""}, // another group: only warned about
		{"Deployment", "apps/v1", ""},
		{"HorizontalPodAutoscaler", "autoscaling/v2beta1", "autoscaling/v2"},
		{"FlowSchema", "flowcontrol.apiserver.k8s.io/v1beta2", "flowcontrol.apiserver.k8s.io/v1"},
		{"HelmRelease", "helm.toolkit.fluxcd.io/v2beta1", "helm.toolkit.fluxcd.io/v2"},
		{"Bucket", "source.toolkit.fluxcd.io/v1alpha1", "source.toolkit.fluxcd.io/v1"},
		{"Kustomization", "kustomize.toolkit.fluxcd.io/v1beta2", "kustomize.toolkit.fluxcd.io/v1"},
		{"Kustomization", "kustomize.config.k8s.io/v1beta1", ""},
		{"Alert", "notification.toolkit.fluxcd.io/v1beta2", "notification.toolkit.fluxcd.io/v1beta3"},
	} {
		got, _ := s.Best(tt.cur, apis[tt.kind], nil, nil)
		if got != tt.want {
			t.Errorf("%s %s → %q, want %q", tt.kind, tt.cur, got, tt.want)
		}
	}
}

func TestDeprecation(t *testing.T) {
	for _, tt := range []struct{ kind, version, want string }{
		{"Deployment", "extensions/v1beta1", "apiVersion extensions/v1beta1 of Deployment is no longer served since Kubernetes 1.16: use apps/v1"},
		{"Deployment", "apps/v1beta2", "apiVersion apps/v1beta2 of Deployment is no longer served since Kubernetes 1.16: use apps/v1"},
		{"Ingress", "extensions/v1beta1", "apiVersion extensions/v1beta1 of Ingress is no longer served since Kubernetes 1.22: use networking.k8s.io/v1"},
		{"HorizontalPodAutoscaler", "autoscaling/v2beta2", "apiVersion autoscaling/v2beta2 of HorizontalPodAutoscaler is no longer served since Kubernetes 1.26: use autoscaling/v2"},
		{"FlowSchema", "flowcontrol.apiserver.k8s.io/v1beta3", "apiVersion flowcontrol.apiserver.k8s.io/v1beta3 of FlowSchema is no longer served since Kubernetes 1.32: use flowcontrol.apiserver.k8s.io/v1"},
		{"PodSecurityPolicy", "policy/v1beta1", "apiVersion policy/v1beta1 of PodSecurityPolicy is no longer served since Kubernetes 1.25"},
		{"PodSecurityPolicy", "extensions/v1beta1", "apiVersion extensions/v1beta1 of PodSecurityPolicy is no longer served since Kubernetes 1.16"},
		{"HelmRelease", "helm.toolkit.fluxcd.io/v2beta2", "apiVersion helm.toolkit.fluxcd.io/v2beta2 of HelmRelease is deprecated: use helm.toolkit.fluxcd.io/v2"},
		{"ExternalSecret", "external-secrets.io/v1beta1", "apiVersion external-secrets.io/v1beta1 of ExternalSecret is deprecated: use external-secrets.io/v1"},
		{"Deployment", "apps/v1", ""},
		{"Kustomization", "kustomize.config.k8s.io/v1beta1", ""},
		{"Kustomization", "kustomize.toolkit.fluxcd.io/v1", ""},
		{"HorizontalPodAutoscaler", "autoscaling/v1", ""}, // not in the table
		{"ServiceMonitor", "monitoring.coreos.com/v1", ""},
	} {
		if got := New().Deprecation(dep.Dep{Name: tt.kind}, tt.version); got != tt.want {
			t.Errorf("%s %s: %q, want %q", tt.kind, tt.version, got, tt.want)
		}
	}
}

// TestTable keeps the table consistent: every version parses under kubernetes-api
// versioning, each kind lists a version once, and every removal names a listed version.
func TestTable(t *testing.T) {
	s, _ := versioning.Get(Versioning)
	for kind, vs := range apis {
		for i, v := range vs {
			if !s.Valid(v) || slices.Index(vs, v) != i {
				t.Errorf("%s: invalid or repeated %q", kind, v)
			}
		}
	}
	for k := range removed {
		kind, v, _ := strings.Cut(k, " ")
		if !Known(kind, v) {
			t.Errorf("removed %s is not in the table", k)
		}
	}
	if Known("Event", "v1") || !Known("Event", "events.k8s.io/v1") {
		t.Error("Known(Event)")
	}
	if group("v1") != "" || group("apps/v1") != "apps" {
		t.Error("group")
	}
}
