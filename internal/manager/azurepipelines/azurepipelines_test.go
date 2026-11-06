package azurepipelines

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                                  int
	depT, ds, name, source, version, skip string
}

const (
	dk = "docker"
	gt = "git-tags"
	at = TasksDatasource
)

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("azure-pipelines.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name {
			t.Errorf("%d: manager %q", i, d.Manager)
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
		if d.DepType != w.depT || d.Datasource != w.ds || d.Line != w.line || d.Name != w.name || d.Source != w.source ||
			d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got %s/%s line=%d name=%q source=%q version=%q skip=%q\nwant %+v",
				i, d.DepType, d.Datasource, d.Line, d.Name, d.Source, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		"azure-pipelines.yml":           true,
		".azure-pipelines.yaml":         true,
		"azure-pipelines/build.yml":     true,
		".azure-pipelines/ci/test.yaml": true,
		"build/azure-pipeline.yml":      true,
		".azuredevops/pipelines/ci.yml": true,
		"sub/.azuredevops/x.yaml":       true,
		"azure-pipelines.json":          false,
		"pipelines/build.yml":           false,
		"azure/main.tf":                 false,
		".azuredevops/pull_request.md":  false,
		"bitbucket-pipelines.yml":       false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`resources:
  repositories:
    - repository: templates
      type: github
      name: org/templates
      ref: refs/tags/v1.2.0
    - repository: main
      type: github
      name: org/branch
      ref: refs/heads/main
    - repository: noref
      type: github
      name: org/noref
    - repository: azure
      type: git
      name: project/repo
      ref: refs/tags/v1.0.0
    - repository: var
      type: github
      name: org/x
      ref: refs/tags/$(tag)
  containers:
    - container: linux
      image: ubuntu:24.04
    - container: py
      image: python:3.14@sha256:1111111111111111111111111111111111111111111111111111111111111111
steps:
  - task: NodeTool@0
  - script: echo hi
  - task: Bash
  - task: ${{ parameters.task }}@1
stages:
  - stage: Build
    jobs:
      - job: A
        steps:
          - task: ms-devlabs.custom-terraform-tasks.custom-terraform-installer-task.TerraformInstaller@1.2.3
      - deployment: Deploy
        strategy:
          runOnce:
            deploy:
              steps:
                - ${{ if eq(variables.x, 'y') }}:
                  - task: AzureCLI@2
extends:
  template: t.yml@templates
  parameters:
    steps:
      - task: UseDotNet@2
`)
	check(t, extract(t, src), 13, map[int]want{
		0:  {line: 6, depT: TypeRepository, ds: gt, name: "org/templates", source: "https://github.com/org/templates", version: "v1.2.0"},
		1:  {line: 10, depT: TypeRepository, ds: gt, name: "org/branch", skip: "non-tag ref"},
		2:  {line: 13, depT: TypeRepository, ds: gt, name: "org/noref", skip: "no ref"},
		3:  {line: 16, depT: TypeRepository, ds: gt, name: "project/repo", skip: "unsupported repository type"},
		4:  {line: 20, depT: TypeRepository, ds: gt, name: "org/x", skip: "variable reference"},
		5:  {line: 24, depT: TypeContainer, ds: dk, name: "ubuntu", source: "docker.io/library/ubuntu", version: "24.04"},
		6:  {line: 26, depT: TypeContainer, ds: dk, name: "python", source: "docker.io/library/python", version: "3.14"},
		7:  {line: 28, depT: TypeTask, ds: at, name: "NodeTool", version: "0"},
		8:  {line: 30, depT: TypeTask, ds: at, name: "Bash", skip: "no version"},
		9:  {line: 31, depT: TypeTask, ds: at, name: "${{ parameters.task }}@1", skip: "variable reference"},
		10: {line: 37, depT: TypeTask, ds: at, name: "ms-devlabs.custom-terraform-tasks.custom-terraform-installer-task.TerraformInstaller", version: "1.2.3"},
		11: {line: 44, depT: TypeTask, ds: at, name: "AzureCLI", version: "2"},
		12: {line: 49, depT: TypeTask, ds: at, name: "UseDotNet", version: "2"},
	})
	if _, err := New().Extract("azure-pipelines.yml", []byte("steps: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
	if deps := extract(t, []byte("")); len(deps) != 0 {
		t.Errorf("deps = %+v", deps)
	}
}

// The fixtures are real-world pipelines: OmniSharp/csharp-language-server-protocol (a GitHub
// template repository pinned to a tag), linux-rdma/rdma-core (container resources, stages) and
// microsoft/aspire (1ES templates: an Azure Repos repository, tasks in extends parameters).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/omnisharp.yml", 3, map[int]want{
			0: {line: 33, depT: TypeRepository, ds: gt, name: "RocketSurgeonsGuild/AzureDevopsTemplates", source: "https://github.com/RocketSurgeonsGuild/AzureDevopsTemplates", version: "v0.13.1"},
			1: {line: 82, depT: TypeTask, ds: at, name: "DotNetCoreCLI", version: "2"},
		}},
		{"testdata/rdma-core.yml", 15, map[int]want{
			0:  {line: 13, depT: TypeContainer, ds: dk, name: "ucfconsort.azurecr.io/rdma-core/azure_pipelines", source: "ucfconsort.azurecr.io/rdma-core/azure_pipelines", version: "44.0"},
			10: {line: 53, depT: TypeTask, ds: at, name: "PythonScript", version: "0"},
			13: {line: 198, depT: TypeTask, ds: at, name: "DownloadPipelineArtifact", version: "2"},
		}},
		{"testdata/aspire.yml", 8, map[int]want{
			0: {line: 111, depT: TypeRepository, ds: gt, name: "1ESPipelineTemplates/1ESPipelineTemplates", skip: "unsupported repository type"},
			1: {line: 329, depT: TypeTask, ds: at, name: "1ES.PublishPipelineArtifact", version: "1"},
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
	src := []byte(`resources:
  repositories:
    - repository: t
      type: github
      name: org/t
      ref: refs/tags/v1.2.0
  containers:
    - container: linux
      image: ubuntu:24.04
steps:
  - task: Bash@3
`)
	deps := extract(t, src)
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[0], NewVersion: "v1.3.0"},
		{Dep: deps[1], NewVersion: "26.04"},
		{Dep: deps[2], NewVersion: "4"},
	} {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	want := `resources:
  repositories:
    - repository: t
      type: github
      name: org/t
      ref: refs/tags/v1.3.0
  containers:
    - container: linux
      image: ubuntu:26.04
steps:
  - task: Bash@4
`
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	sha := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	if _, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "4", NewDigest: sha}); err == nil {
		t.Error("task digest edit: want error")
	}
	r := New().(manager.Renamer)
	if d, err := r.Rename(deps[1], "mcr.microsoft.com/ubuntu"); err != nil || d.Source != "mcr.microsoft.com/ubuntu" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
	if _, err := r.Rename(deps[2], "Other"); err == nil {
		t.Error("task rename: want error")
	}
}
