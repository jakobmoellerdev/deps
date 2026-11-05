package terraform

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stub installs a fake `name` CLI in PATH that logs its arguments, working directory and the
// patched versions.tf, checks what init would read, and appends an entry to the lock.
func stub(t *testing.T, name string) (log string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!` + sh + `
{ echo "$*"; pwd; echo "TF_IN_AUTOMATION=$TF_IN_AUTOMATION TF_DATA_DIR=$TF_DATA_DIR"; } > "$LOG"
if [ -n "$FAIL" ]; then echo "Error: Failed to query available provider packages" >&2; exit 1; fi
if grep -q 'hashicorp/aws"' .terraform.lock.hcl; then echo "stale aws entry" >&2; exit 1; fi
grep -q 'hashicorp/null"' .terraform.lock.hcl || { echo "null entry dropped" >&2; exit 1; }
test -f ../shared/mod/main.tf || { echo "local module missing" >&2; exit 1; }
test -e .terraform || true
grep '"~> 6.71' versions.tf >> "$LOG"
printf 'provider "registry.terraform.io/hashicorp/aws" {\n  version = "6.71.0"\n}\n' >> .terraform.lock.hcl
`
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOG", log)
	return log
}

// workspace lays out env/prod (the moj versions.tf and lock, a local module call) next to
// env/shared/mod and returns the prod directory, its patched versions.tf and the lock.
func workspace(t *testing.T, lockHost string) (dir string, patched, lock []byte) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "env", "prod")
	versions := read(t, "moj/versions.tf")
	lock = read(t, "moj/terraform.lock.hcl")
	if lockHost != "" {
		lock = []byte(strings.ReplaceAll(string(lock), "registry.terraform.io/", lockHost+"/"))
	}
	for name, b := range map[string]string{
		"env/prod/versions.tf":          string(versions),
		"env/prod/main.tf":              `module "m" { source = "../shared/mod" }` + "\n",
		"env/prod/" + LockFile:          string(lock),
		"env/prod/.terraform/modules/x": "stale",
		"env/prod/sub/other.tf":         "",
		"env/shared/mod/main.tf":        `module "n" { source = "../../../other" }` + "\n",
		"env/shared/mod/broken.tf":      "x = {",
		"env/shared/mod/notes.txt":      "",
		"env/readme.md":                 "",
		"top.txt":                       "",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	patched = []byte(strings.Replace(string(versions), `"~> 5.83.0"`, `"~> 6.71.0"`, 1))
	return dir, patched, lock
}

func TestLockerScope(t *testing.T) {
	dir, _, _ := workspace(t, "")
	root := filepath.Dir(filepath.Dir(dir))
	if got, ok := (Locker{}).Scope(root, "env/prod/versions.tf"); !ok || got != "env/prod" {
		t.Errorf("Scope = %q, %v", got, ok)
	}
	if _, ok := (Locker{}).Scope(root, "env/shared/mod/main.tf"); ok {
		t.Error("scope without a lock file")
	}
	if s := (Locker{}).Siblings(); len(s) != 1 || s[0] != LockFile {
		t.Errorf("Siblings = %v", s)
	}
}

func TestLockerRegenerates(t *testing.T) {
	log := stub(t, "terraform")
	dir, patched, lock := workspace(t, "")
	out, err := Locker{}.Process(context.Background(), dir, map[string][]byte{"versions.tf": patched}, map[string][]byte{LockFile: lock})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out[LockFile])
	if !strings.HasSuffix(got, "provider \"registry.terraform.io/hashicorp/aws\" {\n  version = \"6.71.0\"\n}\n") ||
		!strings.Contains(got, `provider "registry.terraform.io/hashicorp/null" {`) || strings.Contains(got, `version     = "5.83.0"`) {
		t.Errorf("lock:\n%s", got)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || lines[0] != "init -backend=false -input=false -no-color" ||
		!strings.HasSuffix(lines[1], filepath.Join("env", "prod")) || strings.HasPrefix(lines[1], dir) ||
		!strings.HasPrefix(lines[2], "TF_IN_AUTOMATION=1 TF_DATA_DIR="+lines[1]) || !strings.Contains(lines[3], `"~> 6.71.0"`) {
		t.Errorf("log:\n%s", b)
	}
	if _, err := os.Stat(lines[1]); !os.IsNotExist(err) {
		t.Errorf("temporary copy left behind: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, LockFile)); !bytes.Equal(b, lock) {
		t.Error("working tree lock changed")
	}
}

func TestLockerOpenTofu(t *testing.T) {
	log := stub(t, "tofu")
	t.Setenv("FAIL", "1")
	dir, patched, lock := workspace(t, OpenTofuRegistry)
	_, err := Locker{}.Process(context.Background(), dir, map[string][]byte{"versions.tf": patched}, map[string][]byte{LockFile: lock})
	if err == nil || !strings.HasPrefix(err.Error(), "tofu init in "+dir+": ") || !strings.HasSuffix(err.Error(), "Error: Failed to query available provider packages") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(log); !strings.HasPrefix(string(b), "init ") {
		t.Errorf("log: %s", b)
	}
}

func TestLockerNoTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir, patched, lock := workspace(t, "")
	_, err := Locker{}.Process(context.Background(), dir, map[string][]byte{"versions.tf": patched}, map[string][]byte{LockFile: lock})
	if err == nil || err.Error() != "terraform not found (needed to update .terraform.lock.hcl)" {
		t.Fatalf("err = %v", err)
	}
}

func TestLockerNothingToDo(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // any tool run would fail
	dir, patched, lock := workspace(t, "")
	versions := read(t, "moj/versions.tf")
	unlocked := strings.Replace(string(versions), `version = "~> 2.0.7"`, `version = "~> 2.1.0"`, 1)
	unlockedLock := strings.Replace(string(lock), `provider "registry.terraform.io/phillbaker/elasticsearch"`, `provider "registry.terraform.io/other/elasticsearch"`, 1)
	for name, tt := range map[string]struct {
		manifest []byte
		files    map[string][]byte
	}{
		"no lock":           {patched, nil},
		"version unchanged": {versions, map[string][]byte{LockFile: lock}},
		"provider unlocked": {[]byte(unlocked), map[string][]byte{LockFile: []byte(unlockedLock)}},
	} {
		out, err := Locker{}.Process(context.Background(), dir, map[string][]byte{"versions.tf": tt.manifest}, tt.files)
		if err != nil || out != nil {
			t.Errorf("%s: %v, %v", name, out, err)
		}
	}
}

func TestLockerErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir, patched, lock := workspace(t, "")
	for name, tt := range map[string]struct {
		manifests map[string][]byte
		lock      string
		want      string
	}{
		"missing manifest": {map[string][]byte{"gone.tf": patched}, string(lock), "no such file"},
		"broken patch":     {map[string][]byte{"versions.tf": []byte("terraform {")}, string(lock), "unterminated block"},
		"dep count":        {map[string][]byte{"versions.tf": []byte("")}, string(lock), "patched file has 0 deps, want 9"},
		"broken lock":      {map[string][]byte{"versions.tf": patched}, "provider {", ".terraform.lock.hcl: line 1"},
	} {
		_, err := Locker{}.Process(context.Background(), dir, tt.manifests, map[string][]byte{LockFile: []byte(tt.lock)})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "versions.tf"), []byte("terraform {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Locker{}).Process(context.Background(), dir, map[string][]byte{"versions.tf": patched}, map[string][]byte{LockFile: lock}); err == nil {
		t.Error("broken original: no error")
	}
}
