package nuget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// stubDotnet installs a fake dotnet on PATH (only it: it uses shell builtins) that logs its
// arguments and the Directory.Packages.props it sees and appends its arguments to the
// project's packages.lock.json; with STUB_FAIL set it fails, with STUB_NOOP it changes nothing.
func stubDotnet(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
cwd=$(pwd)
dir="${2%/*}"
{
  echo "args: $*"
  [ -e Directory.Packages.props ] && while IFS= read -r l; do echo "  $l"; done < Directory.Packages.props
  [ -L "$dir/packages.lock.json" ] && echo "lock is a symlink"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "error NU1004: The packages lock file is inconsistent in $cwd"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "$*" >> "$dir/packages.lock.json"
`
	if err := os.WriteFile(filepath.Join(bin, "dotnet"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	return log
}

const lockRepo = "Directory.Packages.props"

func lockTree(t *testing.T) string {
	t.Helper()
	return write(t, map[string]string{
		".git/HEAD":                      "",
		lockRepo:                         "<Project>old</Project>",
		"src/App/App.csproj":             "<Project />",
		"src/App/packages.lock.json":     "app\n",
		"src/App/obj/project.assets":     "",
		"src/Lib/Lib.fsproj":             "<Project />",
		"src/Lib/packages.lock.json":     "lib\n",
		"src/Lib/bin/packages.lock.json": "build output\n",
		"tools/packages.lock.json":       "no project\n",
		"global.json":                    "{}",
	})
}

func TestLockerScope(t *testing.T) {
	root := lockTree(t)
	for _, tt := range []struct {
		file string
		ok   bool
	}{
		{lockRepo, true}, {"src/App/App.csproj", true}, {"build/Common.targets", true},
		{"src/App/obj/App.csproj.nuget.g.props", false}, {"global.json", false}, {".config/dotnet-tools.json", false},
	} {
		if dir, ok := (Locker{}).Scope(t.Context(), root, tt.file); ok != tt.ok || dir != "." {
			t.Errorf("Scope(%s) = %q, %v", tt.file, dir, ok)
		}
	}
	if _, ok := (Locker{}).Scope(t.Context(), write(t, map[string]string{"a/A.csproj": ""}), "a/A.csproj"); ok {
		t.Error("scope without a lockfile")
	}
	if got := strings.Join((Locker{}).ScopeSiblings(root), " "); got != "src/App/packages.lock.json src/Lib/packages.lock.json" {
		t.Errorf("ScopeSiblings = %s", got)
	}
	if (Locker{}).Siblings() != nil {
		t.Error("Siblings")
	}
	roots := (&nugetManager{}).LockRoots(root, []string{"src/Lib/Lib.fsproj", lockRepo, "src/App/App.csproj", "tools/x.props", "src/App/App.csproj"})
	want := []manager.LockRoot{{Manifest: "src/App/App.csproj", Lockfile: "src/App/packages.lock.json"}, {Manifest: "src/Lib/Lib.fsproj", Lockfile: "src/Lib/packages.lock.json"}}
	if len(roots) != len(want) || roots[0] != want[0] || roots[1] != want[1] {
		t.Errorf("LockRoots = %v", roots)
	}
}

func TestLockerProcess(t *testing.T) {
	log := stubDotnet(t)
	root := lockTree(t)
	files := map[string][]byte{"src/App/packages.lock.json": []byte("app\n"), "src/Lib/packages.lock.json": []byte("lib\n")}
	out, err := Locker{}.Process(t.Context(), root, map[string][]byte{lockRepo: []byte("<Project>new</Project>\n")}, files)
	if err != nil {
		t.Fatal(err)
	}
	flags := " --use-lock-file -p:RestoreLockedMode=false"
	if got := string(out["src/App/packages.lock.json"]); got != "app\nrestore src/App/App.csproj"+flags+"\n" {
		t.Errorf("App lock = %q", got)
	}
	if got := string(out["src/Lib/packages.lock.json"]); got != "lib\nrestore src/Lib/Lib.fsproj"+flags+"\n" {
		t.Errorf("Lib lock = %q", got)
	}
	want := "args: restore src/App/App.csproj" + flags + "\n  <Project>new</Project>\nargs: restore src/Lib/Lib.fsproj" + flags + "\n  <Project>new</Project>\n"
	if b, _ := os.ReadFile(log); string(b) != want {
		t.Errorf("log:\n%s\nwant:\n%s", b, want)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "src", "App", lockFile)); string(b) != "app\n" {
		t.Error("the working tree was modified")
	}

	os.Remove(log)
	out, err = Locker{}.Maintain(t.Context(), root, map[string][]byte{"src/App/packages.lock.json": []byte("app\n")})
	if err != nil || !strings.HasSuffix(string(out["src/App/packages.lock.json"]), flags+" --force-evaluate\n") || len(out) != 1 {
		t.Errorf("Maintain = %q, %v", out, err)
	}

	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).Process(t.Context(), root, nil, files); err != nil || len(out) != 0 {
		t.Errorf("unchanged = %v, %v", out, err)
	}
	if out, err := (Locker{}).Process(t.Context(), root, nil, nil); err != nil || out != nil {
		t.Errorf("no lockfiles = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	if _, err := (Locker{}).Process(t.Context(), root, nil, files); err == nil || err.Error() != "dotnet restore in "+root+": error NU1004: The packages lock file is inconsistent in "+root {
		t.Errorf("failure = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), root, nil, files); err == nil || err.Error() != "dotnet not found (needed to update packages.lock.json)" {
		t.Errorf("no dotnet = %v", err)
	}
}
