package droneci

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
	deps, err := New().Extract(".drone.yml", src, manager.Env{})
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
		".drone.yml":      true,
		"sub/.drone.yml":  true,
		".drone.yaml":     false,
		"x.drone.yml":     false,
		"drone.yml":       false,
		"sub/.drone.yml~": false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`---
kind: pipeline
type: docker
name: default
steps:
  - name: test
    image: golang:1.26
  - name: publish
    image: plugins/docker
  - name: escaped
    image: "registry.example.com/\
      team/tool:1.0"
services:
  - name: db
    image: postgres:17
---
kind: secret
name: token
get:
  path: secret/data/ci
---
pipeline:
  build:
    image: node:${NODE}
services:
  cache:
    image: redis:7.4
`)
	check(t, extract(t, src), 6, map[int]want{
		0: {line: 7, depT: TypeImage, name: "golang", version: "1.26"},
		1: {line: 9, depT: TypeImage, name: "plugins/docker", skip: "no tag"},
		2: {line: 11, depT: TypeImage, name: "registry.example.com/team/tool:1.0", skip: "cannot locate value"},
		3: {line: 15, depT: TypeService, name: "postgres", version: "17"},
		4: {line: 24, depT: TypeImage, name: "node:${NODE}", skip: "variable reference"},
		5: {line: 27, depT: TypeService, name: "redis", version: "7.4"},
	})
	if _, err := New().Extract(".drone.yml", []byte("steps:\n  - image: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
	if deps := extract(t, []byte("kind: secret\n---\n- image\n")); len(deps) != 0 {
		t.Errorf("deps = %+v", deps)
	}
}

// The fixtures are real-world multi-pipeline files: openware/barong (services and steps per
// database pipeline), GoAdminGroup/go-admin (commented-out steps) and bitpoke/mysql-operator
// (untagged plugin images, dind services).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/barong.yml", 31, map[int]want{
			0:  {line: 35, depT: TypeImage, name: "vault", version: "0.11.4"},
			2:  {line: 65, depT: TypeImage, name: "quay.io/openware/sdk-citools", version: "2.6.7"},
			5:  {line: 116, depT: TypeImage, name: "plugins/docker", skip: "no tag"},
			9:  {line: 7, depT: TypeService, name: "mysql", version: "5.7"},
			15: {line: 200, depT: TypeService, name: "mariadb", version: "10.3"},
		}},
		{"testdata/go-admin.yml", 7, map[int]want{
			0: {line: 22, depT: TypeImage, name: "chg80333/goadmin-test", version: "v9"},
			3: {line: 93, depT: TypeService, name: "postgres", version: "10"},
		}},
		{"testdata/mysql-operator.yml", 18, map[int]want{
			1: {line: 20, depT: TypeImage, name: "bitpoke/build", version: "v0.8.3"},
			6: {line: 49, depT: TypeService, name: "docker", version: "20.10.8-dind-rootless"},
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
	src := []byte("kind: pipeline\nsteps:\n  - image: golang:1.26\n---\nkind: pipeline\nservices:\n  - image: redis:7.2\n")
	deps := extract(t, src)
	var edits []patch.Edit
	for _, u := range []dep.Update{{Dep: deps[0], NewVersion: "1.27"}, {Dep: deps[1], NewVersion: "7.4"}} {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply(src, edits)
	if want := "kind: pipeline\nsteps:\n  - image: golang:1.27\n---\nkind: pipeline\nservices:\n  - image: redis:7.4\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	d, err := New().(manager.Renamer).Rename(deps[0], "ghcr.io/acme/go")
	if err != nil || d.Source != "ghcr.io/acme/go" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
}
