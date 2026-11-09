package jsonnetbundler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// stub installs a fake jb first on PATH (with /bin and /usr/bin for mkdir and ln) that logs its arguments,
// the jsonnetfile.json it sees and what of the mirror is visible, appends its arguments to
// jsonnetfile.lock.json and writes vendor/new.libsonnet and vendor/kept.libsonnet, and a
// symlink vendor/local; with STUB_FAIL set it fails, with STUB_NOOP it changes nothing.
func stub(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
{
  echo "args: $*"
  while IFS= read -r l; do echo "  $l"; done < "$PWD/jsonnetfile.json"
  [ -L "$PWD/jsonnetfile.json" ] && echo "jsonnetfile.json is a symlink"
  [ -L "$PWD/jsonnetfile.lock.json" ] && echo "lock is a symlink"
  [ -e "$PWD/.git" ] && echo ".git visible"
  [ -e "$PWD/vendor" ] && echo "vendor visible"
  [ -L "$PWD/lib" ] && echo "lib linked"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "jb: error: failed to install packages: downloading $PWD"
  exit 1
fi
[ -n "$STUB_NOOP" ] && exit 0
echo "$*" >> "$PWD/jsonnetfile.lock.json"
mkdir -p "$PWD/vendor"
echo new > "$PWD/vendor/new.libsonnet"
echo kept > "$PWD/vendor/kept.libsonnet"
ln -s ../lib "$PWD/vendor/local"
`
	if err := os.WriteFile(filepath.Join(bin, "jb"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	return log
}

func update(name string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, File: "app/jsonnetfile.json", Name: name}}
}

const manifest = `{"dependencies": [
  {"source": {"git": {"remote": "https://github.com/acme/a.git", "subdir": "lib/"}}, "version": "v1.1.0"},
  {"source": {"git": {"remote": "git@github.com:acme/b"}}, "version": "main"},
  {"source": {"local": {"directory": "lib"}}, "version": ""}
]}
`

func TestScope(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a/jsonnetfile.json": "{}", "a/jsonnetfile.lock.json": "{}", "b/jsonnetfile.json": "{}", "c/jsonnetfile.lock.json/x": ""})
	for m, want := range map[string]bool{"a/jsonnetfile.json": true, "b/jsonnetfile.json": false, "c/jsonnetfile.json": false, "a/other.json": false} {
		if dir, ok := (Locker{}).Scope(context.Background(), root, m); ok != want || (ok && dir != "a") {
			t.Errorf("Scope(%s) = %q, %v", m, dir, ok)
		}
	}
	if s := (Locker{}).Siblings(); len(s) != 1 || s[0] != lockFile {
		t.Error(s)
	}
	if tr := (Locker{}).Trees(filepath.Join(root, "a")); tr != nil {
		t.Errorf("Trees without vendor = %v", tr)
	}
	write(t, root, map[string]string{"a/vendor/x": ""})
	if tr := (Locker{}).Trees(filepath.Join(root, "a")); len(tr) != 1 || tr[0] != "vendor" {
		t.Errorf("Trees = %v", tr)
	}
}

func TestProcessUpdates(t *testing.T) {
	log := stub(t)
	repo := t.TempDir()
	before := strings.Replace(manifest, "v1.1.0", "v1.0.0", 1)
	write(t, repo, map[string]string{
		".git/HEAD": "", "app/jsonnetfile.json": before, "app/jsonnetfile.lock.json": "lock\n", "app/lib/main.libsonnet": "{}",
		"app/.git": "gitdir: elsewhere",
	})
	dir := filepath.Join(repo, "app")
	ups := map[string][]dep.Update{manifestName: {update("github.com/acme/a/lib"), update("github.com/acme/b")}}
	locks := map[string][]dep.Update{manifestName: {update("github.com/acme/b"), update("github.com/acme/unknown")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{manifestName: []byte(manifest)}, map[string][]byte{lockFile: []byte("lock\n")}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	args := "update https://github.com/acme/a.git/lib git@github.com:acme/b"
	// Without a vendor directory in the checkout, only the lock is taken back.
	if len(out) != 1 || string(out[lockFile]) != "lock\n"+args+"\n" {
		t.Errorf("out = %q", out)
	}
	var want strings.Builder
	want.WriteString("args: " + args + "\n")
	for l := range strings.Lines(manifest) {
		want.WriteString("  " + l)
	}
	want.WriteString("lib linked\n")
	if got := string(read(t, log)); got != want.String() {
		t.Errorf("log:\n%s\nwant:\n%s", got, want.String())
	}
	if string(read(t, filepath.Join(dir, manifestName))) != before || string(read(t, filepath.Join(dir, lockFile))) != "lock\n" {
		t.Error("the working tree was modified")
	}
}

// A committed vendor directory is regenerated: changed and new files are returned, files jb
// no longer writes are deleted, symlinks left alone; the checkout's vendor is not visible to
// jb, which would write through a symlink to it.
func TestProcessVendor(t *testing.T) {
	log := stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{manifestName: manifest, lockFile: "lock\n", "vendor/kept.libsonnet": "kept\n", "vendor/old.libsonnet": "old\n"})
	if err := os.Symlink("../lib", filepath.Join(dir, "vendor", "local")); err != nil {
		t.Fatal(err)
	}
	out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{lockFile: []byte("lock\n")})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || string(out[lockFile]) != "lock\nupdate\n" || string(out["vendor/new.libsonnet"]) != "new\n" ||
		out["vendor/old.libsonnet"] != nil {
		t.Errorf("out = %q", out)
	}
	if _, ok := out["vendor/old.libsonnet"]; !ok {
		t.Error("vendor/old.libsonnet not deleted")
	}
	if strings.Contains(string(read(t, log)), "vendor visible") {
		t.Errorf("log:\n%s", read(t, log))
	}
}

func TestProcessOutcomes(t *testing.T) {
	stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{manifestName: manifest, lockFile: "lock\n"})
	files := map[string][]byte{lockFile: []byte("lock\n")}

	// No updates: the lock is brought in line with jsonnetfile.json.
	out, err := Locker{}.Process(t.Context(), dir, nil, files)
	if err != nil || string(out[lockFile]) != "lock\ninstall\n" {
		t.Errorf("Process = %q, %v", out[lockFile], err)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	_, err = Locker{}.ProcessUpdates(t.Context(), dir, nil, files, map[string][]dep.Update{manifestName: {update("github.com/acme/b")}}, nil)
	if err == nil || err.Error() != "jb update in "+dir+": jb: error: failed to install packages: downloading "+dir {
		t.Errorf("failure = %v", err)
	}
	// Without a lock: nothing to do.
	if out, err := (Locker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil {
		t.Error("missing jsonnetfile.json")
	}
	if _, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{manifestName: []byte("{")}, files); err == nil || !strings.HasPrefix(err.Error(), "jsonnetfile.json: ") {
		t.Errorf("invalid jsonnetfile.json: %v", err)
	}
	if out, err := (Locker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("Maintain without lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).Maintain(t.Context(), t.TempDir(), files); err == nil {
		t.Error("Maintain: missing jsonnetfile.json")
	}
	if _, _, err := mirrorDir(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("mirror of a missing dir")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "jb not found (needed to update jsonnetfile.lock.json)" {
		t.Errorf("no jb: %v", err)
	}
}
