package terraform

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixture, copied unchanged: testdata/bfd/opentofu-version is .opentofu-version of
// github.com/CMSgov/beneficiary-fhir-data@ac57ee8 (CC0-1.0), renamed without the leading dot.

func TestOpenTofuVersionFile(t *testing.T) {
	const tofu = "opentofu-version github-releases opentofu/opentofu https://github.com/opentofu/opentofu "
	for _, tt := range []struct{ src, want string }{
		{string(read(t, "bfd/opentofu-version")), "1 " + tofu + "1.12.1 hashicorp"},
		{"\n v1.8 \n", "2 " + tofu + "v1.8 hashicorp"},
		{"1.6.0-rc1\n", "1 " + tofu + "1.6.0-rc1 hashicorp"},
		{"latest\n", "1 " + tofu + "latest hashicorp [non-version value]"},
		{"latest:^1.8\n", "1 " + tofu + "latest:^1.8 hashicorp [non-version value]"},
		{"min-required\n", "1 " + tofu + "min-required hashicorp [non-version value]"},
		{"latest-allowed\n", "1 " + tofu + "latest-allowed hashicorp [non-version value]"},
		{"", ""},
	} {
		got := extract(t, NewVersion(), "ops/services/.opentofu-version", []byte(tt.src))
		if strings.Join(got, "\n") != tt.want {
			t.Errorf("%q: got %v, want %s", tt.src, got, tt.want)
		}
	}
}

func TestVersionFileMatch(t *testing.T) {
	m := NewVersion()
	for p, want := range map[string]bool{
		".terraform-version":             true,
		".opentofu-version":              true,
		"ops/services/.opentofu-version": true,
		"opentofu-version":               false,
		".opentofu-version.bak":          false,
		".tofu-version":                  false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}

// Fixture, copied unchanged: testdata/aws-eks-base/terragrunt-version is
// terragrunt/.terragrunt-version of github.com/maddevsio/aws-eks-base@a1ee242 (Apache-2.0),
// renamed without the leading dot.

func TestTerragruntVersionFile(t *testing.T) {
	const tg = "terragrunt-version github-releases gruntwork-io/terragrunt https://github.com/gruntwork-io/terragrunt "
	for _, tt := range []struct{ src, want string }{
		{string(read(t, "aws-eks-base/terragrunt-version")), "1 " + tg + "0.45.0 hashicorp"},
		{"\n v0.67 \r\n", "2 " + tg + "v0.67 hashicorp"},
		{"latest\n", "1 " + tg + "latest hashicorp [non-version value]"},
		{"latest:^0.5\n", "1 " + tg + "latest:^0.5 hashicorp [non-version value]"},
		{"min-required\n", "1 " + tg + "min-required hashicorp [non-version value]"},
		{"", ""},
	} {
		got := extract(t, NewTerragruntVersion(), "terragrunt/.terragrunt-version", []byte(tt.src))
		if strings.Join(got, "\n") != tt.want {
			t.Errorf("%q: got %v, want %s", tt.src, got, tt.want)
		}
	}
}

func TestTerragruntVersionFileMatchAndEdit(t *testing.T) {
	m := NewTerragruntVersion()
	if m.Name() != TerragruntVersionName {
		t.Errorf("Name() = %q", m.Name())
	}
	for p, want := range map[string]bool{
		".terragrunt-version":            true,
		"live/prod/.terragrunt-version":  true,
		"terragrunt-version":             false,
		".terraform-version":             false,
		".terragrunt-version.bak":        false,
		"live/prod/x.terragrunt-version": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
	if NewVersion().Match(".terragrunt-version") {
		t.Error("terraform-version matches .terragrunt-version")
	}
	src := []byte("v0.67\n")
	deps, err := m.Extract(".terragrunt-version", src, manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatalf("Extract = %v, %v", deps, err)
	}
	edits, err := m.Edits(dep.Update{Dep: deps[0], NewVersion: "0.68.1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, edits)
	if err != nil || string(out) != "v0.68\n" {
		t.Errorf("patched = %q, %v; want %q", out, err, "v0.68\n")
	}
}
