package terraform

import (
	"os"
	"path/filepath"
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
//     source-map/single,docs/02-overview-azure/step-02-dependencies/vnet,codegen/generate-block/enable}/terragrunt.hcl;
//     @259d9b2: test/fixtures/engine/opentofu-engine/terragrunt.hcl, and
//     docs/src/fixtures/terralith-to-terragrunt/walkthrough/step-6-breaking-the-terralith-further/live/dev/ddb/terragrunt.hcl
//     as docs/terralith-step-6/live/dev/ddb/terragrunt.hcl.
//   - testdata/tg-live: github.com/gruntwork-io/terragrunt-infrastructure-live-example@d106b47 (Apache-2.0):
//     non-prod/us-east-1/qa/mysql/terragrunt.hcl, root.hcl and _envcommon/mysql.hcl (its include).
//   - testdata/abra: github.com/antonbabenko/terragrunt-reference-architecture@1c414c7 (MIT):
//     acme-prod/us-east-1/acm, acme-prod/global/iam-account and
//     acme-master/eu-central-1/ecr-repositories/acme-api terragrunt.hcl.
//   - testdata/do-database: github.com/terraform-do-modules/terraform-digitalocean-database@0b8f788
//     (Apache-2.0): examples/terragrunt/terragrunt.hcl.
//   - testdata/recipes: github.com/ryanoolala/recipes@8a732de (MIT): infrastructure/vpc/terragrunt.hcl.
//   - testdata/teks: github.com/particuleio/teks@0b71bbc (Apache-2.0):
//     terragrunt/live/production/eu-west-1/clusters/demo/vpc/terragrunt.hcl.

// testdataEnv resolves Exists and ReadFile against testdata, the scanned root of the fixtures.
func testdataEnv() manager.Env {
	return manager.Env{
		Exists: func(p string) bool {
			_, err := os.Stat(filepath.Join("testdata", filepath.FromSlash(p)))
			return err == nil
		},
		ReadFile: func(p string) ([]byte, error) { return os.ReadFile(filepath.Join("testdata", filepath.FromSlash(p))) },
	}
}

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
		// include "envcommon" { path = "${dirname(find_in_parent_folders("root.hcl"))}/_envcommon/mysql.hcl" }
		// and source = "${include.envcommon.locals.base_source_url}?ref=v0.8.0": the ref is this file's.
		{"tg-live/non-prod/us-east-1/qa/mysql/terragrunt.hcl", []string{
			"24 github git-tags github.com/gruntwork-io/terragrunt-infrastructure-modules-example https://github.com/gruntwork-io/terragrunt-infrastructure-modules-example.git v0.8.0 semver",
		}},
		{"tg/docs/terralith-step-6/live/dev/ddb/terragrunt.hcl", []string{
			"6 terragrunt - terraform.source - - - [local module]",
		}},
		{"tg/engine/opentofu-engine/terragrunt.hcl", []string{
			"3 engine github-releases gruntwork-io/terragrunt-engine-opentofu https://github.com/gruntwork-io/terragrunt-engine-opentofu v0.1.1 semver",
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
			deps, err := NewTerragrunt().Extract(tt.file, read(t, tt.file), testdataEnv())
			if err != nil {
				t.Fatal(err)
			}
			got := summarize(t, read(t, tt.file), deps)
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
			[]string{"1 github git-refs github.com/org/mod https://github.com/org/mod HEAD tag @0123456789abcdef0123456789abcdef01234567"}},
		{"git tags/ ref", `terraform { source = "git::https://github.com/org/mod.git?ref=tags/0.4.0" }`, manager.Env{},
			[]string{"1 github git-tags github.com/org/mod https://github.com/org/mod.git 0.4.0 semver"}},
		{"git partial ref and depth", `terraform { source = "git::ssh://git@bitbucket.org/org/mod.git//sub?depth=1&ref=v2" }`, manager.Env{},
			[]string{"1 gitTags git-tags bitbucket.org/org/mod https://bitbucket.org/org/mod.git v2 tag"}},
		{"archive", `terraform { source = "https://example.com/mod.zip" }`, manager.Env{},
			[]string{"1 terragrunt - https://example.com/mod.zip - - - [archive module source]"}},
		{"github archive", `terraform { source = "https://github.com/acme/mod/archive/refs/tags/v1.2.0.tar.gz" }`, manager.Env{},
			[]string{"1 github git-tags github.com/acme/mod https://github.com/acme/mod v1.2.0 semver"}},
		{"s3", `terraform { source = "s3::https://s3-eu-west-1.amazonaws.com/bucket/mod.zip" }`, manager.Env{},
			[]string{"1 terragrunt - s3::https://s3-eu-west-1.amazonaws.com/bucket/mod.zip - - - [s3 module source]"}},
		{"s3 host", `terraform { source = "bucket.s3.eu-west-1.amazonaws.com/mod.zip" }`, manager.Env{},
			[]string{"1 terragrunt - bucket.s3.eu-west-1.amazonaws.com/mod.zip - - - [s3 module source]"}},
		{"gcs", `terraform { source = "gcs::https://www.googleapis.com/storage/v1/bucket/mod.zip" }`, manager.Env{},
			[]string{"1 terragrunt - gcs::https://www.googleapis.com/storage/v1/bucket/mod.zip - - - [gcs module source]"}},
		{"hg", `terraform { source = "hg::https://example.com/mod?ref=v1" }`, manager.Env{},
			[]string{"1 terragrunt - hg::https://example.com/mod?ref=v1 - - - [hg module source]"}},
		{"oci", `terraform { source = "oci://ghcr.io/acme/terraform-modules/vpc//sub?tag=1.0.0" }`, manager.Env{},
			[]string{"1 oci docker ghcr.io/acme/terraform-modules/vpc ghcr.io/acme/terraform-modules/vpc 1.0.0 docker"}},
		{"oci digest", `terraform { source = "oci://ghcr.io/acme/vpc?digest=sha256:0123" }`, manager.Env{},
			[]string{"1 oci docker ghcr.io/acme/vpc ghcr.io/acme/vpc - docker [digest without tag]"}},
		{"oci untagged", `terraform { source = "oci://ghcr.io/acme/vpc" }`, manager.Env{},
			[]string{"1 oci docker ghcr.io/acme/vpc ghcr.io/acme/vpc - docker [no tag]"}},
		{"oci no repository", `terraform { source = "oci://ghcr.io?tag=1" }`, manager.Env{},
			[]string{"1 oci docker ghcr.io - - docker [invalid reference]"}},
		{"local dot", `terraform { source = "." }`, manager.Env{}, []string{"1 terragrunt - . - - - [local module]"}},
		{"TG_TF_DEFAULT_REGISTRY_HOST", `terraform { source = "tfr:///acme/vpc/aws?version=1.2.3" }`,
			manager.Env{TerragruntRegistry: "Registry.OpenTofu.org", TerraformRegistry: "registry.example.com"},
			[]string{"1 terragrunt terraform-module acme/vpc/aws https://registry.opentofu.org 1.2.3 hashicorp"}},
		{"local", "locals {\n  base = \"git::https://example.com/org/mod.git\"\n  src = \"${local.base}//sub\"\n}\nterraform {\n  source = \"${local.src}?ref=v1.2.3\"\n}\n", manager.Env{},
			[]string{"6 gitTags git-tags example.com/org/mod https://example.com/org/mod.git v1.2.3 semver"}},
		{"local with ref", "locals {\n  v = \"v1.2.3\"\n}\nterraform {\n  source = \"git::https://example.com/org/mod.git?ref=${local.v}\" # pinned\n}\n", manager.Env{},
			[]string{"5 gitTags git-tags example.com/org/mod https://example.com/org/mod.git v1.2.3 semver"}},
		{"traversal", "locals {\n  source = \"tfr:///acme/vpc/aws?version=1.2.3\"\n}\nterraform {\n  source = local.source\n}\n", manager.Env{},
			[]string{"5 terragrunt terraform-module acme/vpc/aws https://registry.terraform.io 1.2.3 hashicorp"}},
		{"included ref", "include \"c\" {\n  path = \"c.hcl\"\n}\nterraform {\n  source = include.c.locals.src\n}\n",
			manager.Env{ReadFile: func(string) ([]byte, error) {
				return []byte("locals {\n  src = \"git::https://example.com/org/mod.git?ref=v1.0.0\"\n}\n"), nil
			}},
			[]string{"5 gitTags git-tags example.com/org/mod https://example.com/org/mod.git v1.0.0 - [cannot locate value]"}},
		{"unknown function", "terraform {\n  source = \"${get_env(\"SRC\")}?ref=v1\"\n}\n", manager.Env{},
			[]string{"2 terragrunt - terraform.source - - - [non-literal source]"}},
		{"repo root", "terraform {\n  source = \"${get_repo_root()}/modules/vpc\"\n}\n", manager.Env{},
			[]string{"2 terragrunt - /modules/vpc - - - [local module]"}},
		{"terragrunt dir", "terraform {\n  source = \"${get_terragrunt_dir()}/../modules//vpc\"\n}\n", manager.Env{},
			[]string{"2 terragrunt - /../modules//vpc - - - [local module]"}},
		{"local cycle", "locals {\n  a = local.b\n  b = local.a\n}\nterraform {\n  source = local.a\n}\n", manager.Env{},
			[]string{"6 terragrunt - terraform.source - - - [non-literal source]"}},
		{"other blocks ignored", "include \"root\" {\n  path = find_in_parent_folders(\"root.hcl\")\n}\ndependency \"vpc\" {\n  config_path = \"../vpc\"\n}\ngenerate \"provider\" {\n  path = \"provider.tf\"\n  contents = <<EOF\nterraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\", version = \"~> 5.0\" }\n  }\n}\nEOF\n}\nterraform {\n  extra_arguments \"x\" {\n    commands = [\"plan\"]\n  }\n}\n", manager.Env{}, []string{}},
		{"engine", "engine {\n  source = \"github.com/gruntwork-io/terragrunt-engine-opentofu\"\n}\nengine {\n  source = \"/opt/engines/tofu\"\n}\nengine {\n  source = \"https://example.com/engine.zip\"\n}\n", manager.Env{},
			[]string{
				"2 engine github-releases gruntwork-io/terragrunt-engine-opentofu https://github.com/gruntwork-io/terragrunt-engine-opentofu - semver [no version]",
				"5 engine github-releases /opt/engines/tofu - - semver [local engine]",
				"8 engine github-releases https://example.com/engine.zip - - semver [unsupported engine source]",
			}},
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
		{"tg-live/non-prod/us-east-1/qa/mysql/terragrunt.hcl", "v0.9.0", `source = "${include.envcommon.locals.base_source_url}?ref=v0.9.0"`},
		{"tg/engine/opentofu-engine/terragrunt.hcl", "v0.2.0", `version = "v0.2.0"`},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src := read(t, tt.file)
			deps, err := NewTerragrunt().Extract(tt.file, src, testdataEnv())
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

func TestTerragruntSourceEdits(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tt := range []struct {
		name, src string
		u         dep.Update
		want      string
	}{
		{"commit", `terraform { source = "git::https://github.com/org/mod.git//x?ref=` + sha + `" }`,
			dep.Update{NewVersion: defaultBranch, NewDigest: "fedcba9876543210fedcba9876543210fedcba98"},
			`terraform { source = "git::https://github.com/org/mod.git//x?ref=fedcba9876543210fedcba9876543210fedcba98" }`},
		{"oci tag", `terraform { source = "oci://ghcr.io/acme/vpc?tag=1.0.0" }`,
			dep.Update{NewVersion: "1.1.0"}, `terraform { source = "oci://ghcr.io/acme/vpc?tag=1.1.0" }`},
		{"local ref", "locals {\n  v = \"v1.2.3\"\n}\nterraform {\n  source = \"git::https://example.com/org/mod.git?ref=${local.v}\"\n}\n",
			dep.Update{NewVersion: "v1.3.0"}, "  v = \"v1.3.0\""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps, err := NewTerragrunt().Extract("terragrunt.hcl", []byte(tt.src), manager.Env{})
			if err != nil || len(deps) != 1 {
				t.Fatalf("deps = %+v, %v", deps, err)
			}
			tt.u.Dep = deps[0]
			e, err := NewTerragrunt().Edits(tt.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply([]byte(tt.src), e)
			if err != nil || !strings.Contains(string(out), tt.want) {
				t.Errorf("got %s, %v", out, err)
			}
		})
	}
	d := dep.Dep{Datasource: gitRefsDatasource, Name: "x", Digest: sha, DigestSpan: dep.Span{Start: 1, End: 41}}
	if _, err := NewTerragrunt().Edits(dep.Update{Dep: d}); err == nil {
		t.Error("a commit update without a new commit has no edits")
	}
}

func TestTerragruntReplacements(t *testing.T) {
	const (
		hostless = `terraform { source = "tfr:///acme/vpc/aws//modules/x?version=1.2.3" }`
		hosted   = `terraform { source = "tfr://App.Terraform.IO/acme/vpc/aws?version=1.2.3" }`
	)
	for _, tt := range []struct {
		src, registry, name, version, wName, wSource, wLine string
	}{
		{hostless, "", "acme/net/aws", "", "acme/net/aws", "https://registry.terraform.io",
			`terraform { source = "tfr:///acme/net/aws//modules/x?version=1.2.3" }`},
		{hostless, "https://registry.opentofu.org", "acme/net/aws", "1.3.0", "acme/net/aws", "https://registry.opentofu.org",
			`terraform { source = "tfr:///acme/net/aws//modules/x?version=1.3.0" }`},
		{hosted, "", "registry.terraform.io/acme/net/aws", "", "acme/net/aws", "https://registry.terraform.io",
			`terraform { source = "tfr://registry.terraform.io/acme/net/aws?version=1.2.3" }`},
	} {
		got, lines, err := replace(t, NewTerragrunt(), "terragrunt.hcl", tt.src, manager.Env{TerraformRegistry: tt.registry}, "acme/vpc/aws", tt.name, tt.version)
		switch {
		case err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case got.Name != tt.wName || got.Source != tt.wSource:
			t.Errorf("%s: renamed %s %s, want %s %s", tt.name, got.Name, got.Source, tt.wName, tt.wSource)
		case !slices.Equal(lines, []string{tt.wLine}):
			t.Errorf("%s: changed lines %q, want %q", tt.name, lines, tt.wLine)
		}
	}
	for _, tt := range []struct{ src, name string }{
		{hostless, "registry.terraform.io/acme/net/aws"}, // would read tfr:///host/…
		{hosted, "acme/net/aws"},                         // would read tfr://acme/…
		{hostless, "acme/net"},
		{hostless, "acme/net/aws//modules/y"},
		{hosted, "github.com/acme/net/aws"},
	} {
		if _, _, err := replace(t, NewTerragrunt(), "terragrunt.hcl", tt.src, manager.Env{}, "acme/vpc/aws", tt.name, ""); err == nil {
			t.Errorf("%s: Rename(%q) accepted", tt.src, tt.name)
		}
	}
	deps, err := NewTerragrunt().Extract("terragrunt.hcl", []byte(`terraform { source = "github.com/acme/vpc?ref=v1.0.0" }`), manager.Env{})
	if err != nil || len(deps) != 1 || deps[0].Renamable() {
		t.Errorf("git source: %+v, %v", deps, err)
	}
	if _, err := NewTerragrunt().(manager.Renamer).Rename(deps[0], "acme/vpc/aws"); err == nil {
		t.Error("git source renamed")
	}
}

// TestTerragruntEval covers the expression subset tgEval evaluates, in a configuration at
// live/prod/app/terragrunt.hcl whose parents hold root.hcl and an include.
func TestTerragruntEval(t *testing.T) {
	files := map[string]string{
		"live/root.hcl":           "",
		"live/common.hcl":         "locals {\n  base = \"git::https://example.com/org/mod.git\"\n  derived = \"${local.base}//sub\"\n}\n",
		"live/broken.hcl":         "locals {",
		"live/prod/app/local.hcl": "",
	}
	env := manager.Env{
		Exists: func(p string) bool { _, ok := files[p]; return ok },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
	}
	for _, tt := range []struct {
		expr, want string
		ok, local  bool
	}{
		{`"a${local.l}b"`, "aXb", true, false},
		{`"esc\"aped $${x} %%{y}"`, `esc"aped ${x} %{y}`, true, false},
		{`"%{ if true }x%{ endif }"`, "", false, false},
		{`local.l /* c */ # c`, "X", true, false},
		{`local.l // c`, "X", true, false},
		{`local.missing`, "", false, false},
		{`local.`, "", false, false},
		{`var.x`, "", false, false},
		{`1 + 2`, "", false, false},
		{`local.l local.l`, "X", false, false},
		{`include.c.locals.derived`, "git::https://example.com/org/mod.git//sub", true, false},
		{`include.c.locals.missing`, "", false, false},
		{`include.nope.locals.base`, "", false, false},
		{`include.b.locals.base`, "", false, false},
		{`include.gone.locals.base`, "", false, false},
		{`include.up.locals.base`, "", false, false},
		{`include.fn.locals.base`, "", false, false},
		{`get_terragrunt_dir()`, "/live/prod/app", true, true},
		{`get_repo_root()`, "", true, true},
		{`get_path_to_repo_root()`, "../../..", true, true},
		{`get_parent_terragrunt_dir()`, "", false, true},
		{`dirname(find_in_parent_folders("root.hcl"))`, "/live", true, true},
		{`find_in_parent_folders("local.hcl")`, "", false, true},
		{`find_in_parent_folders("nope.hcl", "fallback")`, "fallback", true, true},
		{`find_in_parent_folders("a", "b", "c")`, "", false, true},
		{`dirname("a/b", "c")`, "", false, false},
		{`dirname(get_env("X"))`, "", false, false},
		{`get_env("X", get_repo_root())`, "", false, true},
		{`find_in_parent_folders(get_env("X"))`, "", false, true},
		{`get_repo_root(1 2)`, "", false, true},
	} {
		t.Run(tt.expr, func(t *testing.T) {
			src := "locals {\n  l = \"X\"\n}\n" +
				"include \"c\" {\n  path = \"../../common.hcl\"\n}\n" +
				"include \"b\" {\n  path = \"${get_repo_root()}/live/broken.hcl\"\n}\n" +
				"include \"gone\" {\n  path = \"../../gone.hcl\"\n}\n" +
				"include \"up\" {\n  path = \"../../../../x.hcl\"\n}\n" +
				"include \"fn\" {\n  path = get_env(\"X\")\n}\n" +
				"x = " + tt.expr + "\n"
			b, err := parseHCL([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			file := "live/prod/app/terragrunt.hcl"
			sc := newScope(file, []byte(src), b, false)
			got, ok := (&tgEval{exists: env.Exists, readFile: env.ReadFile}).eval(sc, b.attrs[len(b.attrs)-1].val)
			if ok != tt.ok || got.local != tt.local || ok && got.s != tt.want {
				t.Errorf("eval = %q, %v (local %v)", got.s, ok, got.local)
			}
		})
	}
	// An included file is read once per extraction.
	reads := 0
	ev := &tgEval{readFile: func(p string) ([]byte, error) { reads++; return []byte(files["live/common.hcl"]), nil }}
	src := "include \"c\" {\n  path = \"../../common.hcl\"\n}\n"
	b, _ := parseHCL([]byte(src))
	sc := newScope("live/prod/app/terragrunt.hcl", []byte(src), b, false)
	for range 2 {
		if _, ok := ev.include(sc, "c"); !ok {
			t.Fatal("include not resolved")
		}
	}
	if reads != 1 {
		t.Errorf("reads = %d", reads)
	}
}
