package circleci

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                          int
	depT, ds, name, version, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".circleci/config.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name {
			t.Errorf("%d: manager %q", i, d.Manager)
		}
		if d.Version != "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
		if d.Digest != "" && string(src[d.DigestSpan.Start:d.DigestSpan.End]) != d.Digest {
			t.Errorf("%d: src[DigestSpan] = %q, want %q", i, src[d.DigestSpan.Start:d.DigestSpan.End], d.Digest)
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
		if d.DepType != w.depT || d.Datasource != w.ds || d.Line != w.line || d.Name != w.name || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got %s/%s line=%d name=%q version=%q skip=%q\nwant %+v", i, d.DepType, d.Datasource, d.Line, d.Name, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		".circleci/config.yml":          true,
		".circleci/continue.yaml":       true,
		"sub/.circleci/workflows/x.yml": true,
		".circleci/README.md":           false,
		"circleci/config.yml":           false,
		".circleci.yml":                 false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	const (
		d = "docker"
		o = Datasource
	)
	src := []byte(`version: 2.1
orbs:
  node: circleci/node@5.1.0
  aws: circleci/aws-cli@4
  slack: 'circleci/slack@4.12'
  latest: circleci/go@volatile
  bare: circleci/python
  param: circleci/x@<< pipeline.parameters.v >>
  local: noslash@1.0.0
  inline:
    orbs:
      docker: circleci/docker@2.8.1
    executors:
      default:
        docker:
          - image: cimg/base:2024.01
aliases:
  - image: cimg/go:1.26
executors:
  node:
    docker:
      - image: cimg/node:26.0.0
      - image: postgres:17@sha256:1111111111111111111111111111111111111111111111111111111111111111
  param:
    docker:
      - image: cimg/python:<< parameters.version >>
jobs:
  build:
    docker:
      - image: ${IMAGE}
    steps: [checkout]
  vm:
    machine:
      image: ubuntu-2204:2024.01.1
`)
	check(t, extract(t, src), 14, map[int]want{
		0:  {line: 3, depT: TypeOrb, ds: o, name: "circleci/node", version: "5.1.0"},
		1:  {line: 4, depT: TypeOrb, ds: o, name: "circleci/aws-cli", version: "4"},
		2:  {line: 5, depT: TypeOrb, ds: o, name: "circleci/slack", version: "4.12"},
		3:  {line: 6, depT: TypeOrb, ds: o, name: "circleci/go", version: "volatile"},
		4:  {line: 7, depT: TypeOrb, ds: o, name: "circleci/python", skip: "no version"},
		5:  {line: 8, depT: TypeOrb, ds: o, name: "circleci/x@<< pipeline.parameters.v >>", skip: "parameter reference"},
		6:  {line: 9, depT: TypeOrb, ds: o, name: "noslash", skip: "invalid orb"},
		7:  {line: 12, depT: TypeOrb, ds: o, name: "circleci/docker", version: "2.8.1"},
		8:  {line: 16, depT: TypeImage, ds: d, name: "cimg/base", version: "2024.01"},
		9:  {line: 22, depT: TypeImage, ds: d, name: "cimg/node", version: "26.0.0"},
		10: {line: 23, depT: TypeImage, ds: d, name: "postgres", version: "17"},
		11: {line: 26, depT: TypeImage, ds: d, name: "cimg/python:<< parameters.version >>", skip: "parameter reference"},
		12: {line: 30, depT: TypeImage, ds: d, name: "${IMAGE}", skip: "variable reference"},
		13: {line: 18, depT: TypeImage, ds: d, name: "cimg/go", version: "1.26"},
	})
	if _, err := New().Extract(".circleci/config.yml", []byte("orbs: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
	if deps := extract(t, []byte("")); len(deps) != 0 {
		t.Errorf("deps = %+v", deps)
	}
}

// An anchored `docker` block under a custom top-level key, merged into a job with `<<`, is
// extracted once where it is written.
func TestExtractAnchoredDefaults(t *testing.T) {
	src := []byte(`defaults: &defaults
  docker:
    - image: cimg/node:18.0
parameters:
  docker:
    - image: not/an-image:1
jobs:
  build:
    <<: *defaults
    steps: [checkout]
  test: *defaults
`)
	check(t, extract(t, src), 1, map[int]want{
		0: {line: 3, depT: TypeImage, ds: "docker", name: "cimg/node", version: "18.0"},
	})
}

// The fixtures are real-world configurations: jasmine/jasmine (orb, executors), emscripten-core/emsdk
// (partial orb version, a machine image) and influxdata/influxdb (jobs with merge keys, digest-only
// images).
func TestExtractFixtures(t *testing.T) {
	const (
		d = "docker"
		o = Datasource
	)
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/jasmine.yml", 5, map[int]want{
			0: {line: 7, depT: TypeOrb, ds: o, name: "circleci/windows", version: "5.0.0"},
			1: {line: 12, depT: TypeImage, ds: d, name: "cimg/node", version: "26.0.0"},
		}},
		{"testdata/emsdk.yml", 2, map[int]want{
			0: {line: 4, depT: TypeOrb, ds: o, name: "circleci/windows", version: "5.0"},
			1: {line: 9, depT: TypeImage, ds: d, name: "buildpack-deps", version: "jammy"},
		}},
		{"testdata/influxdb.yml", 14, map[int]want{
			0:  {line: 64, depT: TypeOrb, ds: o, name: "circleci/rust", version: "1.6.1"},
			3:  {line: 251, depT: TypeImage, ds: d, name: "cimg/go", version: "1.25"},
			7:  {line: 374, depT: TypeImage, ds: d, name: "us-east1-docker.pkg.dev/influxdata-team-edge/ci-support/ci-cross-influxdb3", skip: "digest without tag"},
			13: {line: 756, depT: TypeImage, ds: d, name: "busybox", skip: "no tag"},
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
	src := []byte("orbs:\n  node: circleci/node@5\njobs:\n  a:\n    docker:\n      - image: cimg/node:26.0.0\n")
	deps := extract(t, src)
	var edits []patch.Edit
	for _, u := range []dep.Update{{Dep: deps[0], NewVersion: "7"}, {Dep: deps[1], NewVersion: "26.1.0"}} {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply(src, edits)
	if want := "orbs:\n  node: circleci/node@7\njobs:\n  a:\n    docker:\n      - image: cimg/node:26.1.0\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	sha := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "7", NewDigest: sha}); err == nil {
		t.Error("orb digest edit: want error")
	}
	r := New().(manager.Renamer)
	if d, err := r.Rename(deps[1], "ghcr.io/acme/node"); err != nil || d.Source != "ghcr.io/acme/node" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
	if _, err := r.Rename(deps[0], "circleci/other"); err == nil {
		t.Error("orb rename: want error")
	}
}
