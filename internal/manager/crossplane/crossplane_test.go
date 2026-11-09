package crossplane

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                      int
	depT, name, version, skip string
}

func extract(t *testing.T, src []byte, env manager.Env) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("packages.yaml", src, env)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.Datasource != "docker" || d.Versioning != "docker" {
			t.Errorf("%d: %s/%s/%s", i, d.Manager, d.Datasource, d.Versioning)
		}
		if d.Skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.DepType != w.depT || d.Line != w.line || d.Name != w.name || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got %s line=%d name=%q version=%q skip=%q\nwant %+v", i, d.DepType, d.Line, d.Name, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		"provider.yaml":          true,
		"crossplane/config.yml":  true,
		"packages/function.yaml": true,
		"Chart.lock":             false,
		"provider.json":          false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-aws-s3
spec:
  package: xpkg.upbound.io/upbound/provider-aws-s3:v1.21.1
---
apiVersion: pkg.crossplane.io/v1beta1
kind: Function
metadata:
  name: function-patch-and-transform
spec:
  package: "xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:v0.8.2@sha256:1111111111111111111111111111111111111111111111111111111111111111"
---
apiVersion: pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: platform-ref-aws
spec:
  package: ${REGISTRY}/upbound/platform-ref-aws:v1.4.0
---
# Package metadata (crossplane.yaml) uses meta.pkg.crossplane.io: no dep.
apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: platform-ref-aws
spec:
  package: xpkg.upbound.io/upbound/not-a-dep:v1.0.0
---
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata:
  name: default
spec:
  package: xpkg.upbound.io/upbound/not-a-dep:v1.0.0
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: no-package
spec: {}
`)
	check(t, extract(t, src, manager.Env{}), []want{
		{line: 6, depT: TypeProvider, name: "xpkg.upbound.io/upbound/provider-aws-s3", version: "v1.21.1"},
		{line: 13, depT: TypeFunction, name: "xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform", version: "v0.8.2"},
		{line: 20, depT: TypeConfiguration, name: "${REGISTRY}/upbound/platform-ref-aws:v1.4.0", skip: "variable reference"},
	})
	deps := extract(t, src, manager.Env{RegistryAliases: map[string]string{"${REGISTRY}": "xpkg.upbound.io"}})
	if d := deps[2]; d.Skip != "" || d.Source != "xpkg.upbound.io/upbound/platform-ref-aws" || d.Version != "v1.4.0" {
		t.Errorf("aliased = %+v", d)
	}
}

func TestExtractNoDeps(t *testing.T) {
	for _, src := range []string{
		"",
		"apiVersion: v1\nkind: ConfigMap\n",
		// A template that does not parse yields no deps and no error.
		"apiVersion: pkg.crossplane.io/v1\nkind: Provider\nspec:\n  package: {{ .Values.pkg }}\n",
		"apiVersion: pkg.crossplane.io/v1\nkind: Provider\nspec: [\n",
	} {
		if deps := extract(t, []byte(src), manager.Env{}); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
}

// The fixtures are crossplane/crossplane's e2e manifests: functions (v1beta1 and v1), a
// provider, configurations in one multi-document file, and an ImageConfig without packages.
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		wants []want
	}{
		{"testdata/functions.yaml", []want{
			{line: 12, depT: TypeFunction, name: "xpkg.crossplane.io/crossplane-contrib/function-dummy", version: "v0.4.1"},
			{line: 22, depT: TypeFunction, name: "xpkg.upbound.io/crossplane-contrib/function-auto-ready", version: "v0.7.0"},
		}},
		{"testdata/provider.yaml", []want{
			{line: 6, depT: TypeProvider, name: "xpkg.upbound.io/crossplane-contrib/provider-nop", version: "v0.6.0"},
		}},
		{"testdata/configurations.yaml", []want{
			{line: 6, depT: TypeConfiguration, name: "xpkg.crossplane.io/crossplane/e2e-transitive-dep1", version: "v0.0.1"},
			{line: 13, depT: TypeConfiguration, name: "xpkg.crossplane.io/crossplane/e2e-transitive-dep2", version: "v0.0.1"},
			{line: 20, depT: TypeConfiguration, name: "xpkg.crossplane.io/crossplane/e2e-transitive-transitive", version: "v0.0.1"},
		}},
		{"testdata/image-config.yaml", nil},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			check(t, extract(t, src, manager.Env{}), tt.wants)
		})
	}
}

func TestEditsAndRename(t *testing.T) {
	src := []byte("apiVersion: pkg.crossplane.io/v1\nkind: Provider\nspec:\n  package: xpkg.upbound.io/upbound/provider-aws-s3:v1.20.0\n")
	deps := extract(t, src, manager.Env{})
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "v1.21.1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "apiVersion: pkg.crossplane.io/v1\nkind: Provider\nspec:\n  package: xpkg.upbound.io/upbound/provider-aws-s3:v1.21.1\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	d, err := New().(manager.Renamer).Rename(deps[0], "xpkg.crossplane.io/crossplane-contrib/provider-aws-s3")
	if err != nil || d.Source != "xpkg.crossplane.io/crossplane-contrib/provider-aws-s3" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
}
