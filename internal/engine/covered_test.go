package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
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

// Two custom regex entries matching the same version (OCM's envtest-specific and generic .env
// managers): the first entry's dep is resolved, the later one is skipped.
func TestResolveFirstCustomMatchWins(t *testing.T) {
	root, cfg := setup(t, `custom:
  - files: [.env]
    match: 'E=(?P<version>\S+)'
    datasource: go
    name: example.com/a
  - files: [.env]
    match: '(?P<name>\w+)=(?P<version>\S+)'
    datasource: go
`, map[string]string{".env": "E=v1.0.0\n"})
	ms, err := Managers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	o := sources(nil, fakeGo{"example.com/a": {"v1.0.0", "v1.1.0"}, "E": {"v1.0.0", "v9.0.0"}})
	o.Managers = ms
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("err = %v, %v", err, res.Errors)
	}
	if len(res.Updates) != 1 || res.Updates[0].Dep.Name != "example.com/a" || len(res.Skipped) != 1 || res.Skipped[0].Skip != "covered by custom match on line 1" {
		t.Errorf("updates = %+v, skipped = %+v", res.Updates, res.Skipped)
	}
}

// assetChecksums returns the checksum of the release asset a dep's checksum source names at a
// version (missing = error, as a release without the asset).
type assetChecksums map[string]string

func (c assetChecksums) Checksum(_ context.Context, d dep.Dep, v, _ string) (string, error) {
	a := dep.ExpandVersion(d.ChecksumSource.Asset, v)
	if s, ok := c[a]; ok {
		return s, nil
	}
	return "", errors.New("release " + v + " has no asset " + a)
}

// Custom entries matching one version with a checksum each (a tool pinned per platform):
// with the same datasource and name they share the version, which moves once and only with
// every checksum; a different name, or a later entry writing nothing of its own, is skipped as
// a duplicate.
func TestResolveCustomSharedVersion(t *testing.T) {
	h0, x86, arm := strings.Repeat("0", 64), strings.Repeat("a", 64), strings.Repeat("b", 64)
	src := "NFPM_VERSION=2.47.0\nNFPM_SHA256_LINUX_X86_64=" + h0 + "\nNFPM_SHA256_LINUX_ARM64=" + h0 + "\n"
	entry := func(name, arch, asset string) string {
		e := "  - files: [.env]\n    datasource: github-releases\n    name: " + name + "\n"
		if arch == "" {
			return e + `    match: 'NFPM_VERSION=(?P<version>\S+)'` + "\n"
		}
		return e + `    match: 'NFPM_VERSION=(?P<version>\S+)\n(?:.*\n)*?NFPM_SHA256_LINUX_` + arch + `=(?P<checksum>[0-9a-f]{64})'` + "\n" +
			"    checksum: {asset: 'nfpm_{{version}}_Linux_" + asset + ".tar.gz'}\n"
	}
	both := assetChecksums{"nfpm_2.48.0_Linux_x86_64.tar.gz": x86, "nfpm_2.48.0_Linux_arm64.tar.gz": arm}
	for _, c := range []struct {
		name    string
		second  string // the second custom entry
		sums    assetChecksums
		file    string   // the file after Build; "" = no updates
		errs    []string // suffixes of the errors
		skipped string   // the skip of the second entry's dep; "" = shares the version
	}{
		{name: "shared", second: entry("goreleaser/nfpm", "ARM64", "arm64"), sums: both,
			file: "NFPM_VERSION=2.48.0\nNFPM_SHA256_LINUX_X86_64=" + x86 + "\nNFPM_SHA256_LINUX_ARM64=" + arm + "\n"},
		{name: "one checksum missing", second: entry("goreleaser/nfpm", "ARM64", "arm64"),
			sums: assetChecksums{"nfpm_2.48.0_Linux_x86_64.tar.gz": x86},
			errs: []string{"release 2.48.0 has no asset nfpm_2.48.0_Linux_arm64.tar.gz",
				"1 of 2 deps of goreleaser/nfpm in .env resolved to 2.48.0, the others did not; updated none"}},
		{name: "other name", second: entry("goreleaser/other", "ARM64", "arm64"), sums: both,
			file:    "NFPM_VERSION=2.48.0\nNFPM_SHA256_LINUX_X86_64=" + x86 + "\nNFPM_SHA256_LINUX_ARM64=" + h0 + "\n",
			skipped: "covered by custom match on line 1"},
		{name: "nothing of its own", second: entry("goreleaser/nfpm", "", ""), sums: both,
			file:    "NFPM_VERSION=2.48.0\nNFPM_SHA256_LINUX_X86_64=" + x86 + "\nNFPM_SHA256_LINUX_ARM64=" + h0 + "\n",
			skipped: "covered by custom match on line 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, cfg := setup(t, "custom:\n"+entry("goreleaser/nfpm", "X86_64", "x86_64")+c.second, map[string]string{".env": src})
			ms, err := Managers(cfg)
			if err != nil {
				t.Fatal(err)
			}
			o := Options{Managers: ms, Checksums: c.sums, Datasources: map[string]Datasource{"github-releases": fakeReleases{"2.48.0", "2.47.0"}}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil {
				t.Fatal(err)
			}
			var skips []string
			for _, d := range res.Skipped {
				skips = append(skips, d.Skip)
			}
			if want := slices.DeleteFunc([]string{c.skipped}, func(s string) bool { return s == "" }); len(res.Deps) != 2 || !slices.Equal(skips, want) {
				t.Fatalf("deps = %+v, skipped = %q, want %q", res.Deps, skips, want)
			}
			var errs []string
			for _, e := range res.Errors {
				errs = append(errs, e.Error())
			}
			if len(errs) != len(c.errs) {
				t.Errorf("errors = %q, want %q", errs, c.errs)
			}
			for _, want := range c.errs {
				if !slices.ContainsFunc(errs, func(e string) bool { return strings.HasSuffix(e, want) }) {
					t.Errorf("errors = %q, want one ending in %q", errs, want)
				}
			}
			if c.file == "" {
				if len(res.Updates) != 0 {
					t.Errorf("updates = %+v, want none", res.Updates)
				}
				return
			}
			gs := GroupUpdates(res.Updates, "")
			if len(gs) != 1 || gs[0].Title != "chore(deps): update goreleaser/nfpm to 2.48.0" {
				t.Fatalf("groups = %+v, want one PR", gs)
			}
			if local, _ := Local(res.Updates); len(local) != len(res.Updates) {
				t.Errorf("local = %+v of %+v, want all", local, res.Updates)
			}
			changes, errs2 := Build(t.Context(), root, ms, gs[0].Updates, nil)
			if len(errs2) != 0 || len(changes) != 1 || string(changes[0].New) != c.file {
				t.Fatalf("Build = %v, %v; want\n%s", changes, errs2, c.file)
			}
		})
	}
}

// A JSONata entry over compose files claims the image tags it selects: the native docker deps
// of those bytes are skipped, the custom deps stay.
func TestResolveJSONataCoversNativeDep(t *testing.T) {
	root, cfg := setup(t, `custom:
  - files: [compose.yml]
    fileFormat: yaml
    jsonata: 'services.*[$contains(image, "nginx")].{"depName": $substringBefore(image, ":"), "currentValue": $substringAfter(image, ":")}'
    datasource: docker
    versioning: docker
`, map[string]string{"compose.yml": "services:\n  web:\n    image: nginx:1.25.0\n  db:\n    image: postgres:16.1\n"})
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
	type key struct{ manager, name, skip string }
	var got []key
	for _, d := range res.Deps {
		got = append(got, key{d.Manager, d.Name, d.Skip})
	}
	for _, want := range []key{
		{"docker", "nginx", "covered by custom jsonata match on line 3"},
		{"custom", "nginx", ""},
		{"docker", "postgres", ""},
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %+v in %+v", want, got)
		}
	}
}
