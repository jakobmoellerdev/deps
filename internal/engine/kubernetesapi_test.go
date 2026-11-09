package engine

import (
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/kubernetesapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/kubernetes"
)

// TestKubernetesAPI checks that an apiVersion updates within its API group, never gets a
// digest under pin: digest, and that versions Kubernetes no longer serves are reported with
// the version to use, also when no update exists (another API group).
func TestKubernetesAPI(t *testing.T) {
	files := map[string]string{"k8s/app.yaml": `apiVersion: apps/v1beta2
kind: Deployment
metadata:
  name: web
---
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: web
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
`}
	root, cfg := setup(t, "pin: digest\n", files)
	o := Options{
		Managers:    []manager.Manager{kubernetes.New()},
		Datasources: map[string]Datasource{kubernetesapi.Name: kubernetesapi.New()},
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	if len(res.Updates) != 1 {
		t.Fatalf("updates = %+v, want Deployment apps/v1beta2 → apps/v1", res.Updates)
	}
	if u := res.Updates[0]; u.Dep.Name != "Deployment" || u.NewVersion != "apps/v1" || u.NewDigest != "" {
		t.Errorf("update = %s %s → %s %q", u.Dep.Name, u.Dep.Version, u.NewVersion, u.NewDigest)
	}
	var reasons []string
	for _, w := range res.Warnings {
		if w.Kind != WarnDeprecated {
			t.Errorf("warning %+v", w)
		}
		reasons = append(reasons, w.Reason)
	}
	slices.Sort(reasons)
	want := []string{
		"apiVersion apps/v1beta2 of Deployment is no longer served since Kubernetes 1.16: use apps/v1",
		"apiVersion extensions/v1beta1 of Ingress is no longer served since Kubernetes 1.22: use networking.k8s.io/v1",
	}
	if !slices.Equal(reasons, want) {
		t.Errorf("warnings =\n%q\nwant\n%q", reasons, want)
	}
}
