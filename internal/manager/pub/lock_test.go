package pub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestReadLocks reads the pubspec.lock next to immich's mobile app and the workspace root lock
// of localsend's app (`resolution: workspace`), both fetched 2026-11-06.
func TestReadLocks(t *testing.T) {
	m := New().(manager.LockReader)
	_, immich := extract(t, "immich/mobile/pubspec.yaml")
	_, localsend := extract(t, "localsend/app/pubspec.yaml")
	if errs := m.ReadLocks("testdata", [][]dep.Dep{immich, localsend}); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, tt := range []struct {
		ds                    []dep.Dep
		name, locked, version string
	}{
		{immich, "async", "2.13.1", "2.13.1"},
		{immich, "drift_sqlite_async", "0.3.1", "0.3.1"},
		{immich, "native_video_player", "", "cdf621bdb7edaf996e118a58a48f6441187d79c6"}, // git: not a hosted lock entry
		{localsend, "collection", "1.19.1", "1.17.2"},
		{localsend, "connectivity_plus", "7.3.1", "7.3.1"},
	} {
		if d := byName(tt.ds, tt.name); d.Locked != tt.locked || d.Version != tt.version || d.LockUnread {
			t.Errorf("%s: locked %q version %q (%v)", tt.name, d.Locked, d.Version, d.LockUnread)
		}
	}
	if d := byName(localsend, "meta"); d.Skip != skipFlutterPin {
		t.Errorf("meta in a Flutter package: %+v", d)
	}
	if d := byName(localsend, "shared_preferences_platform_interface"); d.Skip != skipUnversioned {
		t.Errorf("any: %+v", d)
	}
}

func TestReadLocksStates(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"a/pubspec.yaml": "dependencies:\n  x: <2.0.0\n  y: <2.0.0\n  z: ^1.0.0\n",
		"a/pubspec.lock": "packages:\n  x:\n    source: hosted\n    version: \"1.5.0\"\n  z:\n    source: git\n    version: \"1.2.0\"\nsdks:\n  dart: \">=3.0.0 <4.0.0\"\n",
		"b/pubspec.yaml": "dependencies:\n  x: ^1.0.0\n", "b/pubspec.lock": "packages: [\n",
		"c/pubspec.yaml": "dependencies:\n  x: ^1.0.0\n", "c/pubspec.lock": "packages: {}\n",
		"d/pubspec.yaml": "resolution: workspace\ndependencies:\n  x: <2.0.0\n",
	})
	var files [][]dep.Dep
	for _, f := range []string{"a/pubspec.yaml", "b/pubspec.yaml", "c/pubspec.yaml", "d/pubspec.yaml"} {
		ds, err := New().Extract(f, read(t, filepath.Join(root, f)), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, ds)
	}
	errs := New().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 2 || !strings.HasPrefix(errs[0].Error(), "b/pubspec.lock: ") || !strings.Contains(errs[1].Error(), "c/pubspec.lock: not a pubspec.lock") {
		t.Errorf("errs = %v", errs)
	}
	a := files[0]
	if a[0].Version != "1.5.0" || a[0].Locked != "1.5.0" || a[1].Skip != skipNoLock || a[2].Locked != "" {
		t.Errorf("a = %+v", a)
	}
	if !files[1][0].LockUnread || !files[2][0].LockUnread {
		t.Error("unreadable locks")
	}
	if files[3][0].Skip != skipNoLock {
		t.Errorf("workspace member without a workspace root: %+v", files[3][0])
	}
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"pubspec.yaml": "workspace:\n  - app\n  - packages/*\n", "pubspec.lock": "sdks: {}\n",
		"app/pubspec.yaml": "resolution: workspace\n", "packages/x/pubspec.yaml": "resolution: workspace\n",
		"tool/pubspec.yaml": "name: tool\n", "tool/pubspec.lock": "sdks: {}\n",
		"nolock/pubspec.yaml": "name: n\n",
	})
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"tool/pubspec.yaml", "app/pubspec.yaml", "packages/x/pubspec.yaml", "nolock/pubspec.yaml", "pubspec.yaml"})
	want := []manager.LockRoot{{Manifest: "pubspec.yaml", Lockfile: "pubspec.lock"}, {Manifest: "tool/pubspec.yaml", Lockfile: "tool/pubspec.lock"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("LockRoots = %+v", got)
	}
	if dir, ok := (Locker{}).Scope(t.Context(), root, "packages/x/pubspec.yaml"); !ok || dir != "." {
		t.Errorf("Scope = %q, %v", dir, ok)
	}
	if len(Locker{}.Siblings()) != 1 {
		t.Error("siblings")
	}
}

// stub installs fake dart and flutter SDKs on PATH (shell builtins only) whose dart logs its
// arguments, FLUTTER_ROOT, the pubspecs it sees and what of the mirror is visible, and appends
// its arguments to pubspec.lock; with STUB_FAIL set it fails, with STUB_NOOP it changes
// nothing.
func stub(t *testing.T) (log, flutterRoot string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin, flutterRoot := t.TempDir(), t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
{
  echo "${0##*/} $* FLUTTER_ROOT=$FLUTTER_ROOT"
  for f in pubspec.yaml */pubspec.yaml; do
    [ -f "$f" ] || continue
    [ -L "$f" ] && echo "$f is a symlink"
    while IFS= read -r l; do echo "  $f: $l"; done < "$f"
  done
  [ -L pubspec.lock ] && echo "pubspec.lock is a symlink"
  [ -e .dart_tool ] && echo ".dart_tool visible"
  [ -L app ] && echo "member linked"
  [ -e lib/main.dart ] && echo "lib visible"
  [ -e ../shared/pubspec.yaml ] && echo "sibling visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "Because x depends on y which doesn't match any versions, version solving failed. ($PWD)"
  exit 65
fi
[ -n "$STUB_NOOP" ] || echo "$*" >> pubspec.lock
`
	for _, p := range []string{filepath.Join(bin, "dart"), filepath.Join(flutterRoot, "bin", "cache", "dart-sdk", "bin", "dart"), filepath.Join(flutterRoot, "bin", "flutter")} {
		write(t, filepath.Dir(p), map[string]string{filepath.Base(p): script})
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(flutterRoot, "bin", "flutter"), filepath.Join(bin, "flutter")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	t.Setenv("FLUTTER_ROOT", "")
	return log, flutterRoot
}

const pubFlags = " --no-precompile --no-example"

func update(name, depType, datasource, rng, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, Name: name, DepType: depType, Datasource: datasource, Range: rng}, NewVersion: version}
}

func TestProcessUpdates(t *testing.T) {
	log, _ := stub(t)
	repo := t.TempDir()
	before := "name: app\nenvironment:\n  sdk: ^3.5.0\ndependencies:\n  http: ^1.2.0\n  path: ^1.8.0\n"
	write(t, repo, map[string]string{
		".git/HEAD": "", "shared/pubspec.yaml": "name: shared\n",
		"app/pubspec.yaml": before, "app/pubspec.lock": "lock\n", "app/lib/main.dart": "", "app/.dart_tool/package_config.json": "{}",
	})
	dir := filepath.Join(repo, "app")
	patched := "name: app\nenvironment:\n  sdk: ^3.5.0\ndependencies:\n  http: ^2.0.0\n  path: ^1.8.0\n"
	ups := map[string][]dep.Update{"pubspec.yaml": {update("http", DepTypeDependencies, "dart", "^1.2.0", "2.0.1"), update("dart", DepTypeEnvironment, "dart-version", "^3.5.0", "4.0.0")}}
	locks := map[string][]dep.Update{"pubspec.yaml": {update("path", DepTypeDependencies, "dart", "^1.8.0", "1.9.1")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"pubspec.yaml": []byte(patched)}, map[string][]byte{"pubspec.lock": []byte("lock\n")}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	args := "--suppress-analytics pub upgrade http path" + pubFlags
	if got := string(out["pubspec.lock"]); got != "lock\n"+args+"\n" {
		t.Errorf("pubspec.lock = %q", got)
	}
	want := "dart " + args + " FLUTTER_ROOT=\n" +
		"  pubspec.yaml: name: app\n  pubspec.yaml: environment:\n  pubspec.yaml:   sdk: ^3.5.0\n  pubspec.yaml: dependencies:\n  pubspec.yaml:   http: 2.0.1\n  pubspec.yaml:   path: 1.9.1\n" +
		"lib visible\nsibling visible\n"
	if got := string(read(t, log)); got != want {
		t.Errorf("log:\n%s\nwant:\n%s", got, want)
	}
	if string(read(t, filepath.Join(dir, "pubspec.yaml"))) != before || string(read(t, filepath.Join(dir, "pubspec.lock"))) != "lock\n" {
		t.Error("the working tree was modified")
	}
}

func TestProcessWorkspaceFlutter(t *testing.T) {
	log, flutterRoot := stub(t)
	dir := t.TempDir()
	root := "name: ws\nenvironment:\n  sdk: ^3.6.0\nworkspace:\n  - app\n"
	member := "name: app\nresolution: workspace\nenvironment:\n  sdk: ^3.6.0\ndependencies:\n  flutter:\n    sdk: flutter\n  http: ^1.2.0\n"
	write(t, dir, map[string]string{"pubspec.yaml": root, "pubspec.lock": "lock\n", "app/pubspec.yaml": member, "app/lib/main.dart": ""})
	// Only the SDK constraint changes: pub get, with the Flutter SDK's Dart.
	ups := map[string][]dep.Update{"app/pubspec.yaml": {update("dart", DepTypeEnvironment, "dart-version", "^3.6.0", "4.0.0")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, nil, map[string][]byte{"pubspec.lock": []byte("lock\n")}, ups, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out["pubspec.lock"]); got != "lock\n--suppress-analytics pub get"+pubFlags+"\n" {
		t.Errorf("pubspec.lock = %q", got)
	}
	got := string(read(t, log))
	if !strings.HasPrefix(got, "dart --suppress-analytics pub get"+pubFlags+" FLUTTER_ROOT="+flutterRoot+"\n") || strings.Contains(got, "symlink") || strings.Contains(got, "member linked") || !strings.Contains(got, "app/pubspec.yaml:   http: ^1.2.0") {
		t.Errorf("log:\n%s", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "app", ".dart_tool")); err == nil {
		t.Error("pub wrote into the checkout")
	}
}

func TestProcessOutcomes(t *testing.T) {
	log, _ := stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"pubspec.yaml": "name: a\n", "pubspec.lock": "lock\n"})
	files := map[string][]byte{"pubspec.lock": []byte("lock\n")}

	out, err := Locker{}.Process(t.Context(), dir, nil, files)
	if err != nil || string(out["pubspec.lock"]) != "lock\n--suppress-analytics pub get"+pubFlags+"\n" {
		t.Errorf("Process = %q, %v", out["pubspec.lock"], err)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	_, err = Locker{}.ProcessUpdates(t.Context(), dir, nil, files, map[string][]dep.Update{"pubspec.yaml": {update("x", DepTypeDependencies, "dart", "", "1.1.0")}}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "dart pub upgrade in "+dir+": Because x depends on y") || strings.Contains(err.Error(), os.TempDir()+"/deps-pub-") {
		t.Errorf("failure = %v", err)
	}
	if out, err := (Locker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, nil, nil, map[string][]dep.Update{"pubspec.yaml": {update("x", DepTypeDependencies, "dart", "^1.0.0", "1.1.0")}}); err == nil {
		t.Error("lockfile-only update without pubspec.lock")
	}
	if _, err := (Locker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil {
		t.Error("missing pubspec.yaml")
	}
	bad := map[string][]dep.Update{"pubspec.yaml": {update("x", DepTypeDependencies, "dart", "^1.0.0", "1.1.0")}}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, map[string][]byte{"pubspec.yaml": []byte("a: [")}, files, bad, nil); err == nil {
		t.Error("unparsable pubspec")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "dart not found (needed to update pubspec.lock)" {
		t.Errorf("no dart: %v", err)
	}
	if _, err := (Locker{}).Process(t.Context(), dir, nil, map[string][]byte{"pubspec.lock": []byte("sdks:\n  flutter: \">=3.0.0\"\n")}); err == nil || !strings.HasPrefix(err.Error(), "flutter not found") {
		t.Errorf("no flutter: %v", err)
	}
	if b := read(t, log); !strings.Contains(string(b), "pub upgrade x"+pubFlags) {
		t.Errorf("log:\n%s", b)
	}
}

func TestMaintain(t *testing.T) {
	stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"pubspec.yaml": "name: a\n", "pubspec.lock": "lock\n"})
	out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{"pubspec.lock": []byte("lock\n")})
	if err != nil || string(out["pubspec.lock"]) != "lock\n--suppress-analytics pub upgrade"+pubFlags+"\n" {
		t.Errorf("Maintain = %q, %v", out["pubspec.lock"], err)
	}
	if out, err := (Locker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).Maintain(t.Context(), t.TempDir(), map[string][]byte{"pubspec.lock": nil}); err == nil {
		t.Error("missing pubspec.yaml")
	}
	if _, _, err := mirrorDir(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("mirror of a missing dir")
	}
}
