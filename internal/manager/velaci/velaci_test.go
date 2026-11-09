package velaci

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                      int
	depT, name, version, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".vela.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.Datasource != "docker" || d.Versioning != "docker" {
			t.Errorf("%d: %s/%s/%s", i, d.Manager, d.Datasource, d.Versioning)
		}
		if d.Version != "" && !docker.Untagged(d) && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, n int, wants map[int]want) {
	t.Helper()
	if len(deps) != n {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), n, deps)
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
		".vela.yml":         true,
		".vela.yaml":        true,
		"sub/.vela.yml":     true,
		"vela.yml":          false,
		".vela.yml.tmpl":    false,
		".vela/build.yml":   false,
		"sub/my.vela.yml":   false,
		".drone.yml":        false,
		"templates/x.yml":   false,
		".github/vela.yaml": false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

// The source combines Renovate's velaci fixtures (stages, secrets with an origin) with a
// variable reference and entries without images.
func TestExtract(t *testing.T) {
	src := []byte(`version: "1"
stages:
  go:
    steps:
      - name: go
        image: golang:1.13
  node:
    steps:
      - name: node
        image: amd64/node:10.0.0@sha256:5082d4db78ee2d24f72b25eb75537f2e19c49f04a595378d1ae8814d6f2fbf28
steps:
  - name: tmpl
    template:
      name: build
  - name: vars
    image: ${REGISTRY}/tool:1
services:
  - name: db
    image: postgres:18
secrets:
  - name: vault_token
  - origin:
      name: private vault
      image: target/secret-vault:v0.1.0
`)
	check(t, extract(t, src), 5, map[int]want{
		0: {line: 16, depT: TypeImage, name: "${REGISTRY}/tool:1", skip: "variable reference"},
		1: {line: 19, depT: TypeService, name: "postgres", version: "18"},
		2: {line: 6, depT: TypeImage, name: "golang", version: "1.13"},
		3: {line: 10, depT: TypeImage, name: "amd64/node", version: "10.0.0"},
		4: {line: 24, depT: TypeSecret, name: "target/secret-vault", version: "v0.1.0"},
	})
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{"", "- a\n", "steps: foo\nstages: [a]\nsecrets: {a: 1}\n"} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
	if _, err := New().Extract(".vela.yml", []byte("steps: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixture is zapplebee/nyx-auth's .vela.yml: services and steps, images repeated per step.
func TestExtractFixture(t *testing.T) {
	src, err := os.ReadFile("testdata/nyx-auth.yml")
	if err != nil {
		t.Fatal(err)
	}
	check(t, extract(t, src), 11, map[int]want{
		0:  {line: 68, depT: TypeImage, name: "alpine", version: "latest"},
		8:  {line: 25, depT: TypeService, name: "oven/bun", version: "1.3.5"},
		9:  {line: 41, depT: TypeService, name: "node", version: "20-alpine"},
		10: {line: 53, depT: TypeService, name: "oven/bun", version: "1.3.5"},
	})
}

func TestEditsAndRename(t *testing.T) {
	src := []byte("steps:\n  - image: golang:1.26\n")
	deps := extract(t, src)
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "1.27"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "steps:\n  - image: golang:1.27\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	d, err := New().(manager.Renamer).Rename(deps[0], "ghcr.io/acme/go")
	if err != nil || d.Source != "ghcr.io/acme/go" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
}
