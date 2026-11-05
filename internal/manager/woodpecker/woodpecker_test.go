package woodpecker

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

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".woodpecker.yaml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.Datasource != "docker" || d.Versioning != "docker" {
			t.Errorf("%d: %s/%s/%s", i, d.Manager, d.Datasource, d.Versioning)
		}
		if d.Version != "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
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
		".woodpecker.yml":       true,
		".woodpecker.yaml":      true,
		".woodpecker/test.yaml": true,
		".woodpecker/build.yml": true,
		".woodpecker/sub/x.yml": false,
		"sub/.woodpecker.yml":   false,
		".woodpecker/README.md": false,
		".woodpeckerrc.yml":     false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`variables:
  - &go 'golang:1.27'
clone:
  git:
    image: woodpeckerci/plugin-git:2.6.0
steps:
  - name: build
    image: *go
  - name: test
    image: *go
  - name: matrix
    image: node:${NODE_VERSION}
  - name: plain
    image: alpine
services:
  db:
    image: postgres:18@sha256:1111111111111111111111111111111111111111111111111111111111111111
pipeline:
  legacy:
    image: debian:13
`)
	check(t, extract(t, src), 6, map[int]want{
		0: {line: 5, depT: TypeImage, name: "woodpeckerci/plugin-git", version: "2.6.0"},
		1: {line: 2, depT: TypeImage, name: "golang", version: "1.27"},
		2: {line: 12, depT: TypeImage, name: "node:${NODE_VERSION}", skip: "variable reference"},
		3: {line: 14, depT: TypeImage, name: "alpine", skip: "no tag"},
		4: {line: 20, depT: TypeImage, name: "debian", version: "13"},
		5: {line: 17, depT: TypeService, name: "postgres", version: "18"},
	})
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{"", "- a\n", "steps: foo\n"} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
	if _, err := New().Extract(".woodpecker.yml", []byte("steps: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixtures are woodpecker-ci/woodpecker's own workflows: images written once as anchors in
// `variables` and used as aliases, services, and plugin steps.
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/test.yaml", 6, map[int]want{
			0: {line: 2, depT: TypeImage, name: "golang", version: "1.27"},
			1: {line: 59, depT: TypeImage, name: "golangci/golangci-lint", version: "v2.15.0"},
			4: {line: 174, depT: TypeService, name: "postgres", version: "18"},
		}},
		{"testdata/docker.yaml", 4, map[int]want{
			1: {line: 3, depT: TypeImage, name: "node", version: "26-alpine"},
			3: {line: 5, depT: TypeImage, name: "woodpeckerci/plugin-docker-buildx", version: "6.1.2"},
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			check(t, extract(t, src), tt.n, tt.wants)
		})
	}
}

func TestEditsAndRename(t *testing.T) {
	src := []byte("steps:\n  - image: golang:1.26\n")
	deps := extract(t, src)
	sha := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "1.27", NewDigest: sha})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "steps:\n  - image: golang:1.27@" + sha + "\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	d, err := New().(manager.Renamer).Rename(deps[0], "ghcr.io/acme/go")
	if err != nil || d.Source != "ghcr.io/acme/go" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
}
