package nix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// stub installs a fake nix on PATH (only it: it uses shell builtins) that logs its arguments
// (the mirror's `path:` reference as `path:<mirror>`), NIX_CONFIG, the flake.nix it sees and
// what of the mirror is visible, and appends its arguments to flake.lock; with STUB_FAIL set
// it fails, with STUB_NOOP it changes nothing.
func stub(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
args=""
for a in "$@"; do
  [ "$a" = "path:$PWD" ] && a="path:<mirror>"
  args="$args $a"
done
{
  echo "args:$args"
  [ -n "$NIX_CONFIG" ] && echo "NIX_CONFIG: $NIX_CONFIG"
  while IFS= read -r l; do echo "  $l"; done < "$PWD/flake.nix"
  [ -L "$PWD/flake.nix" ] && echo "flake.nix is a symlink"
  [ -L "$PWD/flake.lock" ] && echo "flake.lock is a symlink"
  [ -e "$PWD/.git" ] && echo ".git visible"
  [ -L "$PWD/modules" ] && echo "modules linked"
  [ -e "$PWD/../lib/flake.nix" ] && echo "sibling visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "error: cannot find flake 'flake:missing' in $PWD"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "$args" >> "$PWD/flake.lock"
`
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	t.Setenv("NIX_CONFIG", "")
	return log
}

func update(name string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, File: "app/flake.nix", Name: name}}
}

const features = " --extra-experimental-features nix-command flakes "

func TestProcessUpdates(t *testing.T) {
	log := stub(t)
	repo := t.TempDir()
	before := "{ inputs.a.url = \"github:acme/a/v1.0.0\"; }\n"
	write(t, repo, map[string]string{
		".git/HEAD": "", "lib/flake.nix": "{}",
		"app/flake.nix": before, "app/flake.lock": "lock\n", "app/modules/x.nix": "{}", "app/.git": "gitdir: elsewhere",
	})
	dir := filepath.Join(repo, "app")
	patched := "{ inputs.a.url = \"github:acme/a/v1.1.0\"; }\n"
	ups := map[string][]dep.Update{"flake.nix": {update("a"), update("nixpkgs")}}
	locks := map[string][]dep.Update{"flake.nix": {update("nixpkgs"), update("b")}}
	t.Setenv("NIX_CONFIG", "warn-dirty = false")
	out, err := Locker{GitHubToken: "ghs_x"}.ProcessUpdates(t.Context(), dir, map[string][]byte{"flake.nix": []byte(patched)}, map[string][]byte{"flake.lock": []byte("lock\n")}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	args := features + "flake update a nixpkgs b --flake path:<mirror>"
	if got := string(out["flake.lock"]); got != "lock\n"+args+"\n" {
		t.Errorf("flake.lock = %q", got)
	}
	if want := "args:" + args + "\nNIX_CONFIG: warn-dirty = false\nextra-access-tokens = github.com=ghs_x\n  " + patched + "modules linked\nsibling visible\n"; string(read(t, log)) != want {
		t.Errorf("log:\n%s\nwant:\n%s", read(t, log), want)
	}
	if string(read(t, filepath.Join(dir, "flake.nix"))) != before || string(read(t, filepath.Join(dir, "flake.lock"))) != "lock\n" {
		t.Error("the working tree was modified")
	}
}

// TestProcessUnaddressableNames: nix reads `a/b` as input b of input a and rejects `foo.bar`,
// failing the whole command; such inputs are not named. Their updates change flake.nix, which
// nix re-locks anyway (`nix flake lock` when no input is left to name).
func TestProcessUnaddressableNames(t *testing.T) {
	stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"flake.nix": "{}", "flake.lock": "lock\n"})
	files := map[string][]byte{"flake.lock": []byte("lock\n")}
	for _, c := range []struct {
		names []string
		args  string
	}{
		{[]string{"foo.bar", "a/b", "ok", "x_1-y", "1st"}, "flake update ok x_1-y --flake path:<mirror>"},
		{[]string{"foo.bar", "a/b"}, "flake lock path:<mirror>"},
	} {
		var ups []dep.Update
		for _, n := range c.names {
			ups = append(ups, update(n))
		}
		out, err := Locker{}.ProcessUpdates(t.Context(), dir, nil, files, map[string][]dep.Update{"flake.nix": ups}, nil)
		if want := "lock\n" + features + c.args + "\n"; err != nil || string(out["flake.lock"]) != want {
			t.Errorf("%v: flake.lock = %q, %v; want %q", c.names, out["flake.lock"], err, want)
		}
	}
}

func TestProcessOutcomes(t *testing.T) {
	log := stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"flake.nix": "{}", "flake.lock": "lock\n"})
	files := map[string][]byte{"flake.lock": []byte("lock\n")}

	// No updates: the lock is brought in line with flake.nix; no token, no NIX_CONFIG.
	out, err := Locker{}.Process(t.Context(), dir, nil, files)
	if err != nil || string(out["flake.lock"]) != "lock\n"+features+"flake lock path:<mirror>\n" {
		t.Errorf("Process = %q, %v", out["flake.lock"], err)
	}
	if b := read(t, log); strings.Contains(string(b), "NIX_CONFIG") {
		t.Errorf("log:\n%s", b)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	_, err = Locker{}.ProcessUpdates(t.Context(), dir, nil, files, map[string][]dep.Update{"flake.nix": {update("a")}}, nil)
	if err == nil || err.Error() != "nix flake update in "+dir+": error: cannot find flake 'flake:missing' in "+dir {
		t.Errorf("failure = %v", err)
	}
	// Without flake.lock: nothing to do.
	if out, err := (Locker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil {
		t.Error("missing flake.nix")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "nix not found (needed to update flake.lock)" {
		t.Errorf("no nix: %v", err)
	}
}

func TestMaintain(t *testing.T) {
	stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"flake.nix": "{}", "flake.lock": "lock\n"})
	out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{"flake.lock": []byte("lock\n")})
	if err != nil || string(out["flake.lock"]) != "lock\n"+features+"flake update --flake path:<mirror>\n" {
		t.Errorf("Maintain = %q, %v", out["flake.lock"], err)
	}
	if out, err := (Locker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).Maintain(t.Context(), t.TempDir(), map[string][]byte{"flake.lock": nil}); err == nil {
		t.Error("missing flake.nix")
	}
	if _, _, err := mirrorDir(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("mirror of a missing dir")
	}
}
