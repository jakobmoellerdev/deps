package bundler

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// lockOf is a Gemfile.lock locking specs ("name (version)") from rubygems.org.
func lockOf(specs ...string) string {
	var b strings.Builder
	b.WriteString("GEM\n  remote: https://rubygems.org/\n  specs:\n")
	for _, s := range specs {
		b.WriteString("    " + s + "\n")
	}
	b.WriteString("\nPLATFORMS\n  ruby\n\nDEPENDENCIES\n  x\n\nBUNDLED WITH\n  4.0.23\n")
	return b.String()
}

func TestLockedGems(t *testing.T) {
	gems, err := lockedGems(read(t, "testdata/openclaw/Gemfile.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(gems) != 104 || gems["fastlane"] != "2.241.2" || gems["CFPropertyList"] != "3.0.8" || gems["domain_name"] != "0.7.20261007" {
		t.Errorf("gems = %d %v", len(gems), gems)
	}
	if _, ok := gems["bundler"]; ok {
		t.Error("the CHECKSUMS entry of bundler is not a locked gem")
	}
	lock := "GIT\n  remote: https://github.com/rails/rails.git\n  revision: abc\n  specs:\n    rails (8.1.0.alpha)\n\n" +
		"PATH\n  remote: engines/x\n  specs:\n    x (0.1.0)\n\n" +
		"GEM\n  remote: https://rubygems.org/\n  specs:\n    nokogiri (1.18.10-x86_64-linux-gnu)\n      racc (~> 1.4)\n    nokogiri (1.18.10-arm64-darwin)\n    racc (1.8.1)\r\n\n" +
		"GEM\n  remote: https://gems.contribsys.com/\n  specs:\n    sidekiq-pro (7.3.2)\n\nDEPENDENCIES\n  rails!\n"
	gems, err = lockedGems([]byte(lock))
	if err != nil || !maps.Equal(gems, map[string]string{"nokogiri": "1.18.10", "racc": "1.8.1", "sidekiq-pro": "7.3.2"}) {
		t.Errorf("gems = %v, %v", gems, err)
	}
	if _, err := lockedGems([]byte("{\"not\": \"a lock\"}\n")); err == nil {
		t.Error("no error for a non-lockfile")
	}
}

func TestReadLocks(t *testing.T) {
	root := t.TempDir()
	gemfile := "source \"https://rubygems.org\"\ngem \"fastlane\", \"2.241.2\"\ngem \"rake\"\ngem \"rack\", \"~> 3.0\"\ngem \"unlocked\"\ngem \"git\", git: \"https://x.example/g.git\"\n"
	write(t, root, map[string]string{
		"a/Gemfile": gemfile, "a/Gemfile.lock": lockOf("fastlane (2.241.2)", "rake (13.0.0)", "rack (3.1.8)"),
		"b/Gemfile": gemfile, "b/Gemfile.lock": "garbage\n",
		"c/Gemfile": gemfile,
	})
	m := New()
	var files [][]dep.Dep
	for _, f := range []string{"a/Gemfile", "b/Gemfile", "c/Gemfile"} {
		deps, err := m.Extract(f, read(t, filepath.Join(root, f)), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, deps)
	}
	errs := m.(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "b/Gemfile.lock: not a Gemfile.lock") {
		t.Errorf("errs = %v", errs)
	}
	describeLock := func(d dep.Dep) string {
		s := d.Name + " version=" + d.Version + " locked=" + d.Locked
		if d.LockUnread {
			s += " unread"
		}
		if d.Skip != "" {
			s += " skip=" + d.Skip
		}
		return s
	}
	want := [][]string{
		{"fastlane version=2.241.2 locked=2.241.2", "rake version=13.0.0 locked=13.0.0", "rack version=3.0 locked=3.1.8", "unlocked version= locked= skip=" + skipNoLock, "git version= locked= skip=git dependency"},
		{"fastlane version=2.241.2 locked= unread", "rake version= locked= unread skip=" + skipNoLock, "rack version=3.0 locked= unread", "unlocked version= locked= unread skip=" + skipNoLock, "git version= locked= skip=git dependency"},
		{"fastlane version=2.241.2 locked=", "rake version= locked= skip=" + skipNoLock, "rack version=3.0 locked=", "unlocked version= locked= skip=" + skipNoLock, "git version= locked= skip=git dependency"},
	}
	for i, deps := range files {
		var got []string
		for _, d := range deps {
			got = append(got, describeLock(d))
		}
		if !slices.Equal(got, want[i]) {
			t.Errorf("file %d:\n%s\nwant:\n%s", i, strings.Join(got, "\n"), strings.Join(want[i], "\n"))
		}
	}
	roots := m.(manager.LockMaintainer).LockRoots(root, []string{"c/Gemfile", "b/Gemfile", "a/Gemfile"})
	if want := []manager.LockRoot{{Manifest: "a/Gemfile", Lockfile: "a/Gemfile.lock"}, {Manifest: "b/Gemfile", Lockfile: "b/Gemfile.lock"}}; !slices.Equal(roots, want) {
		t.Errorf("LockRoots = %v", roots)
	}
}

func TestPin(t *testing.T) {
	src := "gem \"exact\", \"2.0.0\"\ngem 'tilde', '~> 8.1' # x\ngem \"bare\", require: false\ngem \"list\", \">= 1\", \"< 3\"\ngem \"arr\", [\">= 1\", \"< 3\"]\ngem \"git\", git: \"g\"\ngem \"other\", \"1.0\"\n"
	out, err := pin([]byte(src), map[string]string{"exact": "2.0.0", "tilde": "8.1.3", "bare": "1.2.0", "list": "2.5.0", "arr": "2.5.0", "git": "1.0.0"})
	want := "gem \"exact\", \"2.0.0\"\ngem 'tilde', \"= 8.1.3\" # x\ngem \"bare\", \"= 1.2.0\", require: false\ngem \"list\", \"= 2.5.0\"\ngem \"arr\", \"= 2.5.0\"\ngem \"git\", git: \"g\"\ngem \"other\", \"1.0\"\n"
	if err != nil || string(out) != want {
		t.Errorf("pin = %q, %v\nwant  %q", out, err, want)
	}
	if out, _ := pin([]byte(src), map[string]string{"exact": "2.0.0"}); string(out) != src {
		t.Error("an exact requirement of the version was rewritten")
	}
}

// stub installs a fake bundle on PATH (only it: it uses shell builtins) that logs its
// arguments and the Gemfile it sees, what of the mirror is visible, and appends its arguments
// to Gemfile.lock as a comment; with STUB_FAIL set it fails, with STUB_NOOP it changes nothing.
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
  [ "$BUNDLE_GEMFILE" = "$PWD/Gemfile" ] || echo "BUNDLE_GEMFILE=$BUNDLE_GEMFILE PWD=$PWD"
  while IFS= read -r l; do echo "  $l"; done < "$BUNDLE_GEMFILE"
  [ -L "$PWD/Gemfile" ] && echo "Gemfile is a symlink"
  [ -L "$PWD/Gemfile.lock" ] && echo "Gemfile.lock is a symlink"
  [ -L "$PWD/.bundle" ] && echo ".bundle linked"
  [ -e "$PWD/../shared/shared.gemspec" ] && echo "sibling visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "Could not find gem 'nope' in $PWD"
  exit 1
fi
[ -n "$STUB_NOOP" ] || echo "# $*" >> "$PWD/Gemfile.lock"
`
	if err := os.WriteFile(filepath.Join(bin, "bundle"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	t.Setenv("BUNDLE_GEMFILE", "/elsewhere/Gemfile") // overridden for the mirror
	return log
}

func update(name, locked, version string) dep.Update {
	return dep.Update{Dep: dep.Dep{Manager: Name, File: "Gemfile", Name: name, Locked: locked}, NewVersion: version}
}

// appDir returns a directory "app" in a new repository, so the mirror stops at its top.
func appDir(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, map[string]string{".git/HEAD": "", "app/.keep": ""})
	return filepath.Join(repo, "app")
}

func TestProcessUpdates(t *testing.T) {
	log := stub(t)
	repo := t.TempDir()
	before := "source \"https://rubygems.org\"\ngem \"fastlane\", \"2.241.2\"\ngem \"rake\"\ngem \"rails\", \"~> 8.0\"\ngem \"new\", \"1.0\"\n"
	lock := lockOf("fastlane (2.241.2)", "rake (13.0.0)", "rails (8.0.2)")
	write(t, repo, map[string]string{
		".git/HEAD": "", "shared/shared.gemspec": "",
		"app/Gemfile": before, "app/Gemfile.lock": lock, "app/.bundle/config": "---\n", "app/app.gemspec": "",
	})
	dir := filepath.Join(repo, "app")
	patched := "source \"https://rubygems.org\"\ngem \"fastlane\", \"2.242.0\"\ngem \"rake\"\ngem \"rails\", \"~> 8.1\"\ngem \"new\", \"1.1\"\n"
	ups := map[string][]dep.Update{"Gemfile": {update("fastlane", "2.241.2", "2.242.0"), update("rails", "8.0.2", "8.1.1"), update("new", "", "1.1")}}
	locks := map[string][]dep.Update{"Gemfile": {update("rake", "13.0.0", "13.4.2")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"Gemfile": []byte(patched)}, map[string][]byte{"Gemfile.lock": []byte(lock)}, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out["Gemfile.lock"]), lock+"# lock --update fastlane rails rake --conservative\n# lock\n"; got != want {
		t.Errorf("Gemfile.lock:\n%s\nwant:\n%s", got, want)
	}
	// Run 1 pins rails and rake (fastlane's exact requirement already is its version; new is
	// not locked yet), run 2 restores the patched Gemfile.
	wantLog := "args: lock --update fastlane rails rake --conservative\n" +
		"  source \"https://rubygems.org\"\n  gem \"fastlane\", \"2.242.0\"\n  gem \"rake\", \"= 13.4.2\"\n  gem \"rails\", \"= 8.1.1\"\n  gem \"new\", \"1.1\"\n" +
		".bundle linked\nsibling visible\n" +
		"args: lock\n" +
		"  source \"https://rubygems.org\"\n  gem \"fastlane\", \"2.242.0\"\n  gem \"rake\"\n  gem \"rails\", \"~> 8.1\"\n  gem \"new\", \"1.1\"\n" +
		".bundle linked\nsibling visible\n"
	if b := read(t, log); string(b) != wantLog {
		t.Errorf("log:\n%s\nwant:\n%s", b, wantLog)
	}
	if b := read(t, filepath.Join(dir, "Gemfile")); string(b) != before {
		t.Error("the working tree was modified")
	}
	if b := read(t, filepath.Join(dir, "Gemfile.lock")); string(b) != lock {
		t.Error("the working tree's lock was modified")
	}
}

func TestProcessExactOnly(t *testing.T) {
	log := stub(t)
	dir := appDir(t)
	src := read(t, "testdata/openclaw/Gemfile")
	lock := read(t, "testdata/openclaw/Gemfile.lock")
	write(t, dir, map[string]string{"Gemfile": string(src), "Gemfile.lock": string(lock)})
	patched := strings.Replace(string(src), "2.241.2", "2.242.0", 1)
	ups := map[string][]dep.Update{"Gemfile": {update("fastlane", "2.241.2", "2.242.0")}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{"Gemfile": []byte(patched)}, map[string][]byte{"Gemfile.lock": lock}, ups, nil)
	if err != nil || !strings.HasSuffix(string(out["Gemfile.lock"]), "  4.0.23\n# lock --update fastlane --conservative\n") {
		t.Errorf("Gemfile.lock = %q, %v", out["Gemfile.lock"], err)
	}
	if b := read(t, log); strings.Count(string(b), "args: ") != 1 || strings.Contains(string(b), "symlink") {
		t.Errorf("log:\n%s", b)
	}
	// Process (no updates passed) re-resolves once.
	if out, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{"Gemfile": []byte(patched)}, map[string][]byte{"Gemfile.lock": lock}); err != nil || !strings.HasSuffix(string(out["Gemfile.lock"]), "# lock\n") {
		t.Errorf("Process = %q, %v", out["Gemfile.lock"], err)
	}
}

func TestProcessOutcomes(t *testing.T) {
	stub(t)
	dir := appDir(t)
	write(t, dir, map[string]string{"Gemfile": "gem \"x\", \"1.0\"\n"})
	manifests := map[string][]byte{"Gemfile": []byte("gem \"x\", \"2.0\"\n")}
	lock := map[string][]byte{"Gemfile.lock": []byte(lockOf("x (1.0)"))}
	ups := map[string][]dep.Update{"Gemfile": {update("x", "1.0", "2.0")}}

	// Without a lockfile there is nothing to do, unless lockfile-only updates need one.
	if out, err := (Locker{}).ProcessUpdates(t.Context(), dir, manifests, nil, ups, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, manifests, nil, nil, ups); err == nil || !strings.Contains(err.Error(), "lockfile-only updates need Gemfile.lock") {
		t.Errorf("lockfile-only without a lock: %v", err)
	}
	// An unchanged lock is not returned.
	t.Setenv("STUB_NOOP", "1")
	if out, err := (Locker{}).ProcessUpdates(t.Context(), dir, manifests, lock, ups, nil); out != nil || err != nil {
		t.Errorf("unchanged = %v, %v", out, err)
	}
	// A failure carries Bundler's output, with the mirror's path shown as the directory.
	t.Setenv("STUB_FAIL", "1")
	_, err := Locker{}.ProcessUpdates(t.Context(), dir, manifests, lock, ups, nil)
	if want := "bundle lock in " + dir + ": Could not find gem 'nope' in " + dir; err == nil || err.Error() != want {
		t.Errorf("failure: %v\nwant %s", err, want)
	}
	// An unreadable lock.
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, manifests, map[string][]byte{"Gemfile.lock": []byte("x\n")}, ups, nil); err == nil || !strings.Contains(err.Error(), "not a Gemfile.lock") {
		t.Errorf("bad lock: %v", err)
	}
	// Without bundle.
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).ProcessUpdates(t.Context(), dir, manifests, lock, ups, nil); err == nil || err.Error() != "bundle not found (needed to update Gemfile.lock)" {
		t.Errorf("missing bundle: %v", err)
	}
}

func TestMaintain(t *testing.T) {
	log := stub(t)
	dir := appDir(t)
	lock := lockOf("x (1.0)")
	write(t, dir, map[string]string{"Gemfile": "gem \"x\"\n", "Gemfile.lock": lock})
	out, err := Locker{}.Maintain(context.Background(), dir, map[string][]byte{"Gemfile.lock": []byte(lock)})
	if err != nil || string(out["Gemfile.lock"]) != lock+"# lock --update\n" {
		t.Errorf("Maintain = %q, %v", out["Gemfile.lock"], err)
	}
	if b := read(t, log); !strings.HasPrefix(string(b), "args: lock --update\n  gem \"x\"\n") {
		t.Errorf("log:\n%s", b)
	}
	if out, err := (Locker{}).Maintain(context.Background(), dir, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if scope, ok := (Locker{}).Scope(context.Background(), filepath.Dir(dir), filepath.Base(dir)+"/Gemfile"); !ok || scope != filepath.Base(dir) {
		t.Errorf("Scope = %s, %v", scope, ok)
	}
	if _, ok := (Locker{}).Scope(context.Background(), dir, "sub/Gemfile"); ok {
		t.Error("Scope without a lock")
	}
}
