package composer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadLocks(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"composer.json":        string(read(t, "testdata/bookstack/composer.json")),
		"composer.lock":        string(read(t, "testdata/bookstack/composer.lock")),
		"broken/composer.lock": "{",
		"nolock/composer.json": "",
		"paths/composer.lock": `{"packages": [{"name": "acme/local", "version": "dev-main", "dist": {"type": "path"}},
			{"name": "acme/branch", "version": "dev-main"}, {"name": "acme/any", "version": "v2.1.0"}], "packages-dev": []}`,
		"empty/composer.lock": `{}`,
	})
	deps := extract(t, "composer.json", read(t, "testdata/bookstack/composer.json"))
	broken := []dep.Dep{{Name: "a/b", File: "broken/composer.json", Range: "^1.0", Version: "1.0"}}
	nolock := []dep.Dep{{Name: "a/b", File: "nolock/composer.json", Range: "*"}}
	paths := []dep.Dep{
		{Name: "acme/local", File: "paths/composer.json", Range: "*"},
		{Name: "acme/branch", File: "paths/composer.json", Range: "*"},
		{Name: "ACME/any", File: "paths/composer.json", Range: "*"},
		{Name: "acme/skipped", File: "paths/composer.json", Skip: skipPlatform},
	}
	empty := []dep.Dep{{Name: "a/b", File: "empty/composer.json", Range: "^1.0", Version: "1.0"}}
	errs := New().(manager.LockReader).ReadLocks(root, [][]dep.Dep{deps, broken, nolock, paths, empty})
	if len(errs) != 2 || !strings.HasPrefix(errs[0].Error(), "broken/composer.lock: ") || !strings.Contains(errs[1].Error(), "not a composer.lock") {
		t.Fatalf("errs = %v", errs)
	}
	for name, want := range map[string]string{"laravel/framework": "v13.35.0", "phpunit/phpunit": "12.5.38", "guzzlehttp/guzzle": "7.15.5"} {
		if d := find(deps, name); d.Locked != want {
			t.Errorf("%s locked %q, want %s", name, d.Locked, want)
		}
	}
	if !broken[0].LockUnread || !empty[0].LockUnread {
		t.Error("unreadable locks")
	}
	if nolock[0].Skip != skipNoLock {
		t.Errorf("nolock = %+v", nolock[0])
	}
	if paths[0].Skip != skipPath || paths[1].Skip != skipNoLock || paths[2].Version != "v2.1.0" || paths[2].Locked != "v2.1.0" {
		t.Errorf("paths = %+v", paths)
	}
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"composer.json": "{}", "composer.lock": "{}", "b/composer.json": "{}", "b/composer.lock": "{}", "c/composer.json": "{}"})
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"b/composer.json", "composer.json", "c/composer.json"})
	want := []manager.LockRoot{{Manifest: "b/composer.json", Lockfile: "b/composer.lock"}, {Manifest: "composer.json", Lockfile: "composer.lock"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("LockRoots = %+v", got)
	}
	if (Locker{}).Siblings()[0] != lockFile {
		t.Error("siblings")
	}
}

// stub installs a fake composer on PATH (only it: it uses shell builtins) that logs its
// arguments, the COMPOSER file and what of the mirror is visible, and appends its arguments
// to composer.lock; with STUB_FAIL set it fails, with STUB_NOOP it changes nothing. It
// answers --version with STUB_VERSION (2.8.4 by default).
func stub(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "Composer version ${STUB_VERSION:-2.8.4} 2024-12-11 11:57:23"
  exit 0
fi
{
  echo "args: $*"
  [ "$COMPOSER" = "$PWD/composer.json" ] || echo "COMPOSER=$COMPOSER PWD=$PWD"
  while IFS= read -r l; do echo "  $l"; done < "$COMPOSER"
  [ -L "$PWD/composer.json" ] && echo "composer.json is a symlink"
  [ -L "$PWD/composer.lock" ] && echo "composer.lock is a symlink"
  [ -e "$PWD/vendor" ] && echo "vendor visible"
  [ -L "$PWD/auth.json" ] && echo "auth.json linked"
  [ -e "$PWD/../packages/local/composer.json" ] && echo "sibling visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "Your requirements could not be resolved in $PWD"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "$*" >> "$PWD/composer.lock"
`
	if err := os.WriteFile(filepath.Join(bin, "composer"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	t.Setenv("STUB_VERSION", "")
	t.Setenv("COMPOSER", "/elsewhere/composer.json") // overridden for the mirror
	return log
}

func update(name, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, File: "app/composer.json", Name: name}, NewVersion: version}
}

const lockFlags = " --no-install --no-scripts --no-plugins --no-audit --no-interaction --no-ansi"

func TestProcessUpdates(t *testing.T) {
	log := stub(t)
	repo := t.TempDir()
	before := `{"require": {"monolog/monolog": "^2.9", "psr/log": "^1.1"}}`
	write(t, repo, map[string]string{
		".git/HEAD": "", "packages/local/composer.json": "{}",
		"app/composer.json": before, "app/composer.lock": "lock\n", "app/auth.json": "{}", "app/vendor/autoload.php": "",
	})
	dir := filepath.Join(repo, "app")
	patched := "{\"require\": {\"monolog/monolog\": \"^3.0\", \"psr/log\": \"^1.1\"}}\n"
	ups := map[string][]dep.Update{"composer.json": {update("Monolog/Monolog", "3.8.1")}}
	locks := map[string][]dep.Update{"composer.json": {update("psr/log", "1.1.4"), update("psr/log", "1.1.4")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"composer.json": []byte(patched)}, map[string][]byte{"composer.lock": []byte("lock\n")}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	args := "update monolog/monolog:3.8.1 psr/log:1.1.4 --with-dependencies --minimal-changes" + lockFlags + " --ignore-platform-reqs"
	if got := string(out["composer.lock"]); got != "lock\n"+args+"\n" {
		t.Errorf("composer.lock = %q", got)
	}
	if want := "args: " + args + "\n  " + patched + "auth.json linked\nsibling visible\n"; string(read(t, log)) != want {
		t.Errorf("log:\n%s\nwant:\n%s", read(t, log), want)
	}
	if string(read(t, filepath.Join(dir, "composer.json"))) != before || string(read(t, filepath.Join(dir, "composer.lock"))) != "lock\n" {
		t.Error("the working tree was modified")
	}
}

func TestProcessOutcomes(t *testing.T) {
	log := stub(t)
	dir := t.TempDir()
	platform := `{"config": {"platform": {"php": "8.3.0"}}, "require": {"a/b": "^1.0"}}`
	write(t, dir, map[string]string{"composer.json": platform, "composer.lock": "lock\n"})
	files := map[string][]byte{"composer.lock": []byte("lock\n")}

	// No updates: the content-hash is refreshed; the checked-out composer.json is used.
	out, err := Locker{}.Process(t.Context(), dir, nil, files)
	if err != nil || string(out["composer.lock"]) != "lock\nupdate --lock"+lockFlags+" --ignore-platform-req=ext-* --ignore-platform-req=lib-*\n" {
		t.Errorf("Process = %q, %v", out["composer.lock"], err)
	}
	// composer.ignorePlatformReqs replaces the default, config.platform.php or not; [] ignores
	// nothing.
	l := Locker{IgnorePlatformReqs: []string{"ext-intl", "php"}}
	if out, err := l.Process(t.Context(), dir, nil, files); err != nil || string(out["composer.lock"]) != "lock\nupdate --lock"+lockFlags+" --ignore-platform-req=ext-intl --ignore-platform-req=php\n" {
		t.Errorf("configured reqs = %q, %v", out["composer.lock"], err)
	}
	if out, err := (Locker{IgnorePlatformReqs: []string{}}).Process(t.Context(), dir, nil, files); err != nil || string(out["composer.lock"]) != "lock\nupdate --lock"+lockFlags+"\n" {
		t.Errorf("no reqs ignored = %q, %v", out["composer.lock"], err)
	}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).Process(t.Context(), dir, nil, files); err != nil || out != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	_, err = Locker{}.ProcessUpdates(t.Context(), dir, nil, files, map[string][]dep.Update{"composer.json": {update("a/b", "1.1.0")}}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "composer update in "+dir+": Your requirements could not be resolved in "+dir) {
		t.Errorf("failure = %v", err)
	}
	// Without composer.lock: nothing to do, unless lockfile-only updates need it.
	if out, err := (Locker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, nil, nil, nil, map[string][]dep.Update{"composer.json": {update("a/b", "1.1.0")}}); err == nil {
		t.Error("lockfile-only update without composer.lock")
	}
	if _, err := (Locker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil {
		t.Error("missing composer.json")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "composer not found (needed to update composer.lock)" {
		t.Errorf("no composer: %v", err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "args: update a/b:1.1.0 --with-dependencies") {
		t.Errorf("log:\n%s", b)
	}
}

func TestProcessUpdatesComposerVersion(t *testing.T) {
	tests := []struct {
		version, want, err string
	}{
		{version: "2.7.0", want: "update a/b:1.1.0 --with-dependencies --minimal-changes"},
		{version: "2.6.6", want: "update a/b:1.1.0 --with-dependencies" + lockFlags},
		{version: "unknown", want: "update a/b:1.1.0 --with-dependencies --minimal-changes"},
		{version: "1.10.27", err: "composer 1.10 cannot update composer.lock: deps needs Composer 2.7 or later (2.0 to 2.6 without --minimal-changes)"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			stub(t) // a new binary path per case: versions are cached by path
			t.Setenv("STUB_VERSION", tt.version)
			dir := t.TempDir()
			write(t, dir, map[string]string{"composer.json": "{}"})
			out, err := Locker{}.ProcessUpdates(t.Context(), dir, nil, map[string][]byte{"composer.lock": []byte("lock\n")}, map[string][]dep.Update{"composer.json": {update("a/b", "1.1.0")}}, nil)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err = %v, want %s", err, tt.err)
				}
				return
			}
			if err != nil || !strings.HasPrefix(string(out["composer.lock"]), "lock\n"+tt.want) {
				t.Errorf("ProcessUpdates = %q, %v", out["composer.lock"], err)
			}
		})
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := updateFlags(t.Context()); err == nil || err.Error() != "composer not found (needed to update composer.lock)" {
		t.Errorf("no composer: %v", err)
	}
}

func TestMaintain(t *testing.T) {
	stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{"composer.json": "{}", "composer.lock": "lock\n"})
	out, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{"composer.lock": []byte("lock\n")})
	if err != nil || string(out["composer.lock"]) != "lock\nupdate"+lockFlags+" --ignore-platform-reqs\n" {
		t.Errorf("Maintain = %q, %v", out["composer.lock"], err)
	}
	if out, err := (Locker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	out, err = Locker{IgnorePlatformReqs: []string{"ext-*", "lib-*"}}.Maintain(t.Context(), dir, map[string][]byte{"composer.lock": []byte("lock\n")})
	if err != nil || string(out["composer.lock"]) != "lock\nupdate"+lockFlags+" --ignore-platform-req=ext-* --ignore-platform-req=lib-*\n" {
		t.Errorf("Maintain with composer.ignorePlatformReqs = %q, %v", out["composer.lock"], err)
	}
	if _, err := (Locker{}).Maintain(t.Context(), t.TempDir(), map[string][]byte{"composer.lock": nil}); err == nil {
		t.Error("missing composer.json")
	}
	if _, _, err := mirrorDir(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("mirror of a missing dir")
	}
}

func TestReadLocksUnnamedVCS(t *testing.T) {
	root := t.TempDir()
	manifest := `{"repositories": [{"packagist.org": false}, {"type": "vcs", "url": "https://gitlab.example.com/platform/php-sdk.git"}],
	"require": {"acme/sdk": "^2.0", "acme/missing": "^1.0", "acme/none": "^1.0"}}`
	write(t, root, map[string]string{
		"app/composer.json": manifest,
		"app/composer.lock": `{"packages": [
			{"name": "acme/sdk", "version": "2.3.1", "source": {"type": "git", "url": "git@gitlab.example.com:platform/php-sdk.git", "reference": "abc"}},
			{"name": "acme/missing", "version": "1.0.0", "source": {"type": "git", "url": "https://github.com/acme/missing.git"}}]}`,
	})
	deps := extract(t, "app/composer.json", []byte(manifest))
	if d := find(deps, "acme/sdk"); d.Skip != skipNoRepository {
		t.Fatalf("before ReadLocks: %+v", d)
	}
	if errs := New().(manager.LockReader).ReadLocks(root, [][]dep.Dep{deps}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if d := find(deps, "acme/sdk"); d.Datasource != GitDatasource || d.Source != "https://gitlab.example.com/platform/php-sdk" || d.Skip != "" || d.Locked != "2.3.1" {
		t.Errorf("acme/sdk = %+v", d)
	}
	for _, name := range []string{"acme/missing", "acme/none"} {
		if d := find(deps, name); d.Datasource != Datasource || d.Skip != skipNoRepository {
			t.Errorf("%s = %+v", name, d)
		}
	}
	// An unreadable composer.json maps nothing.
	if r := manifestRepositories(filepath.Join(root, "nope.json")); len(r.unnamed) != 0 {
		t.Errorf("repos = %+v", r)
	}
}
