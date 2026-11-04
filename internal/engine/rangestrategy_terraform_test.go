package engine

import (
	"maps"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
)

const tfRangeConfig = `terraform {
  required_providers {
    a = { source = "x/a", version = "~> 5.0" }
    b = { source = "x/b", version = ">= 4.0, < 6.0" }
    c = { source = "x/c", version = "3.2.1" }
    d = { source = "x/d", version = ">= 1.0" }
  }
}
`

const tfRangeLock = `provider "registry.terraform.io/x/a" {
  version     = "5.1.0"
  constraints = "~> 5.0"
}
provider "registry.terraform.io/x/b" {
  version     = "4.5.0"
  constraints = ">= 4.0, < 6.0"
}
provider "registry.terraform.io/x/c" {
  version     = "3.2.1"
  constraints = "3.2.1"
}
provider "registry.terraform.io/x/d" {
  version     = "1.0.0"
  constraints = ">= 1.0"
}
`

var tfRangeReleases = fakeGo{
	"x/a": {"5.0.0", "5.1.0", "5.2.0", "6.0.0", "6.1.0"},
	"x/b": {"4.5.0", "5.9.0", "6.0.0"},
	"x/c": {"3.2.1", "3.3.0"},
	"x/d": {"1.0.0", "1.5.0"},
}

func TestTerraformRangeStrategy(t *testing.T) {
	lock := map[string]string{"env/.terraform.lock.hcl": tfRangeLock}
	for _, tt := range []struct {
		name, cfg string
		files     map[string]string // besides env/main.tf
		want      []string
	}{
		{
			// Renovate's terraform default: admitted versions are left alone, the others replace
			// the constraint; ranges that cannot be rewritten stay as they are.
			name: "auto is replace", files: lock,
			want: []string{"x/a 5.0->6.1.0 major", "x/c 3.2.1->3.3.0 minor"},
		},
		{
			name: "replace without a lock file", cfg: "rangeStrategy: replace\n",
			want: []string{"x/a 5.0->6.1 major", "x/c 3.2.1->3.3.0 minor"},
		},
		{
			name: "update-lockfile", cfg: "rangeStrategy: update-lockfile\n", files: lock,
			want: []string{
				"x/a 5.1.0->5.2.0 minor lockfile", // admitted: only the lock moves
				"x/a 5.0->6.1.0 major",
				"x/b 4.5.0->5.9.0 major lockfile", // never rewritten: 6.0.0 is outside
				"x/c 3.2.1->3.3.0 minor",
				"x/d 1.0.0->1.5.0 minor lockfile",
			},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n", files: lock,
			want: []string{
				"x/a 5.0->5.2 minor", "x/a 5.0->6.1 major",
				"x/b 4.5.0->5.9.0 major lockfile", // a fixed range only moves in the lock
				"x/c 3.2.1->3.3.0 minor",
				"x/d 1.0->1.5 minor",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"env/main.tf": tfRangeConfig}
			maps.Copy(files, tt.files)
			root, cfg := setup(t, tt.cfg, files)
			o := Options{Managers: []manager.Manager{terraform.New()}, Datasources: map[string]Datasource{"terraform-provider": tfRangeReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			if got := renderUpdates(res.Updates); got != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", got, strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestTerraformBrokenLock(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{"env/main.tf": tfRangeConfig, "env/.terraform.lock.hcl": "provider {"})
	o := Options{Managers: []manager.Manager{terraform.New()}, Datasources: map[string]Datasource{"terraform-provider": tfRangeReleases}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "env/.terraform.lock.hcl: line 1") {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
}
