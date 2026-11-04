package terraform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestConstraintRanges(t *testing.T) {
	src := `terraform {
  required_providers {
    exact = { source = "x/exact", version = "= 1.2.3" }
    pess  = { source = "x/pess", version = "~> 5.0" }
    lower = { source = "x/lower", version = ">= 1.5.7" }
    fixed = { source = "x/fixed", version = ">= 4.0, < 6.0" }
    upper = { source = "x/upper", version = "< 2" }
  }
}
`
	deps, err := New().Extract("main.tf", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		version, rng string
		fixed        bool
	}{
		"x/exact": {"1.2.3", "= 1.2.3", false},
		"x/pess":  {"5.0", "~> 5.0", false},
		"x/lower": {"1.5.7", ">= 1.5.7", false},
		"x/fixed": {"4.0", ">= 4.0, < 6.0", true},
		"x/upper": {"2", "< 2", true},
	}
	for _, d := range deps {
		w := want[d.Name]
		if d.Version != w.version || d.Range != w.rng || d.RangeFixed != w.fixed || d.Skip != "" {
			t.Errorf("%s: version %q range %q fixed %v skip %q", d.Name, d.Version, d.Range, d.RangeFixed, d.Skip)
		}
		if !d.RangeFixed && src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
			t.Errorf("%s: RefSpan %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End])
		}
	}
}

func TestDefaultRegistry(t *testing.T) {
	src := []byte(`terraform {
  required_version = "~> 1.8"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
    own = { source = "example.com/o/own", version = "1.0.0" }
  }
}
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}
`)
	for _, tt := range []struct {
		file, registry, want, cli string
	}{
		{"main.tf", "", "https://registry.terraform.io", "hashicorp/terraform"},
		{"main.tofu", "", "https://registry.opentofu.org", "opentofu/opentofu"},
		{"main.tf", "https://Registry.OpenTofu.org/", "https://registry.opentofu.org", "hashicorp/terraform"},
		{"main.tofu", "mirror.example.com", "https://mirror.example.com", "opentofu/opentofu"},
	} {
		deps, err := New().Extract(tt.file, src, manager.Env{TerraformRegistry: tt.registry})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, d := range deps {
			got[d.Name] = d.Source
		}
		if got["hashicorp/aws"] != tt.want || got["terraform-aws-modules/vpc/aws"] != tt.want || got["o/own"] != "https://example.com" || deps[0].Name != tt.cli {
			t.Errorf("%s with %q: %v (cli %s)", tt.file, tt.registry, got, deps[0].Name)
		}
	}
}

func TestEditsRanges(t *testing.T) {
	for _, tt := range []struct {
		d    dep.Dep
		next string
		want string
		err  string
	}{
		{dep.Dep{Version: "5.0", Versioning: "hashicorp", RefSpan: dep.Span{Start: 3, End: 6}}, "6.2.1", "6.2", ""},
		{dep.Dep{Version: "v1.5.7", Versioning: "hashicorp", RefSpan: dep.Span{Start: 3, End: 9}}, "1.9.5", "v1.9.5", ""},
		{dep.Dep{Version: "v1.2.3", Versioning: "semver", RefSpan: dep.Span{Start: 0, End: 6}}, "v1.3.0", "v1.3.0", ""},
		{dep.Dep{Version: "4.0", Range: ">= 4.0, < 6.0", RangeFixed: true}, "5.9.0", "", `range ">= 4.0, < 6.0" is not rewritten`},
	} {
		e, err := New().Edits(dep.Update{Dep: tt.d, NewVersion: tt.next})
		switch {
		case tt.err != "":
			if err == nil || err.Error() != tt.err {
				t.Errorf("%+v: err = %v", tt.d, err)
			}
		case err != nil || len(e) != 1 || e[0].New != tt.want || e[0].Start != tt.d.RefSpan.Start:
			t.Errorf("%+v: edits = %+v, %v", tt.d, e, err)
		}
	}
}

func TestReadLocks(t *testing.T) {
	root := t.TempDir()
	lock := read(t, "moj/terraform.lock.hcl")
	tofuLock := strings.ReplaceAll(string(lock), "registry.terraform.io/", OpenTofuRegistry+"/")
	for name, b := range map[string]string{
		"a/" + LockFile: string(lock),
		"b/" + LockFile: tofuLock,
		"c/" + LockFile: "provider {",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := read(t, "moj/versions.tf")
	var files [][]dep.Dep
	for _, f := range []string{"a/versions.tf", "b/versions.tf", "c/versions.tf", "d/versions.tf"} {
		deps, err := New().Extract(f, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, deps)
	}
	errs := New().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "c/.terraform.lock.hcl: line 1") {
		t.Errorf("errs = %v", errs)
	}
	for i, want := range []string{"5.83.0", "5.83.0", "", ""} {
		var aws, tfVersion dep.Dep
		for _, d := range files[i] {
			switch d.Name {
			case "hashicorp/aws":
				aws = d
			case terraformRepo:
				tfVersion = d
			}
		}
		if aws.Locked != want || tfVersion.Locked != "" {
			t.Errorf("file %d: aws locked %q, required_version locked %q", i, aws.Locked, tfVersion.Locked)
		}
	}
}

// pinStub installs a fake terraform that checks the override pinning hashicorp/random and the
// stripped lock, then locks random at $LOCKVER with the override's constraint.
func pinStub(t *testing.T) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	bin := t.TempDir()
	script := `#!` + sh + `
grep -q 'random = {' ` + overrideFile + ` || { echo "no override" >&2; exit 1; }
grep -q 'source  = "hashicorp/random"' ` + overrideFile + ` || { echo "no source" >&2; exit 1; }
grep -q 'version = "= 3.6.9"' ` + overrideFile + ` || { echo "no pin" >&2; exit 1; }
if grep -q 'hashicorp/random"' .terraform.lock.hcl; then echo "stale random entry" >&2; exit 1; fi
printf 'provider "registry.terraform.io/hashicorp/random" {\n  version     = "%s"\n  constraints = "= 3.6.9, >= 3.0.0"\n  hashes = ["h1:x="]\n}\n' "$LOCKVER" >> .terraform.lock.hcl
`
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLockerLockfileOnly(t *testing.T) {
	pinStub(t)
	dir, _, lock := workspace(t, "")
	versions := read(t, "moj/versions.tf")
	up := dep.Update{Dep: dep.Dep{Manager: Name, Name: "hashicorp/random", Source: "https://registry.terraform.io", Locked: "3.6.3"}, NewVersion: "3.6.9", Lockfile: true}
	run := func(lock []byte, u dep.Update) (map[string][]byte, error) {
		return Locker{}.ProcessLocked(context.Background(), dir, map[string][]byte{"versions.tf": versions},
			map[string][]byte{LockFile: lock}, map[string][]dep.Update{"versions.tf": {u}})
	}

	t.Setenv("LOCKVER", "3.6.9")
	out, err := run(lock, up)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out[LockFile])
	if !strings.Contains(got, "provider \"registry.terraform.io/hashicorp/random\" {\n  version     = \"3.6.9\"\n  constraints = \">= 3.0.0\"\n") ||
		!strings.Contains(got, `provider "registry.terraform.io/hashicorp/aws" {`) {
		t.Errorf("lock:\n%s", got)
	}

	// An entry without constraints gets none back.
	noConstraints := strings.Replace(string(lock), "  constraints = \">= 3.0.0\"\n  hashes", "  hashes", 1)
	if out, err = run([]byte(noConstraints), up); err != nil || strings.Contains(string(out[LockFile]), "= 3.6.9, >= 3.0.0") {
		t.Errorf("without constraints: %v\n%s", err, out[LockFile])
	}

	t.Setenv("LOCKVER", "3.7.0")
	if _, err := run(lock, up); err == nil || err.Error() != ".terraform.lock.hcl locked hashicorp/random 3.7.0, want 3.6.9" {
		t.Errorf("wrong version: %v", err)
	}

	other := up
	other.Dep.Name = "x/unlocked"
	if _, err := run(lock, other); err == nil || err.Error() != ".terraform.lock.hcl does not lock x/unlocked" {
		t.Errorf("unlocked: %v", err)
	}

	notRequired := up
	notRequired.Dep.Name = "hashicorp/kubernetes" // locked, but not in versions.tf
	if _, err := run(lock, notRequired); err == nil || !strings.HasPrefix(err.Error(), "no required_providers entry for hashicorp/kubernetes") {
		t.Errorf("not required: %v", err)
	}
}

func TestLockName(t *testing.T) {
	for in, want := range map[string]string{
		"registry.terraform.io/HashiCorp/AWS": "hashicorp/aws",
		"registry.opentofu.org/hashicorp/aws": "hashicorp/aws",
		"hashicorp/aws":                       "hashicorp/aws",
	} {
		if got := lockName(in); got != want {
			t.Errorf("lockName(%s) = %s", in, got)
		}
	}
}
