package cloudbuild

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                        int
	name, version, digest, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("cloudbuild.yaml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.DepType != TypeImage || d.Datasource != "docker" || d.Versioning != "docker" {
			t.Errorf("%d: %s/%s/%s/%s", i, d.Manager, d.DepType, d.Datasource, d.Versioning)
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
		if d.Line != w.line || d.Name != w.name || d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip {
			t.Errorf("%d: got line=%d name=%q version=%q digest=%q skip=%q\nwant %+v", i, d.Line, d.Name, d.Version, d.Digest, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		"cloudbuild.yaml":                    true,
		"cloudbuild.yml":                     true,
		"images/base/cloudbuild.yaml":        true,
		"templates/cloudbuild.yaml.hbs":      false,
		"cloudbuild.json":                    false,
		"ci/my-cloudbuild.yaml":              false,
		"cloudbuild/config.yaml":             false,
		"deploy/cloudbuild.yaml/values.yaml": false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`steps:
- name: gcr.io/cloud-builders/docker
  args: [build, .]
- id: test
  name: 'golang:1.26'
- name: ${_REPOSITORY}/tool:${_TAG}
- name: "node:24@sha256:1111111111111111111111111111111111111111111111111111111111111111"
- entrypoint: bash
- name: [list]
images:
- gcr.io/$PROJECT_ID/app
options:
  pool:
    name: projects/p/locations/l/workerPools/w
`)
	check(t, extract(t, src), 4, map[int]want{
		0: {line: 2, name: "gcr.io/cloud-builders/docker", version: "latest"},
		1: {line: 5, name: "golang", version: "1.26"},
		2: {line: 6, name: "${_REPOSITORY}/tool:${_TAG}", skip: "variable reference"},
		3: {line: 7, name: "node", version: "24", digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111"},
	})
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{"", "- a\n", "steps: foo\n", "steps: {name: x}\n"} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
	if _, err := New().Extract("cloudbuild.yaml", []byte("steps: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixtures are real Cloud Build configurations: kubernetes-sigs/cluster-api (a builder
// pinned to a digest only), GoogleCloudPlatform/microservices-demo and google/fhir-data-pipes
// (substituted images, untagged builders, a worker pool `name` that is no step).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/cluster-api.yaml", 1, map[int]want{
			0: {line: 10, name: "registry.k8s.io/releng/gcb-docker-gcloud", version: "latest", digest: "sha256:b2651f57b579d925a243e308c41546d4fd895f705de091a5a8a39060e6192604"},
		}},
		{"testdata/microservices-demo.yaml", 1, map[int]want{
			0: {line: 30, name: "gcr.io/k8s-skaffold/skaffold", version: "v2.16.1"},
		}},
		{"testdata/fhir-data-pipes.yaml", 37, map[int]want{
			0:  {line: 22, name: "docker/compose", version: "latest"},
			4:  {line: 53, name: "maven", version: "3.8.5-openjdk-17"},
			6:  {line: 69, name: "${_REPOSITORY}/synthea-uploader:${_TAG}", skip: "variable reference"},
			18: {line: 183, name: "postgres", version: "latest"},
			36: {line: 347, name: "docker/compose", version: "latest"},
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
	src := []byte("steps:\n- name: 'gcr.io/k8s-skaffold/skaffold:v2.16.1'\n")
	deps := extract(t, src)
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "v2.17.0"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "steps:\n- name: 'gcr.io/k8s-skaffold/skaffold:v2.17.0'\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	d, err := New().(manager.Renamer).Rename(deps[0], "ghcr.io/acme/skaffold")
	if err != nil || d.Source != "ghcr.io/acme/skaffold" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
}
