package conda

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// The testdata/conda-lock fixture was locked by conda-lock 4.0.5 (`conda-lock lock -f
// environment.yml --lockfile conda-lock.yml`, linux-64) with numpy==1.26.4 and
// requests==2.31.0, then the specs were opened to numpy>=1.26 and requests>=2.31.
const lockDir = "testdata/conda-lock"

func TestReadLocks(t *testing.T) {
	const env = "conda-lock/environment.yml"
	deps := extract(t, env, read(t, filepath.Join(lockDir, "environment.yml")))
	other := []dep.Dep{{Manager: Name, Name: "numpy", File: "nolock/environment.yml", Range: ">=1", Version: "1"}}
	m := New().(manager.LockReader)
	if errs := m.ReadLocks("testdata", [][]dep.Dep{deps, other}); errs != nil {
		t.Fatal(errs)
	}
	got := map[string]string{}
	for _, d := range deps {
		got[d.Name] = d.Locked
	}
	if got["numpy"] != "1.26.4" || got["requests"] != "2.31.0" || got["python"] != "3.11.18" || got["pip"] != "" || other[0].Locked != "" {
		t.Errorf("locked = %v, other = %+v", got, other[0])
	}

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, lockFile), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ds := []dep.Dep{{Manager: Name, Name: "numpy", File: "environment.yml", Range: ">=1"}, {Manager: Name, Name: "scipy", File: "environment.yml", Range: ">=1"}}
	if errs := m.ReadLocks(bad, [][]dep.Dep{ds}); len(errs) != 1 || !ds[0].LockUnread || !ds[1].LockUnread {
		t.Errorf("unreadable lock = %v, %+v", errs, ds)
	}
}

func TestLocked(t *testing.T) {
	l, err := parseLock([]byte(`version: 1
metadata: {platforms: [linux-64, osx-arm64], sources: [environment.yml]}
package:
- {name: NumPy, version: 2.1.0, manager: conda, platform: linux-64}
- {name: numpy, version: 2.0.2, manager: conda, platform: osx-arm64}
- {name: Typing_Extensions, version: 4.12.2, manager: pip, platform: linux-64}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := l.locked()
	if got[[2]string{"conda", "numpy"}] != "2.0.2" || got[[2]string{"pip", "typing-extensions"}] != "4.12.2" {
		t.Errorf("locked = %v", got)
	}
	for _, src := range []string{"[", "version: 1\n", "package: []\n"} {
		if _, err := parseLock([]byte(src)); err == nil {
			t.Errorf("parseLock(%q): no error", src)
		}
	}
}

func TestLockRoots(t *testing.T) {
	root, _ := filepath.Abs("testdata")
	roots := New().(manager.LockMaintainer).LockRoots(root, []string{"pandas/environment.yml", "conda-lock/environment.yml"})
	if len(roots) != 1 || roots[0] != (manager.LockRoot{Manifest: "conda-lock/environment.yml", Lockfile: "conda-lock/conda-lock.yml"}) {
		t.Errorf("LockRoots = %v", roots)
	}
	if (Locker{}).Siblings()[0] != lockFile {
		t.Error("Siblings")
	}
}

// stub installs a fake conda-lock on PATH (only it: it uses shell builtins) that logs its
// arguments and the environment.yml it sees and appends its arguments to conda-lock.yml; with
// STUB_FAIL set it fails, with STUB_NOOP it changes nothing.
func stub(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
cwd=$(pwd)
{
  echo "args: $*"
  while IFS= read -r l; do echo "  $l"; done < environment.yml
  [ -L conda-lock.yml ] && echo "conda-lock.yml is a symlink"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "Could not lock the environment in $cwd"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "# $*" >> conda-lock.yml
`
	if err := os.WriteFile(filepath.Join(bin, "conda-lock"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	return log
}

func TestProcessUpdates(t *testing.T) {
	log := stub(t)
	dir := t.TempDir()
	lock := "version: 1\nmetadata:\n  platforms: [linux-64, osx-arm64]\n  sources: [environment.yml]\npackage: []\n"
	for name, content := range map[string]string{"environment.yml": "dependencies: [numpy>=1.26]\n", lockFile: lock} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{lockFile: []byte(lock)}
	patched := map[string][]byte{"environment.yml": []byte("dependencies: [numpy>=2.0]\n")}
	up := func(name, depType string) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: Name, Name: name, DepType: depType, File: "environment.yml"}, NewVersion: "2.0.0"}
	}
	ups := map[string][]dep.Update{"environment.yml": {up("NumPy", DepTypeDependencies)}}
	locks := map[string][]dep.Update{"environment.yml": {up("Typing_Extensions", DepTypePip), up("numpy", DepTypeDependencies)}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, patched, files, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	args := "lock -f environment.yml --lockfile conda-lock.yml -p linux-64 -p osx-arm64 --update numpy --update typing-extensions"
	if got := string(out[lockFile]); got != lock+"# "+args+"\n" {
		t.Errorf("conda-lock.yml = %q", got)
	}
	if want := "args: " + args + "\n  dependencies: [numpy>=2.0]\n"; string(read(t, log)) != want {
		t.Errorf("log:\n%s\nwant:\n%s", read(t, log), want)
	}
	if string(read(t, filepath.Join(dir, lockFile))) != lock {
		t.Error("the working tree was modified")
	}

	os.Remove(log)
	if out, err := (Locker{}).Maintain(t.Context(), dir, files); err != nil || !strings.HasSuffix(string(out[lockFile]), "# lock -f environment.yml --lockfile conda-lock.yml -p linux-64 -p osx-arm64\n") {
		t.Errorf("Maintain = %q, %v", out[lockFile], err)
	}
	// Without recorded sources the patched manifests are locked.
	bare := map[string][]byte{lockFile: []byte("version: 1\npackage: []\n")}
	if out, err := (Locker{}).Process(t.Context(), dir, patched, bare); err != nil || !strings.HasSuffix(string(out[lockFile]), "# lock -f environment.yml --lockfile conda-lock.yml\n") {
		t.Errorf("Process = %q, %v", out[lockFile], err)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "conda-lock lock in "+dir+": Could not lock the environment in "+dir {
		t.Errorf("failure = %v", err)
	}
	if out, err := (Locker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, nil, nil, locks); err == nil {
		t.Error("lockfile-only update without conda-lock.yml")
	}
	if _, err := (Locker{}).Process(t.Context(), dir, nil, map[string][]byte{lockFile: []byte("{")}); err == nil {
		t.Error("unparsable lockfile")
	}
	if _, err := (Locker{}).Process(t.Context(), filepath.Join(dir, "missing"), nil, files); err == nil {
		t.Error("mirror of a missing dir")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "conda-lock not found (needed to update conda-lock.yml)" {
		t.Errorf("no conda-lock = %v", err)
	}
}
