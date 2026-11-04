package terraform

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Terragrunt fixtures, copied unchanged (paths kept below testdata/<short-repo>/):
//   - testdata/tg: github.com/gruntwork-io/terragrunt@1a46eb6 (MIT): test/fixtures/{tfr/root-shorthand,
//     tfr/subdir,tfr/version-constraint,tfr/version-constraint-in-query,filter-source/github-acme-bar,
//     source-map/single,docs/02-overview-azure/step-02-dependencies/vnet,codegen/generate-block/enable}/terragrunt.hcl.
//   - testdata/tg-live: github.com/gruntwork-io/terragrunt-infrastructure-live-example@d106b47 (Apache-2.0):
//     non-prod/us-east-1/qa/mysql/terragrunt.hcl.
//   - testdata/abra: github.com/antonbabenko/terragrunt-reference-architecture@1c414c7 (MIT):
//     acme-prod/us-east-1/acm, acme-prod/global/iam-account and
//     acme-master/eu-central-1/ecr-repositories/acme-api terragrunt.hcl.
//   - testdata/do-database: github.com/terraform-do-modules/terraform-digitalocean-database@0b8f788
//     (Apache-2.0): examples/terragrunt/terragrunt.hcl.
//   - testdata/recipes: github.com/ryanoolala/recipes@8a732de (MIT): infrastructure/vpc/terragrunt.hcl.
//   - testdata/teks: github.com/particuleio/teks@0b71bbc (Apache-2.0):
//     terragrunt/live/production/eu-west-1/clusters/demo/vpc/terragrunt.hcl.

func TestTerragruntRealWorld(t *testing.T) {
	const (
		tr = "https://registry.terraform.io"
		ot = "https://registry.opentofu.org"
	)
	for _, tt := range []struct {
		file string
		want []string
	}{
		{"tg/tfr/root-shorthand/terragrunt.hcl", []string{
			"3 terragrunt terraform-module gruntwork-io/terragrunt-registry-test/null " + tr + " 0.0.2 hashicorp",
		}},
		{"tg/tfr/subdir/terragrunt.hcl", []string{
			"3 terragrunt terraform-module yorinasub17/terragrunt-registry-test/null " + tr + " 0.0.2 hashicorp",
		}},
		{"tg/tfr/version-constraint/terragrunt.hcl", []string{
			"5 terragrunt terraform-module yorinasub17/terragrunt-registry-test/null " + ot + " 0.0.1 hashicorp",
		}},
		{"tg/tfr/version-constraint-in-query/terragrunt.hcl", []string{
			"5 terragrunt terraform-module yorinasub17/terragrunt-registry-test/null " + ot + " ~>0.0.1 hashicorp [unsupported constraint]",
		}},
		{"tg/docs/02-overview-azure/step-02-dependencies/vnet/terragrunt.hcl", []string{
			"7 terragrunt terraform-module Azure/avm-res-network-virtualnetwork/azurerm " + tr + " 0.22.3 hashicorp",
		}},
		{"tg/filter-source/github-acme-bar/terragrunt.hcl", []string{
			"2 github git-tags github.com/acme/bar https://github.com/acme/bar v1.0.0 semver",
		}},
		{"tg/source-map/single/terragrunt.hcl", []string{
			"2 github git-tags github.com/gruntwork-io/i-dont-exist https://github.com/gruntwork-io/i-dont-exist.git master semver",
		}},
		{"tg/codegen/generate-block/enable/terragrunt.hcl", []string{
			"9 terragrunt - ../../module - - - [local module]",
		}},
		{"tg-live/non-prod/us-east-1/qa/mysql/terragrunt.hcl", []string{
			"24 terragrunt - terraform.source - - - [non-literal source]",
		}},
		{"abra/acme-prod/us-east-1/acm/terragrunt.hcl", []string{
			"2 github git-tags github.com/terraform-aws-modules/terraform-aws-acm https://github.com/terraform-aws-modules/terraform-aws-acm.git v2.5.0 semver",
		}},
		{"abra/acme-prod/global/iam-account/terragrunt.hcl", []string{
			"2 github git-tags github.com/terraform-aws-modules/terraform-aws-iam https://github.com/terraform-aws-modules/terraform-aws-iam.git v2.9.0 semver",
		}},
		{"abra/acme-master/eu-central-1/ecr-repositories/acme-api/terragrunt.hcl", []string{
			"2 github git-tags github.com/doingcloudright/terraform-aws-ecr-cross-account https://github.com/doingcloudright/terraform-aws-ecr-cross-account.git 1.0.1 semver",
		}},
		{"do-database/examples/terragrunt/terragrunt.hcl", []string{
			"63 github git-tags github.com/terraform-do-modules/terraform-digitalocean-database https://github.com/terraform-do-modules/terraform-digitalocean-database.git v1.0.0 semver",
		}},
		{"recipes/infrastructure/vpc/terragrunt.hcl", []string{
			"6 gitTags git-tags gitlab.com/govtechsingapore/gdsace/terraform-modules/aws-vpc https://gitlab.com/govtechsingapore/gdsace/terraform-modules/aws-vpc v1.1.0 semver",
		}},
		{"teks/terragrunt/live/production/eu-west-1/clusters/demo/vpc/terragrunt.hcl", []string{
			"8 github git-tags github.com/terraform-aws-modules/terraform-aws-vpc https://github.com/terraform-aws-modules/terraform-aws-vpc v5.15.0 semver",
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			got := extract(t, NewTerragrunt(), tt.file, read(t, tt.file))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestTerragruntForms(t *testing.T) {
	for _, tt := range []struct {
		name, src string
		env       manager.Env
		want      []string
	}{
		{"tfr host case and params", `terraform { source = "tfr://App.Terraform.IO/acme/vpc/aws//modules/x?foo=1&version=v1.2" }`, manager.Env{},
			[]string{"1 terragrunt terraform-module acme/vpc/aws https://app.terraform.io v1.2 hashicorp"}},
		{"tfr default registry override", `terraform { source = "tfr:///acme/vpc/aws?version=1.2.3" }`, manager.Env{TerraformRegistry: "https://registry.opentofu.org/"},
			[]string{"1 terragrunt terraform-module acme/vpc/aws https://registry.opentofu.org 1.2.3 hashicorp"}},
		{"tfr query wins over attribute", "terraform {\n  source = \"tfr:///acme/vpc/aws?version=1.2.3\"\n  version = \"~> 2.0\"\n}", manager.Env{},
			[]string{"2 terragrunt terraform-module acme/vpc/aws https://registry.terraform.io 1.2.3 hashicorp"}},
		{"tfr non-literal attribute", "terraform {\n  source = \"tfr:///acme/vpc/aws\"\n  version = local.v\n}", manager.Env{},
			[]string{"3 terragrunt terraform-module acme/vpc/aws https://registry.terraform.io - - [non-literal value]"}},
		{"tfr without version", `terraform { source = "tfr:///terraform-aws-modules/vpc/aws" }`, manager.Env{},
			[]string{"1 terragrunt terraform-module terraform-aws-modules/vpc/aws https://registry.terraform.io - - [no version constraint]"}},
		{"tfr encoded version", `terraform { source = "tfr:///acme/vpc/aws?version=1.0%2B1" }`, manager.Env{},
			[]string{"1 terragrunt terraform-module acme/vpc/aws https://registry.terraform.io 1.0%2B1 - [cannot locate value]"}},
		{"tfr escaped source", `terraform { source = "tfr:///acme/vpc/aws?version=1.0.0\u0020" }`, manager.Env{},
			[]string{"1 terragrunt terraform-module acme/vpc/aws https://registry.terraform.io 1.0.0  - [cannot locate value]"}},
		{"tfr malformed", `terraform { source = "tfr:///acme/vpc?version=1.0.0" }`, manager.Env{},
			[]string{"1 terragrunt - tfr:///acme/vpc?version=1.0.0 - - - [unsupported module source]"}},
		{"git no ref", `terraform { source = "git::https://example.com/org/mod.git" }`, manager.Env{},
			[]string{"1 gitTags git-tags example.com/org/mod https://example.com/org/mod.git - - [no ref]"}},
		{"git commit ref", `terraform { source = "github.com/org/mod?ref=0123456789abcdef0123456789abcdef01234567" }`, manager.Env{},
			[]string{"1 github git-tags github.com/org/mod https://github.com/org/mod 0123456789abcdef0123456789abcdef01234567 - [commit ref]"}},
		{"git partial ref and depth", `terraform { source = "git::ssh://git@bitbucket.org/org/mod.git//sub?depth=1&ref=v2" }`, manager.Env{},
			[]string{"1 gitTags git-tags bitbucket.org/org/mod https://bitbucket.org/org/mod.git v2 tag"}},
		{"archive", `terraform { source = "https://example.com/mod.zip" }`, manager.Env{},
			[]string{"1 terragrunt - https://example.com/mod.zip - - - [unsupported module source]"}},
		{"oci", `terraform { source = "oci://ghcr.io/acme/terraform-modules/vpc?tag=1.0.0" }`, manager.Env{},
			[]string{"1 terragrunt - oci://ghcr.io/acme/terraform-modules/vpc?tag=1.0.0 - - - [unsupported module source]"}},
		{"local dot", `terraform { source = "." }`, manager.Env{}, []string{"1 terragrunt - . - - - [local module]"}},
		{"other blocks ignored", "include \"root\" {\n  path = find_in_parent_folders(\"root.hcl\")\n}\nterraform {\n  extra_arguments \"x\" {\n    commands = [\"plan\"]\n  }\n}\nengine {\n  source = \"github.com/gruntwork-io/terragrunt-engine-opentofu\"\n}\n", manager.Env{}, []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps, err := NewTerragrunt().Extract("terragrunt.hcl", []byte(tt.src), tt.env)
			if err != nil {
				t.Fatal(err)
			}
			got := summarize(t, []byte(tt.src), deps)
			if !slices.Equal(got, tt.want) {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestTerragruntMatch(t *testing.T) {
	m := NewTerragrunt()
	for p, want := range map[string]bool{
		"terragrunt.hcl":                         true,
		"live/prod/vpc/terragrunt.hcl":           true,
		"root.hcl":                               false,
		"terragrunt.hcl.example":                 false,
		"my-terragrunt.hcl":                      false,
		".terragrunt-cache/x/y/terragrunt.hcl":   false,
		"vpc/.terragrunt-cache/x/terragrunt.hcl": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}

func TestTerragruntEdits(t *testing.T) {
	for _, tt := range []struct{ file, version, want string }{
		{"tg/tfr/subdir/terragrunt.hcl", "0.1.0", `source = "tfr://registry.terraform.io/yorinasub17/terragrunt-registry-test/null//modules/one?version=0.1.0"`},
		{"tg/tfr/version-constraint/terragrunt.hcl", "0.2.4", `version = "~> 0.2.4"`},
		{"do-database/examples/terragrunt/terragrunt.hcl", "v1.2.0", `source = "git::https://github.com/terraform-do-modules/terraform-digitalocean-database.git//?ref=v1.2.0"`},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src := read(t, tt.file)
			deps, err := NewTerragrunt().Extract(tt.file, src, manager.Env{})
			if err != nil || len(deps) != 1 {
				t.Fatalf("deps = %+v, %v", deps, err)
			}
			e, err := NewTerragrunt().Edits(dep.Update{Dep: deps[0], NewVersion: tt.version})
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply(src, e)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tt.want) || len(out)-len(src) != len(tt.version)-len(deps[0].Version) {
				t.Errorf("got\n%s", out)
			}
		})
	}
}
