package terraform

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures, copied unchanged:
//   - testdata/eks: github.com/terraform-aws-modules/terraform-aws-eks@91b8a8d (Apache-2.0):
//     versions.tf, main.tf and examples/eks-hybrid-nodes/remote.tf.
//   - testdata/moj: github.com/ministryofjustice/cloud-platform-infrastructure@921d44f (MIT):
//     terraform/aws-accounts/cloud-platform-aws/account/{versions.tf,ecr.tf,.terraform.lock.hcl}
//     and .tflint.hcl (lock and tflint files renamed without the leading dot).

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// summarize renders each dep as "<line> <depType> <datasource> <name> <source> <version> <versioning> [<skip>]"
// and checks that RefSpan covers the version (ranges that are never rewritten have none) and
// NameParts their text.
func summarize(t *testing.T, src []byte, deps []dep.Dep) []string {
	t.Helper()
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		s := fmt.Sprintf("%d %s %s %s %s %s %s", d.Line, d.DepType, dash(d.Datasource), d.Name, dash(d.Source), dash(d.Version), dash(d.Versioning))
		switch {
		case d.Skip != "":
			s += " [" + d.Skip + "]"
		case d.RangeFixed:
			if d.RefSpan != (dep.Span{}) {
				t.Errorf("%s: fixed range with a RefSpan", s)
			}
		default:
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
				t.Errorf("%s: RefSpan covers %q", s, got)
			}
		}
		for _, p := range d.NameParts {
			if got := string(src[p.Span.Start:p.Span.End]); got != p.Text || p.Role != dep.NameWhole {
				t.Errorf("%s: name part %+v covers %q", s, p, got)
			}
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func extract(t *testing.T, m manager.Manager, file string, src []byte) []string {
	t.Helper()
	deps, err := m.Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	for _, d := range deps {
		if d.Manager != m.Name() || d.File != file {
			t.Errorf("%s: manager %q file %q", d.Name, d.Manager, d.File)
		}
	}
	return summarize(t, src, deps)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func TestExtractRealWorld(t *testing.T) {
	const tr = "https://registry.terraform.io"
	for _, tt := range []struct {
		file string
		want []string
	}{
		{"eks/versions.tf", []string{
			"2 required_version github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.5.7 hashicorp",
			"5 required_provider terraform-provider hashicorp/aws " + tr + " 6.59 hashicorp",
			"9 required_provider terraform-provider hashicorp/tls " + tr + " 4.0 hashicorp",
			"13 required_provider terraform-provider hashicorp/time " + tr + " 0.9 hashicorp",
		}},
		{"eks/main.tf", []string{
			"401 module terraform-module terraform-aws-modules/kms/aws " + tr + " 4.0.0 hashicorp",
		}},
		{"eks/remote.tf", []string{
			"11 module - ../../modules/hybrid-node-role - - - [local module]",
			"32 module terraform-module terraform-aws-modules/key-pair/aws " + tr + " 2.0 hashicorp",
			"211 helm_release helm cilium https://helm.cilium.io 1.16.4 semver",
			"256 module terraform-module terraform-aws-modules/vpc/aws " + tr + " 6.0 hashicorp",
		}},
		{"moj/versions.tf", []string{
			"2 required_version github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.2.5 hashicorp",
			"4 required_provider terraform-provider hashicorp/aws " + tr + " 5.83.0 hashicorp",
			"8 required_provider terraform-provider auth0/auth0 " + tr + " 1.9.1 hashicorp",
			"12 required_provider terraform-provider phillbaker/elasticsearch " + tr + " 2.0.7 hashicorp",
			"16 required_provider terraform-provider opensearch-project/opensearch " + tr + " 2.3.1 hashicorp",
			"20 required_provider terraform-provider hashicorp/http " + tr + " 3.4.1 hashicorp",
			"24 required_provider terraform-provider integrations/github " + tr + " 6.4.0 hashicorp",
			"28 required_provider terraform-provider hashicorp/null " + tr + " 3.2.3 hashicorp",
			"32 required_provider terraform-provider hashicorp/random " + tr + " 3.6.3 hashicorp",
		}},
		{"moj/ecr.tf", []string{
			"2 module git-tags github.com/ministryofjustice/cloud-platform-terraform-ecr-credentials https://github.com/ministryofjustice/cloud-platform-terraform-ecr-credentials 7.1.0 semver",
			"63 module git-tags github.com/ministryofjustice/cloud-platform-terraform-ecr-credentials https://github.com/ministryofjustice/cloud-platform-terraform-ecr-credentials 7.1.0 semver",
			"124 module git-tags github.com/ministryofjustice/cloud-platform-terraform-ecr-credentials https://github.com/ministryofjustice/cloud-platform-terraform-ecr-credentials 7.1.0 semver",
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

func TestExtractForms(t *testing.T) {
	src := `# comment with terraform { required_version = "1.0.0" }
terraform {
  required_version = "~> 1.9" // pinned minor
  required_providers {
    aws = "~> 2.0" # legacy string form
    google = {
      source = "registry.opentofu.org/hashicorp/google"
      version = "= 6.1.0"
    }
    quoted = { "source" = "example/quoted", version = "~>1.2.3" }
    tfe = { source = "app.terraform.io/acme/tfe" }
    builtin = { source = "terraform.io/builtin/terraform" }
    bad = { source = "a/b/c/d", version = "1.0.0" }
    expr = { source = local.src, version = "1.0.0" }
    vexpr = { source = "x/vexpr", version = var.v }
    interp = { source = "x/interp", version = "${var.v}" }
    esc = { source = "x/esc", version = "1.0\u002e0" }
    wide = { source = "x/wide", version = ">= 1.0, < 2.0" }
    junk = { source = "x/junk", version = "latest" }
    other = merge(local.a, { b = 1 })
    for = { for k, v in local.m : k => v }
  }
}

terraform {
  required_version = ">= 1.0"
}

module "git" {
  source = "git::https://example.com/org/repo.git//modules/x?depth=1&ref=v1.2.3"
}
module "ssh" {
  source = "git::ssh://git@github.com:22/org/repo.git?ref=v2"
}
module "scp" {
  source = "git@github.com:org/scp.git?ref=1.0.0"
}
module "sha" {
  source = "github.com/org/sha?ref=0123456789abcdef0123456789abcdef01234567"
}
module "noref" {
  source = "bitbucket.org/org/noref"
}
module "enc" {
  source = "github.com/org/enc?ref=release%2F1"
}
module "subdir" {
  source  = "hashicorp/consul/aws//modules/consul-cluster"
  version = "0.11.0"
}
module "private" {
  source  = "app.terraform.io/Acme/vpc/aws"
  version = "~> 1"
}
module "nover" {
  source = "terraform-aws-modules/vpc/aws"
}
module "archive" {
  source = "https://example.com/vpc-module.zip?ref=1.0.0"
}
module "s3" {
  source = "s3::https://s3-eu-west-1.amazonaws.com/bucket/vpc.zip"
}
module "dyn" {
  source = local.src
}
module "nosource" {
  count = 0
}
resource "x" "y" {
  v = <<-EOT
    module "fake" {
      source = "terraform-aws-modules/vpc/aws"
    EOT
  w = [for s in var.l : upper(s) if s != "}"]
  z = "a ${jsonencode({ k = "}" })} b"
  o = { "a" : 1, b = [1, 2] }
}
`
	const tr = "https://registry.terraform.io"
	want := []string{
		"3 required_version github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.9 hashicorp",
		"5 required_provider terraform-provider hashicorp/aws " + tr + " 2.0 hashicorp",
		"6 required_provider terraform-provider hashicorp/google https://registry.opentofu.org 6.1.0 hashicorp",
		"10 required_provider terraform-provider example/quoted " + tr + " 1.2.3 hashicorp",
		"11 required_provider terraform-provider acme/tfe https://app.terraform.io - - [no version constraint]",
		"12 required_provider terraform-provider builtin/terraform https://terraform.io - - [builtin provider]",
		"13 required_provider terraform-provider a/b/c/d - - - [invalid provider source]",
		"14 required_provider terraform-provider expr - - - [non-literal source]",
		"15 required_provider terraform-provider x/vexpr " + tr + " - - [non-literal value]",
		"16 required_provider terraform-provider x/interp " + tr + " - - [non-literal value]",
		"17 required_provider terraform-provider x/esc " + tr + " 1.0.0 - [cannot locate value]",
		"18 required_provider terraform-provider x/wide " + tr + " 1.0 hashicorp",
		"19 required_provider terraform-provider x/junk " + tr + " latest hashicorp [unsupported constraint]",
		"20 required_provider terraform-provider other - - - [non-literal value]",
		"21 required_provider terraform-provider for - - - [non-literal value]",
		"26 required_version github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.0 hashicorp",
		"30 module git-tags example.com/org/repo https://example.com/org/repo.git v1.2.3 semver",
		"33 module git-tags github.com/org/repo https://github.com/org/repo.git v2 tag",
		"36 module git-tags github.com/org/scp https://github.com/org/scp.git 1.0.0 semver",
		"39 module git-tags github.com/org/sha https://github.com/org/sha 0123456789abcdef0123456789abcdef01234567 - [commit ref]",
		"42 module git-tags bitbucket.org/org/noref https://bitbucket.org/org/noref - - [no ref]",
		"45 module git-tags github.com/org/enc https://github.com/org/enc release%2F1 - [cannot locate value]",
		"49 module terraform-module hashicorp/consul/aws " + tr + " 0.11.0 hashicorp",
		"53 module terraform-module Acme/vpc/aws https://app.terraform.io 1 hashicorp",
		"56 module terraform-module terraform-aws-modules/vpc/aws " + tr + " - - [no version constraint]",
		"59 module - https://example.com/vpc-module.zip?ref=1.0.0 - - - [unsupported module source]",
		"62 module - s3::https://s3-eu-west-1.amazonaws.com/bucket/vpc.zip - - - [unsupported module source]",
		"65 module - dyn - - - [non-literal source]",
	}
	got := extract(t, New(), "main.tf", []byte(src))
	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestExtractErrors(t *testing.T) {
	for _, src := range []string{
		`terraform {`,
		`a = "unterminated`,
		`a = "x` + "\n" + `"`,
		`a = <<EOT` + "\nno end\n",
		`a = <<` + "\n",
		`a = [1, 2`,
		`/* open`,
		`= 1`,
		`block "label" = 1`,
		`a = "${x"`,
		`a = "\q"`,
		`a = "\u12"`,
		`a = "\uZZZZ"`,
	} {
		if _, err := New().Extract("x.tf", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
}

func TestParseEscapes(t *testing.T) {
	b, err := parseHCL([]byte(`a = "x\n\r\t\"\\\u00e9\U0001F600$${y}%%{z}"` + "\nb = \"$${}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := b.attrs[0].val; v.kind != kindString || v.str != "x\n\r\t\"\\é😀${y}%{z}" || v.span != (dep.Span{}) {
		t.Errorf("a = %+v", v)
	}
	if v := b.attrs[1].val; v.str != "${}" {
		t.Errorf("b = %+v", v)
	}
}

func TestMatch(t *testing.T) {
	m, v, l := New(), NewVersion(), NewTFLint()
	for p, want := range map[string][3]bool{
		"main.tf":                     {true, false, false},
		"env/prod/x.tofu":             {true, false, false},
		"x.tf.json":                   {true, false, false},
		"x.tofu.json":                 {true, false, false},
		"x.json":                      {false, false, false},
		"x.tfvars":                    {false, false, false},
		"x.tf.json.bak":               {false, false, false},
		".terraform/modules/a/b.tf":   {false, false, false},
		"env/.terraform/modules/x.tf": {false, false, false},
		".terraform-version":          {false, true, false},
		"env/.terraform-version":      {false, true, false},
		".tflint.hcl":                 {false, false, true},
		"x/.tflint.hcl":               {false, false, true},
		"tflint.hcl":                  {false, false, false},
	} {
		if got := [3]bool{m.Match(p), v.Match(p), l.Match(p)}; got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	if m.Name() != Name || v.Name() != VersionName || l.Name() != TFLintName {
		t.Error("names")
	}
}

func TestEdits(t *testing.T) {
	src := read(t, "moj/versions.tf")
	deps, err := New().Extract("versions.tf", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	var edits []patch.Edit
	for _, d := range deps {
		if d.Name != "hashicorp/aws" && d.Name != "opensearch-project/opensearch" {
			continue
		}
		e, err := New().Edits(dep.Update{Dep: d, NewVersion: map[string]string{"hashicorp/aws": "6.71.0", "opensearch-project/opensearch": "2.6.0"}[d.Name]})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(string(src), `"~> 5.83.0"`, `"~> 6.71.0"`, 1), `version = "2.3.1"`, `version = "2.6.0"`, 1)
	if string(out) != want {
		t.Errorf("patched:\n%s", out)
	}
}

func TestVersionFile(t *testing.T) {
	for _, tt := range []struct{ src, want string }{
		{"1.9.5\n", "1 terraform-version github-releases hashicorp/terraform https://github.com/hashicorp/terraform 1.9.5 hashicorp"},
		{"\n  v1.9 \n", "2 terraform-version github-releases hashicorp/terraform https://github.com/hashicorp/terraform v1.9 hashicorp"},
		{"latest:^1.5", "1 terraform-version github-releases hashicorp/terraform https://github.com/hashicorp/terraform latest:^1.5 hashicorp [non-version value]"},
		{"\n\n", ""},
	} {
		got := extract(t, NewVersion(), ".terraform-version", []byte(tt.src))
		if strings.Join(got, "\n") != tt.want {
			t.Errorf("%q: got %v, want %s", tt.src, got, tt.want)
		}
	}
	d := dep.Dep{Version: "1.9.5", RefSpan: dep.Span{Start: 0, End: 5}}
	if e, _ := NewVersion().Edits(dep.Update{Dep: d, NewVersion: "1.14.0"}); len(e) != 1 || e[0].New != "1.14.0" {
		t.Errorf("edits = %+v", e)
	}
}

func TestTFLint(t *testing.T) {
	got := extract(t, NewTFLint(), ".tflint.hcl", read(t, "moj/tflint.hcl"))
	want := []string{"8 plugin github-releases terraform-linters/tflint-ruleset-aws https://github.com/terraform-linters/tflint-ruleset-aws 0.21.1 semver"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
	src := `plugin "ghes" {
  source  = "GHE.example.com/org/tflint-ruleset-x"
  version = "v1.0.0"
}
plugin "nover" { source = "github.com/o/r" }
plugin "badsrc" {
  source  = "github.com/o"
  version = "1.0.0"
}
plugin "exprsrc" { source = local.s }
plugin "exprver" {
  source  = "github.com/o/r"
  version = local.v
}
plugin "esc" {
  source  = "github.com/o/r"
  version = "1.0\u002e0"
}
plugin "bundled" { enabled = true }
config { call_module_type = "all" }
`
	got = extract(t, NewTFLint(), ".tflint.hcl", []byte(src))
	want = []string{
		"3 plugin github-releases https://ghe.example.com/org/tflint-ruleset-x https://ghe.example.com/org/tflint-ruleset-x v1.0.0 semver",
		"5 plugin github-releases o/r https://github.com/o/r - semver [no version]",
		"7 plugin github-releases github.com/o - - semver [invalid plugin source]",
		"10 plugin github-releases exprsrc - - semver [non-literal source]",
		"13 plugin github-releases o/r https://github.com/o/r - semver [non-literal value]",
		"17 plugin github-releases o/r https://github.com/o/r 1.0.0 semver [cannot locate value]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := NewTFLint().Extract(".tflint.hcl", []byte("plugin {"), manager.Env{}); err == nil {
		t.Error("no error for a broken file")
	}
	d := dep.Dep{Version: "0.21.1", RefSpan: dep.Span{Start: 1, End: 7}}
	if e, _ := NewTFLint().Edits(dep.Update{Dep: d, NewVersion: "0.50.0"}); len(e) != 1 || e[0].Start != 1 || e[0].New != "0.50.0" {
		t.Errorf("edits = %+v", e)
	}
}

// replace extracts file with m, renames the first renamable dep named old to name at version
// ("" = its current one) and returns the renamed dep and the changed lines of the patched file.
func replace(t *testing.T, m manager.Manager, file, src string, env manager.Env, old, name, version string) (dep.Dep, []string, error) {
	t.Helper()
	deps, err := m.Extract(file, []byte(src), env)
	if err != nil {
		t.Fatal(err)
	}
	summarize(t, []byte(src), deps) // checks the name parts
	i := slices.IndexFunc(deps, func(d dep.Dep) bool { return d.Name == old && d.Renamable() })
	if i < 0 {
		t.Fatalf("no renamable dep %s", old)
	}
	d := deps[i]
	nd, err := m.(manager.Renamer).Rename(d, name)
	if err != nil {
		return nd, nil, err
	}
	e, err := m.Edits(dep.Update{Dep: d, Type: dep.Replacement, NewName: name, NewVersion: cmp.Or(version, d.Version)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), e)
	if err != nil {
		t.Fatal(err)
	}
	before, after := strings.Split(src, "\n"), strings.Split(string(out), "\n")
	var changed []string
	for i := range after {
		if after[i] != before[i] {
			changed = append(changed, after[i])
		}
	}
	return nd, changed, nil
}

func TestReplacements(t *testing.T) {
	const src = `terraform {
  required_providers {
    aws     = { source = "hashicorp/aws", version = "~> 5.0" }
    tpl     = { source = "registry.terraform.io/hashicorp/template", version = "2.2.0" }
    bare    = { source = "null", version = "3.0.0" }
    implied = { version = "1.0.0" }
    legacy  = "1.0.0"
    esc     = { source = "hashicorp/\u0061ws", version = "1.0.0" }
    builtin = { source = "terraform.io/builtin/terraform" }
  }
}
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws//modules/x"
  version = "5.0.0"
}
module "hosted" {
  source  = "app.terraform.io/acme/vpc/aws"
  version = "1.0.0"
}
module "git" { source = "github.com/acme/vpc?ref=v1.0.0" }
module "local" { source = "./mod" }
resource "docker_container" "x" { image = "nginx:1.25.3" }
resource "docker_image" "y" { name = "busybox" }
`
	deps, err := New().Extract("main.tf", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range deps {
		if d.Renamable() {
			names = append(names, d.NameParts[0].Text)
		}
	}
	if want := []string{"hashicorp/aws", "registry.terraform.io/hashicorp/template", "null", "terraform-aws-modules/vpc/aws", "app.terraform.io/acme/vpc/aws", "nginx"}; !slices.Equal(names, want) {
		t.Errorf("renamable %q, want %q", names, want)
	}
	const json = `{"terraform": {"required_providers": {"aws": {"source": "hashicorp/aws", "version": "~> 5.0"}}}}`
	for _, tt := range []struct {
		file, src, registry   string // registry: config terraform.defaultRegistry
		old, name, version    string
		wName, wSource, wLine string
	}{
		{"main.tf", src, "", "hashicorp/aws", "opentofu/aws", "",
			"opentofu/aws", "https://registry.terraform.io", `    aws     = { source = "opentofu/aws", version = "~> 5.0" }`},
		{"main.tf", src, "", "hashicorp/aws", "opentofu/aws", "6.2.1",
			"opentofu/aws", "https://registry.terraform.io", `    aws     = { source = "opentofu/aws", version = "~> 6.2" }`},
		{"main.tf", src, "", "hashicorp/aws", "Registry.OpenTofu.org/opentofu/aws", "",
			"opentofu/aws", "https://registry.opentofu.org", `    aws     = { source = "Registry.OpenTofu.org/opentofu/aws", version = "~> 5.0" }`},
		{"main.tofu", src, "", "hashicorp/aws", "opentofu/aws", "",
			"opentofu/aws", "https://registry.opentofu.org", `    aws     = { source = "opentofu/aws", version = "~> 5.0" }`},
		{"main.tf", src, "https://registry.example.com/", "hashicorp/aws", "acme/aws", "",
			"acme/aws", "https://registry.example.com", `    aws     = { source = "acme/aws", version = "~> 5.0" }`},
		{"main.tf", src, "", "hashicorp/template", "registry.opentofu.org/opentofu/template", "2.2.1",
			"opentofu/template", "https://registry.opentofu.org", `    tpl     = { source = "registry.opentofu.org/opentofu/template", version = "2.2.1" }`},
		{"main.tf", src, "", "hashicorp/null", "opentofu/null", "",
			"opentofu/null", "https://registry.terraform.io", `    bare    = { source = "opentofu/null", version = "3.0.0" }`},
		{"main.tf", src, "", "terraform-aws-modules/vpc/aws", "acme/vpc/aws", "",
			"acme/vpc/aws", "https://registry.terraform.io", `  source  = "acme/vpc/aws//modules/x"`},
		{"main.tf", src, "", "acme/vpc/aws", "App.Terraform.IO/acme/net/aws", "",
			"acme/net/aws", "https://app.terraform.io", `  source  = "App.Terraform.IO/acme/net/aws"`},
		{"main.tf", src, "", "nginx", "ghcr.io/acme/nginx", "",
			"ghcr.io/acme/nginx", "ghcr.io/acme/nginx", `resource "docker_container" "x" { image = "ghcr.io/acme/nginx:1.25.3" }`},
		{"main.tf.json", json, "", "hashicorp/aws", "opentofu/aws", "",
			"opentofu/aws", "https://registry.terraform.io", `{"terraform": {"required_providers": {"aws": {"source": "opentofu/aws", "version": "~> 5.0"}}}}`},
	} {
		t.Run(tt.file+" "+tt.name, func(t *testing.T) {
			got, lines, err := replace(t, New(), tt.file, tt.src, manager.Env{TerraformRegistry: tt.registry}, tt.old, tt.name, tt.version)
			switch {
			case err != nil:
				t.Fatal(err)
			case got.Name != tt.wName || got.Source != tt.wSource:
				t.Errorf("renamed %s %s, want %s %s", got.Name, got.Source, tt.wName, tt.wSource)
			case !slices.Equal(lines, []string{tt.wLine}):
				t.Errorf("changed lines %q, want %q", lines, tt.wLine)
			}
		})
	}
	for _, tt := range []struct{ old, name string }{
		{"hashicorp/aws", ""},
		{"hashicorp/aws", "a/b/c/d"},
		{"hashicorp/aws", "hashicorp/a ws"},
		{"hashicorp/aws", "hashicorp/${x}"},
		{"hashicorp/aws", "hashicorp//aws"},
		{"hashicorp/template", "hashicorp/template2"}, // the written host needs another
		{"terraform-aws-modules/vpc/aws", "acme/vpc"},
		{"terraform-aws-modules/vpc/aws", "acme/vpc/aws//modules/x"},
		{"terraform-aws-modules/vpc/aws", "github.com/acme/vpc/aws"},
		{"terraform-aws-modules/vpc/aws", "git::https://example.com/acme/vpc.git"},
		{"acme/vpc/aws", "acme/net/aws"}, // the written host needs another
		{"nginx", "nginx:1.27"},
		{"nginx", "Nginx"},
	} {
		if _, _, err := replace(t, New(), "main.tf", src, manager.Env{}, tt.old, tt.name, ""); err == nil {
			t.Errorf("%s: Rename(%q) accepted", tt.old, tt.name)
		}
	}
	if _, err := New().(manager.Renamer).Rename(dep.Dep{Datasource: "helm", DepType: DepTypeHelmRelease}, "x"); err == nil {
		t.Error("helm_release renamed")
	}
	fixed := dep.Dep{Datasource: providerDatasource, Version: "1.0", Range: ">= 1.0, < 2.0", RangeFixed: true,
		NameParts: []dep.NamePart{{Span: dep.Span{Start: 1, End: 4}, Text: "aws"}}}
	if _, err := New().Edits(dep.Update{Dep: fixed, NewName: "opentofu/aws", NewVersion: "1.1"}); err == nil {
		t.Error("fixed range rewritten")
	}
	if e, err := New().Edits(dep.Update{Dep: fixed, NewName: "opentofu/aws", NewVersion: "1.0"}); err != nil || len(e) != 1 {
		t.Errorf("fixed range renamed: %+v, %v", e, err)
	}
	if got, err := New().(manager.Renamer).Rename(dep.Dep{Datasource: providerDatasource, Source: "https://r.example"}, "acme/x"); err != nil || got.Name != "acme/x" || got.Source != "https://r.example" {
		t.Errorf("unlocated provider renamed %+v, %v", got, err)
	}
}

func TestTFLintReplacements(t *testing.T) {
	src := string(read(t, "moj/tflint.hcl"))
	const ghes = "plugin \"x\" {\n  source  = \"GHE.example.com/org/x\"\n  version = \"1.0.0\"\n}\n"
	for _, tt := range []struct {
		src, old, name, wName, wSource, wLine string
	}{
		{src, "terraform-linters/tflint-ruleset-aws", "github.com/acme/tflint-ruleset-aws",
			"acme/tflint-ruleset-aws", "https://github.com/acme/tflint-ruleset-aws", `  source  = "github.com/acme/tflint-ruleset-aws"`},
		{ghes, "https://ghe.example.com/org/x", "github.com/acme/x",
			"acme/x", "https://github.com/acme/x", `  source  = "github.com/acme/x"`},
		{src, "terraform-linters/tflint-ruleset-aws", "GHE.example.com/acme/x",
			"https://ghe.example.com/acme/x", "https://ghe.example.com/acme/x", `  source  = "GHE.example.com/acme/x"`},
	} {
		got, lines, err := replace(t, NewTFLint(), ".tflint.hcl", tt.src, manager.Env{}, tt.old, tt.name, "")
		switch {
		case err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case got.Name != tt.wName || got.Source != tt.wSource:
			t.Errorf("%s: renamed %s %s, want %s %s", tt.name, got.Name, got.Source, tt.wName, tt.wSource)
		case !slices.Equal(lines, []string{tt.wLine}):
			t.Errorf("%s: changed lines %q, want %q", tt.name, lines, tt.wLine)
		}
	}
	for _, name := range []string{"acme/tflint-ruleset-aws", "github.com/acme", "github.com/acme/x/y", "github.com/a b/x"} {
		if _, _, err := replace(t, NewTFLint(), ".tflint.hcl", src, manager.Env{}, "terraform-linters/tflint-ruleset-aws", name, ""); err == nil {
			t.Errorf("Rename(%q) accepted", name)
		}
	}
}
