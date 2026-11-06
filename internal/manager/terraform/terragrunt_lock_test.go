package terraform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestLockConfig(t *testing.T) {
	cfg, tofu, err := lockConfig(read(t, "moj/terraform.lock.hcl"))
	if err != nil || tofu {
		t.Fatalf("tofu %v, err %v", tofu, err)
	}
	for _, want := range []string{
		"terraform {\n  required_providers {\n    p0 = {\n      source = \"registry.terraform.io/anschoewe/curl\"\n      version = \">= 1.0.2\"\n    }\n",
		"    p1 = {\n      source = \"registry.terraform.io/auth0/auth0\"\n      version = \">= 0.34.0, ~> 1.9.1\"\n    }\n",
		"    p13 = {\n      source = \"registry.terraform.io/phillbaker/elasticsearch\"\n      version = \"~> 2.0.7\"\n    }\n  }\n}\n",
	} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	// The generated configuration parses as Terraform and requires every locked provider.
	deps, err := New().Extract("providers.tf", cfg, manager.Env{})
	if err != nil || len(deps) != 14 {
		t.Errorf("deps %d, %v", len(deps), err)
	}

	cfg, tofu, err = lockConfig([]byte("provider \"registry.opentofu.org/hashicorp/aws\" {\n  version = \"5.0.0\"\n}\n"))
	if err != nil || !tofu || string(cfg) != "terraform {\n  required_providers {\n    p0 = {\n      source = \"registry.opentofu.org/hashicorp/aws\"\n    }\n  }\n}\n" {
		t.Errorf("tofu lock: %q, %v, %v", cfg, tofu, err)
	}
	if cfg, _, err := lockConfig([]byte("# empty\n")); cfg != nil || err != nil {
		t.Errorf("empty lock: %q, %v", cfg, err)
	}
	if _, _, err := lockConfig([]byte("provider {")); err == nil {
		t.Error("broken lock parsed")
	}
}

// configStub installs a fake `name` CLI that requires `init ... -upgrade` in a directory holding
// only the lock and the generated providers.tf, and appends a marker entry to the lock.
func configStub(t *testing.T, name string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	bin := t.TempDir()
	script := `#!` + sh + `
case " $* " in *" -upgrade "*) ;; *) echo "not an upgrade" >&2; exit 1;; esac
[ "$(ls -A | LC_ALL=C sort | tr '\n' ' ')" = ".terraform.lock.hcl providers.tf " ] || { ls -A >&2; exit 1; }
grep -q 'source = "registry.terraform.io/hashicorp/aws"' providers.tf || exit 1
printf '# upgraded\n' >> .terraform.lock.hcl
`
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestTerragruntLockerMaintain(t *testing.T) {
	configStub(t, "terraform")
	lock := []byte("provider \"registry.terraform.io/hashicorp/aws\" {\n  version     = \"5.0.0\"\n  constraints = \"~> 5.0\"\n}\n")
	dir := t.TempDir()
	out, err := TerragruntLocker{}.Maintain(context.Background(), dir, map[string][]byte{LockFile: lock})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out[LockFile]); got != string(lock)+"# upgraded\n" {
		t.Errorf("lock:\n%s", got)
	}
	for _, files := range []map[string][]byte{nil, {LockFile: []byte("# no providers\n")}} {
		if out, err := (TerragruntLocker{}).Maintain(context.Background(), dir, files); err != nil || out != nil {
			t.Errorf("%v: %v, %v", files, out, err)
		}
	}
	if out, err := (TerragruntLocker{}).Process(context.Background(), dir, nil, map[string][]byte{LockFile: lock}); err != nil || out != nil {
		t.Errorf("Process: %v, %v", out, err)
	}
	if got := (TerragruntLocker{}).Siblings(); !slices.Equal(got, []string{LockFile}) {
		t.Errorf("Siblings = %v", got)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (TerragruntLocker{}).Maintain(context.Background(), dir, map[string][]byte{LockFile: lock}); err == nil {
		t.Error("maintained without terraform")
	}
}

func TestTerragruntLockRoots(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"live/a/terragrunt.hcl", "live/a/" + LockFile, "live/b/terragrunt.hcl", "live/c/terragrunt.hcl", "live/c/main.tf", "live/c/" + LockFile} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := NewTerragrunt().(manager.LockMaintainer).LockRoots(root, []string{"live/c/terragrunt.hcl", "live/b/terragrunt.hcl", "live/a/terragrunt.hcl"})
	want := []manager.LockRoot{{Manifest: "live/a/terragrunt.hcl", Lockfile: "live/a/" + LockFile}}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %+v", got)
	}
	if dir, ok := (TerragruntLocker{}).Scope(context.Background(), root, "live/a/terragrunt.hcl"); !ok || dir != "live/a" {
		t.Errorf("Scope = %q, %v", dir, ok)
	}
}
