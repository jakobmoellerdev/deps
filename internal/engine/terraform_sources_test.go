package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
)

// Terraform and Terragrunt module sources: a commit-pinned git source tracks the default
// branch (digest-only), an OCI source moves its tag without a digest pin, a `tags/` ref moves
// after its prefix, and a Terragrunt source built from an included local keeps its ref
// editable in the terragrunt.hcl.
func TestTerraformModuleSources(t *testing.T) {
	const (
		old  = "0000000000000000000000000000000000000000"
		head = "1111111111111111111111111111111111111111"
	)
	main := `module "pinned" {
  source = "git::https://github.com/acme/mod.git//x?ref=` + old + `"
}
module "oci" {
  source = "oci://ghcr.io/acme/vpc?tag=1.0.0"
}
module "cp" {
  source = "git::https://github.com/cloudposse/x.git?ref=tags/0.4.0"
}
`
	tg := `include "c" {
  path   = "${dirname(find_in_parent_folders("root.hcl"))}/_envcommon/app.hcl"
  expose = true
}
terraform {
  source = "${include.c.locals.base}?ref=v1.0.0"
}
`
	ms := []manager.Manager{terraform.New(), terraform.NewTerragrunt()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"git-refs": fakeBranches{"https://github.com/acme/mod.git#HEAD": head},
		"git-tags": fakeTags{"https://github.com/cloudposse/x.git": {"0.4.0", "0.5.0"}, "https://github.com/acme/app.git": {"v1.0.0", "v1.1.0"}},
		"docker":   fakeDocker{"ghcr.io/acme/vpc": {"1.0.0", "1.1.0"}},
	}}
	root, cfg := setup(t, "separate: none\npin: digest\n", map[string]string{
		"infra/main.tf":                main,
		"live/root.hcl":                "",
		"live/_envcommon/app.hcl":      "locals {\n  base = \"git::https://github.com/acme/app.git//modules/app\"\n}\n",
		"live/prod/app/terragrunt.hcl": tg,
	})
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{
		{"github.com/acme/mod", "HEAD", head, dep.Digest},
		{"ghcr.io/acme/vpc", "1.1.0", "", dep.Minor},
		{"github.com/cloudposse/x", "0.5.0", "", dep.Minor},
		{"github.com/acme/app", "v1.1.0", "", dep.Minor},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v\nwant      %+v", got, want)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 2 {
		t.Fatalf("changes = %+v, errs = %v", changes, errs)
	}
	out := string(changes[0].New) + string(changes[1].New)
	for _, s := range []string{"x?ref=" + head + `"`, `?tag=1.1.0"`, `?ref=tags/0.5.0"`, `locals.base}?ref=v1.1.0"`} {
		if !strings.Contains(out, s) {
			t.Errorf("changes lack %s:\n%s", s, out)
		}
	}
}
