package engine

import (
	"slices"
	"testing"
)

func TestResolveAnnotationCoversNativeDep(t *testing.T) {
	root, cfg := setup(t, `custom:
  - files: ["**/*.yaml", "**/*.yml"]
`, map[string]string{
		"k0s/manifests/charts.yaml": `apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
metadata:
  name: cert-manager
spec:
  chartName: jetstack/cert-manager
  # deps: helm https://charts.jetstack.io cert-manager
  version: "v1.21.3"
  repository:
    url: https://charts.jetstack.io
---
apiVersion: helm.k0sproject.io/v1beta1
kind: Chart
spec:
  chartName: other/plain
  version: 1.0.0
  repository:
    url: https://charts.example.com
`,
		"compose.yml": `services:
  web:
    # deps: docker nginx
    image: nginx:1.25.0
  db:
    image: postgres:16.1
`,
	})
	ms, err := Managers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	o := sources(nil, nil)
	o.Managers = ms
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ manager, file, name, skip string }
	var got []key
	for _, d := range res.Deps {
		got = append(got, key{d.Manager, d.File, d.Name, d.Skip})
	}
	has := func(k key) bool { return slices.Contains(got, k) }
	for _, want := range []key{
		{"helm", "k0s/manifests/charts.yaml", "cert-manager", "covered by annotation on line 7"},
		{"docker", "compose.yml", "nginx", "covered by annotation on line 3"},
	} {
		if !has(want) {
			t.Errorf("missing %+v in %+v", want, got)
		}
	}
	// The annotations themselves and the unannotated native deps stay updatable.
	var custom, native []string
	for _, d := range res.Deps {
		if d.Skip != "" {
			continue
		}
		if d.Manager == "custom" {
			custom = append(custom, d.File+":"+d.Name)
		} else {
			native = append(native, d.Manager+":"+d.Name)
		}
	}
	slices.Sort(custom)
	slices.Sort(native)
	if want := []string{"compose.yml:nginx", "k0s/manifests/charts.yaml:cert-manager"}; !slices.Equal(custom, want) {
		t.Errorf("updatable custom deps = %q, want %q", custom, want)
	}
	if want := []string{"docker:postgres", "helm:plain"}; !slices.Equal(native, want) {
		t.Errorf("updatable native deps = %q, want %q", native, want)
	}
}
