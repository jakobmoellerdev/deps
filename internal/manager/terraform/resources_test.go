package terraform

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures, copied unchanged (renamed):
//   - testdata/docker: github.com/kreuzwerker/terraform-provider-docker@f1624b4 (MPL-2.0):
//     examples/ssh-protocol/main.tf, examples/data-sources/docker_registry_image/data-source.tf,
//     examples/resources/docker_service/resource-advanced.tf,
//     examples/resources/docker_container/import-resource.tf,
//     examples/provider/provider-credentials.tf.
//   - testdata/kubernetes: github.com/hashicorp/terraform-provider-kubernetes@f76a1ae (MPL-2.0):
//     examples/resources/{stateful_set_v1,cron_job_v1}/example_1.tf,
//     examples/resources/pod_v1/example_2.tf, _examples/wordpress-mysql-gce-pv/wordpress.tf,
//     _examples/cross-cluster/main.tf.
//   - testdata/helm: github.com/hashicorp/terraform-provider-helm@0e0a60f (MPL-2.0):
//     examples/example_1.tf, examples/resources/release/example_{1,2,3,4,7}.tf.
//   - testdata/governance: github.com/jamesrcounts/devops-governance@78874b4 (MIT):
//     workspaces/root/modules/workspace-kubernetes/main.tf.
//   - testdata/runners: github.com/terraform-google-modules/terraform-google-github-actions-runners@a05d439
//     (Apache-2.0): modules/gh-runner-gke/main.tf.

func TestExtractResourcesRealWorld(t *testing.T) {
	const (
		tr = "https://registry.terraform.io"
		tf = "hashicorp/terraform https://github.com/hashicorp/terraform"
	)
	for _, tt := range []struct {
		file string
		want []string
	}{
		{"docker/ssh-protocol.tf", []string{
			"3 provider terraform-provider hashicorp/docker " + tr + " 1.2.0 hashicorp",
			"11 docker_image docker busybox docker.io/library/busybox latest docker",
			"20 provider terraform-provider hashicorp/docker " + tr + " 1.2.0 hashicorp",
			"24 docker_image docker docker docker.io/library/docker 18.09.0-dind docker",
			"33 docker_container docker docker docker.io/library/docker 18.09.0-dind docker",
		}},
		{"docker/registry-image.tf", []string{
			"2 docker_registry_image docker ubuntu docker.io/library/ubuntu precise docker",
		}},
		{"docker/service-advanced.tf", []string{
			"29 docker_service docker repo.mycompany.com:8080/foo-service repo.mycompany.com:8080/foo-service v1 docker",
		}},
		{"docker/container-import.tf", []string{
			"3 docker_container docker nginx docker.io/library/nginx latest docker",
		}},
		{"docker/provider-credentials.tf", []string{
			"22 docker_registry_image docker myorg/privateimage docker.io/myorg/privateimage latest docker",
			"26 docker_registry_image docker quay.io:8181/myorg/privateimage quay.io:8181/myorg/privateimage latest docker",
		}},
		{"kubernetes/stateful_set_v1.tf", []string{
			"44 kubernetes_stateful_set_v1 docker busybox docker.io/library/busybox latest docker",
			"57 kubernetes_stateful_set_v1 docker jimmidyson/configmap-reload docker.io/jimmidyson/configmap-reload v0.1 docker",
			"86 kubernetes_stateful_set_v1 docker prom/prometheus docker.io/prom/prometheus v2.2.1 docker",
		}},
		{"kubernetes/cron_job_v1.tf", []string{
			"22 kubernetes_cron_job_v1 docker busybox docker.io/library/busybox latest docker",
		}},
		{"kubernetes/pod_v1.tf", []string{
			"35 kubernetes_pod_v1 docker k8s.gcr.io/pause k8s.gcr.io/pause 2.0 docker",
		}},
		{"kubernetes/wordpress.tf", nil}, // the image is a template
		{"kubernetes/cross-cluster.tf", []string{ // the provider block in a heredoc is text
			"15 provider terraform-provider hashicorp/kubernetes " + tr + " 1.10.0 hashicorp",
			"56 kubernetes_job docker hashicorp/terraform docker.io/hashicorp/terraform 0.12.13 docker",
		}},
		{"helm/example_1.tf", []string{
			"23 helm_release helm nginx-ingress-controller https://charts.bitnami.com/bitnami - semver [no version]",
		}},
		{"helm/release_1.tf", []string{
			"5 helm_release helm redis https://charts.bitnami.com/bitnami 6.0.1 semver",
		}},
		{"helm/release_2.tf", []string{
			"3 helm_release helm ./charts/example - - semver [local chart]",
		}},
		{"helm/release_3.tf", []string{
			"3 helm_release helm https://charts.bitnami.com/bitnami/redis-10.7.16.tgz - - semver [local chart]",
		}},
		{"helm/release_4.tf", []string{
			"20 helm_release helm test-chart oci://localhost:5000/helm-charts 1.2.3 semver",
		}},
		{"helm/release_7.tf", []string{
			"6 helm_release helm bitnami/redis - - semver [repository alias]",
		}},
		{"runners/gh-runner-gke.tf", []string{
			"54 module terraform-module terraform-google-modules/kubernetes-engine/google " + tr + " 35.0 hashicorp",
			"120 helm_release helm gha-runner-scale-set-controller oci://ghcr.io/actions/actions-runner-controller-charts - semver [non-literal value]",
			"129 helm_release helm gha-runner-scale-set oci://ghcr.io/actions/actions-runner-controller-charts - semver [non-literal value]",
		}},
		{"governance/workspace-kubernetes.tf", []string{
			"10 tfe_workspace github-releases " + tf + " 1.3.6 hashicorp",
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			got := extract(t, New(), tt.file, read(t, tt.file))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestExtractResourceForms(t *testing.T) {
	src := `terraform {
  required_providers {
    kubectl = { source = "gavinbunney/kubectl", version = "~> 1.14" }
    dyn     = { source = local.src }
    bad     = { source = "a/b/c/d" }
    legacy  = "~> 1.0"
  }
}

provider "kubectl" {
  version = "1.14.0"
}
provider "dyn" { version = "1.0.0" }
provider "bad" { version = "1.0.0" }
provider "legacy" { version = "~> 2.1" }
provider "aws" {
  alias   = "east"
  version = var.v
}
provider "google" { region = "x" }

resource "helm_release" "oci" {
  chart   = "oci://registry-1.docker.io/bitnamicharts/redis"
  version = "19.5.0"
}
resource "helm_release" "nohost" { chart = "oci://nohost" }
resource "helm_release" "slash" {
  repository = "https://charts.jetstack.io/"
  chart      = "cert-manager"
  version    = "v1.14.0"
}
resource "helm_release" "plain" {
  repository = "http://charts.example.com"
  chart      = "plain"
  version    = "1.0.0-rc.1+b5"
}
resource "helm_release" "range" {
  repository = "https://charts.example.com"
  chart      = "range"
  version    = "~1.2.0"
}
resource "helm_release" "partial" {
  repository = "https://charts.example.com"
  chart      = "partial"
  version    = "1.2"
}
resource "helm_release" "empty" {
  repository = "https://charts.example.com"
  chart      = "empty"
  version    = ""
}
resource "helm_release" "esc" {
  repository = "https://charts.example.com"
  chart      = "esc"
  version    = "1.0\u002e0"
}
resource "helm_release" "gcs" {
  repository = "gs://bucket/charts"
  chart      = "gcs"
  version    = "1.0.0"
}
resource "helm_release" "dynrepo" {
  repository = var.repo
  chart      = "dynrepo"
}
resource "helm_release" "dynchart" { chart = var.chart }
resource "helm_release" "abs" { chart = "/charts/abs" }
resource "helm_release" "bare" {
  chart   = "bare"
  version = "1.0.0"
}
resource "helm_release" "nochart" { name = "x" }

resource "tfe_workspace" "fixed" { terraform_version = "1.9.8" }
resource "tfe_workspace" "dyn" { terraform_version = var.tf }
resource "tfe_workspace" "unset" { name = "x" }

resource "docker_image" "pinned" {
  name = "nginx:1.25.3@sha256:0000000000000000000000000000000000000000000000000000000000000000"
}
resource "docker_image" "digest" {
  name = "nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"
}
resource "docker_image" "ref" { name = data.docker_registry_image.x.name }
resource "docker_image" "esc" { name = "nginx:1.25\u002e3" }
resource "docker_image" "bad" { name = "Bad Ref" }
resource "docker_container" "c" { image = "ghcr.io/org/app:2.0.0" }
resource "docker_service" "s" {
  task_spec {
    container_spec { image = "redis:7.2" }
  }
  image = "not:checked"
}
data "docker_registry_image" "r" { name = "alpine:3.20" }
data "docker_image" "d" { name = "alpine:3.19" }
resource "kubernetes_deployment_v1" "k" {
  spec {
    template {
      spec {
        init_container { image = "busybox:1.36" }
        container {
          image = "nginx:1.27.0"
          env { image = "not:checked" }
        }
        container { image = var.image }
      }
    }
  }
}
resource "kubernetes_service_v1" "svc" {
  spec {
    container { image = "not:checked" }
  }
}
resource "aws_instance" "x" { image = "not:checked" }
`
	const (
		tr = "https://registry.terraform.io"
		ex = "https://charts.example.com"
	)
	want := []string{
		"11 provider terraform-provider gavinbunney/kubectl " + tr + " 1.14.0 hashicorp",
		"13 provider terraform-provider dyn - - - [non-literal source]",
		"14 provider terraform-provider a/b/c/d - - - [invalid provider source]",
		"15 provider terraform-provider hashicorp/legacy " + tr + " 2.1 hashicorp",
		"18 provider terraform-provider hashicorp/aws " + tr + " - - [non-literal value]",
		"24 helm_release helm redis oci://registry-1.docker.io/bitnamicharts 19.5.0 semver",
		"26 helm_release helm oci://nohost - - semver [invalid chart reference]",
		"30 helm_release helm cert-manager https://charts.jetstack.io v1.14.0 semver",
		"35 helm_release helm plain http://charts.example.com 1.0.0-rc.1+b5 semver",
		"40 helm_release helm range " + ex + " 1.2.0 semver range=~1.2.0",
		"45 helm_release helm partial " + ex + " 1.2.0 semver range=1.2",
		"50 helm_release helm empty " + ex + " - semver [no version]",
		"55 helm_release helm esc " + ex + " 1.0.0 semver [cannot locate value]",
		"59 helm_release helm gcs - - semver [unknown chart repository gs://bucket/charts]",
		"64 helm_release helm dynrepo - - semver [non-literal value]",
		"66 helm_release helm dynchart - - semver [non-literal value]",
		"67 helm_release helm /charts/abs - - semver [local chart]",
		"69 helm_release helm bare - - semver [local chart]",
		"74 tfe_workspace github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.9.8 hashicorp",
		"75 tfe_workspace github-releases hashicorp/terraform https://github.com/hashicorp/terraform - - [non-literal value]",
		"79 docker_image docker nginx docker.io/library/nginx 1.25.3 docker",
		"82 docker_image docker nginx docker.io/library/nginx latest docker",
		"85 docker_image docker nginx:1.25.3 - - docker [cannot locate value]",
		"86 docker_image docker Bad Ref - - docker [invalid reference]",
		"87 docker_container docker ghcr.io/org/app ghcr.io/org/app 2.0.0 docker",
		"90 docker_service docker redis docker.io/library/redis 7.2 docker",
		"94 docker_registry_image docker alpine docker.io/library/alpine 3.20 docker",
		"100 kubernetes_deployment_v1 docker busybox docker.io/library/busybox 1.36 docker",
		"102 kubernetes_deployment_v1 docker nginx docker.io/library/nginx 1.27.0 docker",
	}
	b := []byte(src)
	deps, err := New().Extract("main.tf", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	deps = slices.DeleteFunc(deps, func(d dep.Dep) bool { return d.DepType == DepTypeProvider })
	if got := summarize(t, b, deps); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, d := range deps {
		if d.Manager != Name || d.File != "main.tf" {
			t.Errorf("%s: manager %q file %q", d.Name, d.Manager, d.File)
		}
		if d.Digest != "" && string(b[d.DigestSpan.Start:d.DigestSpan.End]) != d.Digest {
			t.Errorf("%s: DigestSpan covers %q", d.Name, b[d.DigestSpan.Start:d.DigestSpan.End])
		}
		if d.Datasource == "docker" && d.Skip == "" && b[d.InsertAt] != '@' && b[d.InsertAt] != '"' {
			t.Errorf("%s: InsertAt %d is not after the reference", d.Name, d.InsertAt)
		}
	}
}

func TestResourceEdits(t *testing.T) {
	const (
		old = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		dig = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	)
	for _, tt := range []struct {
		file string
		src  []byte
		upd  func(d dep.Dep) dep.Update // zero NewVersion: no update
		want []string                   // changed lines of the patched file
	}{
		{"docker/ssh-protocol.tf", nil, func(d dep.Dep) dep.Update {
			switch {
			case d.DepType == DepTypeProviderBlock && d.Line == 3:
				return dep.Update{Dep: d, NewVersion: "3.6.2"}
			case d.Name == "busybox":
				return dep.Update{Dep: d, NewVersion: d.Version, NewDigest: dig}
			case d.DepType == "docker_container":
				return dep.Update{Dep: d, NewVersion: "27.3.1-dind"}
			}
			return dep.Update{}
		}, []string{
			`  version = "~> 3.6.2"`,
			`  name     = "busybox:latest@` + dig + `"`,
			`  image = "docker:27.3.1-dind"`,
		}},
		{"helm/release_4.tf", nil, func(d dep.Dep) dep.Update {
			return dep.Update{Dep: d, NewVersion: "1.3.0"}
		}, []string{`  version     = "1.3.0"`}},
		{"kubernetes/pod_v1.tf", nil, func(d dep.Dep) dep.Update {
			return dep.Update{Dep: d, NewVersion: "3.10"}
		}, []string{`      image = "k8s.gcr.io/pause:3.10"`}},
		{"main.tf", []byte("provider \"aws\" { version = \"~> 5.0\" }\n" +
			"resource \"docker_image\" \"x\" { name = \"nginx:1.25.3@" + old + "\" }\n"),
			func(d dep.Dep) dep.Update {
				if d.Datasource == "docker" {
					return dep.Update{Dep: d, NewVersion: "1.27.2", NewDigest: dig}
				}
				return dep.Update{Dep: d, NewVersion: "6.2.1"}
			}, []string{
				`provider "aws" { version = "~> 6.2" }`,
				`resource "docker_image" "x" { name = "nginx:1.27.2@` + dig + `" }`,
			}},
		{"helm.tf", []byte("resource \"helm_release\" \"x\" {\n  repository = \"https://charts.example.com\"\n  chart      = \"x\"\n  version    = \">= 1.2, < 2\"\n}\n"),
			func(d dep.Dep) dep.Update {
				return dep.Update{Dep: d, NewVersion: "2.1.0"}
			}, []string{`  version    = ">= 2.1, < 3"`}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src := tt.src
			if src == nil {
				src = read(t, tt.file)
			}
			deps, err := New().Extract(tt.file, src, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			var edits []patch.Edit
			for _, d := range deps {
				u := tt.upd(d)
				if u.NewVersion == "" {
					continue
				}
				e, err := New().Edits(u)
				if err != nil {
					t.Fatalf("%s: %v", d.Name, err)
				}
				edits = append(edits, e...)
			}
			out, err := patch.Apply(src, edits)
			if err != nil {
				t.Fatal(err)
			}
			before, after := strings.Split(string(src), "\n"), strings.Split(string(out), "\n")
			if len(before) != len(after) {
				t.Fatalf("line count %d, want %d", len(after), len(before))
			}
			var got []string
			for i := range after {
				if after[i] != before[i] {
					got = append(got, after[i])
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("changed lines\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestResourceEditsUnpin(t *testing.T) {
	src := []byte(`resource "docker_container" "x" { image = "nginx:1.25.3@sha256:0000000000000000000000000000000000000000000000000000000000000000" }`)
	deps, err := New().Extract("main.tf", src, manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatalf("deps %v, err %v", deps, err)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "1.27.2"}); err == nil {
		t.Error("unpinning a digest: no error")
	}
}
