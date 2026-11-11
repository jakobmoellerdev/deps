package tekton

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                          int
	depT, ds, name, version, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("pipeline.yaml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name {
			t.Errorf("%d: manager %s", i, d.Manager)
		}
		if d.Version != "" && d.Skip == "" && !docker.Untagged(d) && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
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
		if d.DepType != w.depT || d.Datasource != w.ds || d.Line != w.line || d.Name != w.name || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got %s %s line=%d name=%q version=%q skip=%q\nwant %+v", i, d.DepType, d.Datasource, d.Line, d.Name, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		".tekton/pull-request.yaml": true,
		"deploy/pipeline.yml":       true,
		"task.yaml":                 true,
		"Chart.lock":                false,
		"pipeline.json":             false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtractResources(t *testing.T) {
	sha := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	src := []byte(`apiVersion: tekton.dev/v1
kind: StepAction
spec:
  image: alpine:3.22
---
apiVersion: tekton.dev/v1
kind: TaskRun
spec:
  taskRef:
    bundle: docker.io/acme/legacy-task:1.0
  taskSpec:
    steps:
      - image: busybox:1.37
---
apiVersion: tekton.dev/v1
kind: Task
spec:
  stepTemplate:
    image: debian:13
  steps:
    - name: a
      image: golang:1.26@` + sha + `
    - name: b
      ref:
        name: some-step-action
  sidecars:
    - image: docker:29-dind
---
apiVersion: tekton.dev/v1
kind: PipelineRun
spec:
  pipelineRef:
    resolver: bundles
    resource:
      - name: bundle
        value: quay.io/acme/pipeline:0.3
  pipelineSpec:
    tasks:
      - name: by-name
        taskRef:
          name: git-clone
      - name: git-resolver
        taskRef:
          resolver: git
          params:
            - name: url
              value: https://github.com/acme/tasks
    finally:
      - name: notify
        taskRef:
          resolver: bundles
          params:
            - name: name
              value: notify
            - name: bundle
              value: $(params.registry)/notify:1.0
---
apiVersion: v1
kind: List
items:
  - apiVersion: tekton.dev/v1beta1
    kind: Pipeline
    spec:
      tasks:
        - name: inline
          taskSpec:
            steps:
              - image: node:24
  - apiVersion: v1
    kind: Pod
    spec:
      image: not-tekton:1
---
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
spec:
  resourcetemplates:
    - apiVersion: tekton.dev/v1
      kind: TaskRun
      spec:
        taskRef:
          resolver: bundles
          params:
            - name: bundle
              value: ghcr.io/acme/build-task:2.1.0
---
apiVersion: apps/v1
kind: Deployment
spec:
  image: unrelated:1.0
`)
	check(t, extract(t, src), 10, map[int]want{
		0: {line: 4, depT: TypeStepImage, ds: "docker", name: "alpine", version: "3.22"},
		1: {line: 10, depT: TypeBundle, ds: "docker", name: "acme/legacy-task", version: "1.0"},
		2: {line: 13, depT: TypeStepImage, ds: "docker", name: "busybox", version: "1.37"},
		3: {line: 22, depT: TypeStepImage, ds: "docker", name: "golang", version: "1.26"},
		4: {line: 27, depT: TypeStepImage, ds: "docker", name: "docker", version: "29-dind"},
		5: {line: 19, depT: TypeStepImage, ds: "docker", name: "debian", version: "13"},
		6: {line: 36, depT: TypeBundle, ds: "docker", name: "quay.io/acme/pipeline", version: "0.3"},
		7: {line: 56, depT: TypeBundle, ds: "docker", name: "$(params.registry)/notify:1.0", skip: "variable reference"},
		8: {line: 68, depT: TypeStepImage, ds: "docker", name: "node", version: "24"},
		9: {line: 85, depT: TypeBundle, ds: "docker", name: "ghcr.io/acme/build-task", version: "2.1.0"},
	})
}

func TestExtractAnnotations(t *testing.T) {
	src := []byte(`apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  annotations:
    pipelinesascode.tekton.dev/task: "[git-clone, https://github.com/foo/bar/raw/v0.0.1/task/my-task/my-task.yaml, .tekton/tasks/lint.yaml]"
    pipelinesascode.tekton.dev/task-1: https://raw.githubusercontent.com/foo/baz/v1.2.3/task.yaml
    pipelinesascode.tekton.dev/task-2: '[ https://github.com/foo/rel/releases/download/v0.0.1/create-git-tag-task.yaml ]'
    pipelinesascode.tekton.dev/task-3: "https://raw.githubusercontent.com/foo/tags/refs/tags/v2.0.0/task.yaml"
    pipelinesascode.tekton.dev/task-4: "https://raw.githubusercontent.com/foo/main/refs/heads/main/task.yaml"
    pipelinesascode.tekton.dev/task-5: "https://github.com/foo/pinned/raw/0123456789abcdef0123456789abcdef01234567/task.yaml"
    pipelinesascode.tekton.dev/task-6: "https://github.com/foo/var/raw/{{revision}}/task.yaml"
    pipelinesascode.tekton.dev/task-7: "https://github.com/foo/esc/raw/v1.0.0/t\u0061sk.yaml"
    pipelinesascode.tekton.dev/pipeline: github.com/foo/pipe/raw/v3/pipeline.yaml
    pipelinesascode.tekton.dev/task-x: https://github.com/foo/ignored/raw/v1/task.yaml
    pipelinesascode.tekton.dev/max-keep-runs: "5"
    pipelinesascode.tekton.dev/task-8: "[https://gitlab.com/foo/bar/-/raw/v1/t.yaml, https://github.com/foo/bar/blob/v1/t.yaml, https://github.com/foo/bar/releases/download/v1/, https://github.com/foo/bar/raw/v1/, https://raw.githubusercontent.com/foo/bar/refs/pull/1/t.yaml, https://github.com/foo/bar/raw/v1/t.yaml?x=1]"
spec: {}
`)
	deps := extract(t, src)
	check(t, deps, 9, map[int]want{
		0: {line: 5, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/bar", version: "v0.0.1"},
		1: {line: 6, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/baz", version: "v1.2.3"},
		2: {line: 7, depT: TypeAnnotation, ds: "github-releases", name: "foo/rel", version: "v0.0.1"},
		3: {line: 8, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/tags", version: "v2.0.0"},
		4: {line: 9, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/main", skip: "non-version ref"},
		5: {line: 10, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/pinned", skip: "commit ref"},
		6: {line: 11, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/var", skip: "variable reference"},
		7: {line: 12, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/esc", version: "v1.0.0", skip: "cannot locate value"},
		8: {line: 13, depT: TypeAnnotation, ds: "git-tags", name: "github.com/foo/pipe", version: "v3"},
	})
	if deps[2].Source != "https://github.com/foo/rel" || deps[0].Source != "https://github.com/foo/bar" {
		t.Errorf("sources: %q, %q", deps[2].Source, deps[0].Source)
	}
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{
		"",
		"apiVersion: v1\nkind: ConfigMap\n",
		"- tekton.dev\n",
		"apiVersion: tekton.dev/v1\nkind: Task\nspec: [a]\n",
		"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  annotations: [pipelinesascode.tekton.dev/task]\nspec:\n  steps: {image: a}\n  taskRef: x\n",
		"apiVersion: tekton.dev/v1\nkind: PipelineRun\nmetadata:\n  annotations:\n    pipelinesascode.tekton.dev/task: \"[]\"\n    pipelinesascode.tekton.dev/task-1: \"[\"\n    pipelinesascode.tekton.dev/task-2: \"]\"\n",
		"apiVersion: nottekton.dev/v1\nkind: Task\nspec:\n  steps:\n    - image: alpine:3.22\n",
		// A Helm template rendering Tekton resources does not parse: no deps, no error.
		"apiVersion: tekton.dev/v1\nkind: Task\nspec:\n  steps:\n    - image: {{ .Values.image }}\n",
	} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
}

// TestExtractUnparsableDocument: a document that does not parse ends the file, but the
// documents before it keep their deps.
func TestExtractUnparsableDocument(t *testing.T) {
	src := []byte("apiVersion: tekton.dev/v1\nkind: StepAction\nspec:\n  image: alpine:3.22\n" +
		"---\napiVersion: tekton.dev/v1\nkind: Task\nspec: [\n")
	check(t, extract(t, src), 1, map[int]want{
		0: {line: 4, depT: TypeStepImage, ds: "docker", name: "alpine", version: "3.22"},
	})
}

func TestPacKey(t *testing.T) {
	for k, ok := range map[string]bool{
		"pipelinesascode.tekton.dev/task":          true,
		"pipelinesascode.tekton.dev/task-12":       true,
		"pipelinesascode.tekton.dev/pipeline":      true,
		"pipelinesascode.tekton.dev/task-":         false,
		"pipelinesascode.tekton.dev/task-a":        false,
		"pipelinesascode.tekton.dev/pipeline-1":    false,
		"pipelinesascode.tekton.dev/max-keep-runs": false,
		"tekton.dev/task":                          false,
	} {
		if got := pacKey(k); got != ok {
			t.Errorf("pacKey(%q) = %v, want %v", k, got, ok)
		}
	}
}

// The fixtures are real Tekton files: Konflux PipelineRuns with bundles-resolver task
// references (konflux-ci/build-service, konflux-ci/build-definitions, the latter with inline
// taskSpecs and a PaC annotation of repository files), Pipelines-as-Code's own PipelineRuns
// (a raw.githubusercontent.com pipeline on a branch, inline step images), tektoncd/catalog's
// kind Task (sidecars) and gcs sample (a TriggerTemplate in a multi-document file) and a
// Konflux StepAction.
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/build-service-pull-request.yaml", 16, map[int]want{
			0:  {line: 158, depT: TypeBundle, ds: "docker", name: "quay.io/konflux-ci/tekton-catalog/task-init", version: "0.4.3"},
			4:  {line: 283, depT: TypeBundle, ds: "docker", name: "quay.io/konflux-ci/tekton-catalog/task-build-image-index", version: "0.4"},
			15: {line: 545, depT: TypeBundle, ds: "docker", name: "quay.io/konflux-ci/tekton-catalog/task-rpms-signature-scan", version: "0.2.2"},
		}},
		{"testdata/build-definitions-pull-request.yaml", 5, map[int]want{
			0: {line: 33, depT: TypeBundle, ds: "docker", name: "quay.io/konflux-ci/tekton-catalog/task-git-clone", version: "0.2.4"},
			1: {line: 76, depT: TypeStepImage, ds: "docker", name: "$(params.image-target)", skip: "variable reference"},
			2: {line: 84, depT: TypeStepImage, ds: "docker", name: "registry.access.redhat.com/ubi9/python-39", version: "9.7-1776644933"},
			4: {line: 224, depT: TypeStepImage, ds: "docker", name: "registry.redhat.io/openshift4/ose-cli", version: "v4.12"},
		}},
		{"testdata/pac-boussole.yaml", 1, map[int]want{
			0: {line: 6, depT: TypeAnnotation, ds: "git-tags", name: "github.com/pipelines-as-code/pac-boussole", skip: "non-version ref"},
		}},
		{"testdata/pac-go.yaml", 2, map[int]want{
			0: {line: 70, depT: TypeStepImage, ds: "docker", name: "golang", version: "1.27"},
			1: {line: 94, depT: TypeStepImage, ds: "docker", name: "golangci/golangci-lint", version: "v2.15.0"},
		}},
		{"testdata/catalog-kind.yaml", 2, map[int]want{
			0: {line: 42, depT: TypeStepImage, ds: "docker", name: "$(params.image)", skip: "variable reference"},
			1: {line: 55, depT: TypeStepImage, ds: "docker", name: "docker", version: "18.05-dind"},
		}},
		{"testdata/catalog-gcs-triggertemplate.yaml", 2, map[int]want{
			0: {line: 46, depT: TypeStepImage, ds: "docker", name: "ubuntu", version: "latest"},
			1: {line: 98, depT: TypeStepImage, ds: "docker", name: "ubuntu", version: "latest"},
		}},
		{"testdata/stepaction-eaas.yaml", 1, map[int]want{
			0: {line: 9, depT: TypeStepImage, ds: "docker", name: "registry.redhat.io/openshift4/ose-cli", version: "latest"},
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			deps := extract(t, src)
			check(t, deps, tt.n, tt.wants)
			// The docker manager reads every YAML file too: it must not extract the same values.
			dd, err := docker.New().Extract(tt.file, src, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range dd {
				for _, d := range deps {
					if d.Line == o.Line && d.Name == o.Name {
						t.Errorf("line %d %s extracted by both tekton and docker", d.Line, d.Name)
					}
				}
			}
		})
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  annotations:
    pipelinesascode.tekton.dev/task: "[git-clone, https://github.com/foo/bar/raw/v0.0.1/task.yaml, https://github.com/foo/rel/releases/download/v0.0.1/t.yaml]"
spec:
  pipelineRef:
    resolver: bundles
    params:
      - name: bundle
        value: quay.io/acme/pipeline:0.3@sha256:1111111111111111111111111111111111111111111111111111111111111111
`)
	deps := extract(t, src)
	if len(deps) != 3 {
		t.Fatalf("deps = %+v", deps)
	}
	sha := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[0], NewVersion: "v0.1.0"},
		{Dep: deps[1], NewVersion: "v0.2.0"},
		{Dep: deps[2], NewVersion: "0.4", NewDigest: sha},
	} {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatalf("%s: %v", u.Dep.Name, err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer(
		"bar/raw/v0.0.1/", "bar/raw/v0.1.0/",
		"download/v0.0.1/", "download/v0.2.0/",
		"pipeline:0.3@sha256:1111111111111111111111111111111111111111111111111111111111111111", "pipeline:0.4@"+sha,
	).Replace(string(src))
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if again := extract(t, got); len(again) != 3 || again[0].Version != "v0.1.0" || again[1].Version != "v0.2.0" || again[2].Version != "0.4" || again[2].Digest != sha {
		t.Errorf("re-extracted %+v", again)
	}

	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "v0.1.0", NewDigest: sha}); err == nil {
		t.Error("want error pinning an annotation ref")
	}
	d, err := New().(manager.Renamer).Rename(deps[2], "ghcr.io/acme/pipeline")
	if err != nil || d.Source != "ghcr.io/acme/pipeline" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
	if _, err := New().(manager.Renamer).Rename(deps[0], "x"); err == nil {
		t.Error("want error renaming an annotation")
	}
}
