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

// upgradeStub installs a fake `name` CLI that requires `init ... -upgrade`, logs its arguments
// and, unless $SAME is set, appends a marker entry to the lock.
func upgradeStub(t *testing.T, name string) (log string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!` + sh + `
echo "$*" > "$LOG"
case " $* " in *" -upgrade "*) ;; *) echo "not an upgrade" >&2; exit 1;; esac
test -f ../shared/mod/main.tf || { echo "local module missing" >&2; exit 1; }
[ -n "$SAME" ] || printf '# upgraded\n' >> .terraform.lock.hcl
`
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOG", log)
	return log
}

func TestLockerMaintain(t *testing.T) {
	log := upgradeStub(t, "terraform")
	dir, _, lock := workspace(t, "")
	out, err := Locker{}.Maintain(context.Background(), dir, map[string][]byte{LockFile: lock})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out[LockFile]); got != string(lock)+"# upgraded\n" {
		t.Errorf("lock:\n%s", got)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "init -backend=false -input=false -no-color -upgrade" {
		t.Errorf("args: %s", b)
	}

	t.Setenv("SAME", "1")
	if out, err := (Locker{}).Maintain(context.Background(), dir, map[string][]byte{LockFile: lock}); err != nil || out != nil {
		t.Errorf("unchanged lock: %v, %v", out, err)
	}
	if out, err := (Locker{}).Maintain(context.Background(), dir, nil); err != nil || out != nil {
		t.Errorf("no lock: %v, %v", out, err)
	}
}

func TestLockerMaintainOpenTofu(t *testing.T) {
	upgradeStub(t, "tofu")
	dir, _, lock := workspace(t, OpenTofuRegistry)
	if _, err := (Locker{}).Maintain(context.Background(), dir, map[string][]byte{LockFile: lock}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Maintain(context.Background(), dir, map[string][]byte{LockFile: lock}); err == nil || err.Error() != "tofu not found (needed to update .terraform.lock.hcl)" {
		t.Errorf("err = %v", err)
	}
}

func TestLockRoots(t *testing.T) {
	dir, _, _ := workspace(t, "")
	root := filepath.Dir(filepath.Dir(dir))
	files := []string{"env/prod/main.tf", "env/prod/versions.tf", "env/shared/mod/main.tf"}
	got := New().(manager.LockMaintainer).LockRoots(root, files)
	want := []manager.LockRoot{{Manifest: "env/prod/main.tf", Lockfile: "env/prod/" + LockFile}}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %+v", got)
	}
}
