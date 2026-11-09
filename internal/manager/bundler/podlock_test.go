package bundler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// podStub installs a fake `pod` on PATH (shell builtins only) that logs its arguments, the
// Podfile it sees and what of the mirror is visible, writes into the Xcode project as `pod
// install` integrates it, and appends its arguments to Podfile.lock; with STUB_FAIL set it
// fails, with STUB_NOOP it leaves Podfile.lock alone.
func podStub(t *testing.T) (log string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
{
  echo "pod $*"
  while IFS= read -r l; do echo "  Podfile: $l"; done < Podfile
  [ -L Podfile ] && echo "Podfile is a symlink"
  [ -L Podfile.lock ] && echo "Podfile.lock is a symlink"
  [ -L App.xcodeproj/project.pbxproj ] && echo "project is a symlink"
  [ -e Pods ] && echo "Pods visible"
  [ -e build ] && echo "build visible"
  [ -e scripts/setup.rb ] && echo "scripts visible"
  [ -e ../node_modules/react-native/package.json ] && echo "node_modules visible"
} >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "[!] CocoaPods could not find compatible versions for pod \"X\" ($PWD)"
  exit 1
fi
echo integrated >> App.xcodeproj/project.pbxproj
mkdir -p Pods && echo manifest > Pods/Manifest.lock
[ -n "$STUB_NOOP" ] || echo "$*" >> Podfile.lock
`
	if err := os.WriteFile(filepath.Join(bin, "pod"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	return log
}

// podRepo writes a React Native style checkout: ios/ holds the Podfile, its lock, the Xcode
// project, installed Pods and build output; node_modules is next to it.
func podRepo(t *testing.T) (repo, dir string) {
	t.Helper()
	repo = t.TempDir()
	for p, s := range map[string]string{
		".git/HEAD": "", "node_modules/react-native/package.json": "{}",
		"ios/Podfile": "pod 'Alamofire', '~> 5.8'\n", "ios/Podfile.lock": "PODS:\n  - Alamofire (5.8.1)\n",
		"ios/App.xcodeproj/project.pbxproj": "project\n", "ios/App.xcodeproj/xcshareddata/xcschemes/App.xcscheme": "scheme\n",
		"ios/Pods/Manifest.lock": "old\n", "ios/build/generated/x": "", "ios/scripts/setup.rb": "",
	} {
		p = filepath.Join(repo, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo, filepath.Join(repo, "ios")
}

// podFlags are the flags pod gets besides the command's.
func podFlags() string {
	if os.Geteuid() == 0 {
		return " --no-ansi --allow-root"
	}
	return " --no-ansi"
}

func TestPodLockerProcess(t *testing.T) {
	log := podStub(t)
	repo, dir := podRepo(t)
	lock := "PODS:\n  - Alamofire (5.8.1)\n"
	patched := "pod 'Alamofire', '~> 6.0'\n"
	l := PodLocker{}
	if d, ok := l.Scope(t.Context(), repo, "ios/Podfile"); !ok || d != "ios" {
		t.Errorf("Scope = %s, %v", d, ok)
	}
	if _, ok := l.Scope(t.Context(), repo, "other/Podfile"); ok {
		t.Error("Scope without Podfile.lock")
	}
	if s := l.Siblings(); len(s) != 1 || s[0] != podfileLock {
		t.Errorf("Siblings = %v", s)
	}
	out, err := l.Process(t.Context(), dir, map[string][]byte{"Podfile": []byte(patched)}, map[string][]byte{"Podfile.lock": []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out["Podfile.lock"]); got != lock+"install"+podFlags()+"\n" || len(out) != 1 {
		t.Errorf("out = %q", out)
	}
	want := "pod install" + podFlags() + "\n  Podfile: pod 'Alamofire', '~> 6.0'\nscripts visible\nnode_modules visible\n"
	if got := string(read(t, log)); got != want {
		t.Errorf("log:\n%s\nwant:\n%s", got, want)
	}
	for p, want := range map[string]string{
		"ios/Podfile": "pod 'Alamofire', '~> 5.8'\n", "ios/Podfile.lock": lock,
		"ios/App.xcodeproj/project.pbxproj": "project\n", "ios/Pods/Manifest.lock": "old\n",
	} {
		if got := string(read(t, filepath.Join(repo, p))); got != want {
			t.Errorf("%s was modified: %q", p, got)
		}
	}
}

func TestPodLockerLockedAndMaintain(t *testing.T) {
	log := podStub(t)
	_, dir := podRepo(t)
	files := map[string][]byte{"Podfile.lock": []byte("lock\n")}
	locks := map[string][]dep.Update{"Podfile": {
		{Dep: dep.Dep{Name: "Firebase/Core"}, NewVersion: "10.30.0", Lockfile: true},
		{Dep: dep.Dep{Name: "Alamofire"}, NewVersion: "5.9.0", Lockfile: true},
		{Dep: dep.Dep{Name: "Firebase/Analytics"}, NewVersion: "10.30.0", Lockfile: true},
	}}
	out, err := PodLocker{}.ProcessLocked(t.Context(), dir, nil, files, locks)
	if err != nil || string(out["Podfile.lock"]) != "lock\nupdate Alamofire Firebase"+podFlags()+"\n" {
		t.Errorf("ProcessLocked = %q, %v", out, err)
	}
	// The checked-out Podfile is used when the manifest is unchanged.
	if !strings.Contains(string(read(t, log)), "  Podfile: pod 'Alamofire', '~> 5.8'\n") {
		t.Errorf("log:\n%s", read(t, log))
	}
	out, err = PodLocker{}.Maintain(t.Context(), dir, files)
	if err != nil || string(out["Podfile.lock"]) != "lock\nupdate"+podFlags()+"\n" {
		t.Errorf("Maintain = %q, %v", out, err)
	}
	if out, err := (PodLocker{}).Maintain(t.Context(), dir, nil); out != nil || err != nil {
		t.Errorf("Maintain without lock = %v, %v", out, err)
	}
}

func TestPodLockerOutcomes(t *testing.T) {
	podStub(t)
	_, dir := podRepo(t)
	files := map[string][]byte{"Podfile.lock": []byte("lock\n")}
	t.Setenv("STUB_NOOP", "1")
	if out, err := (PodLocker{}).Process(t.Context(), dir, nil, files); out != nil || err != nil {
		t.Errorf("unchanged lock = %v, %v", out, err)
	}
	t.Setenv("STUB_FAIL", "1")
	_, err := PodLocker{}.Process(t.Context(), dir, nil, files)
	if err == nil || !strings.HasPrefix(err.Error(), "pod install in "+dir+": [!] CocoaPods could not find compatible versions") || strings.Contains(err.Error(), "deps-cocoapods-") {
		t.Errorf("failure = %v", err)
	}
	if out, err := (PodLocker{}).Process(t.Context(), dir, nil, nil); out != nil || err != nil {
		t.Errorf("no lock = %v, %v", out, err)
	}
	if _, err := (PodLocker{}).ProcessLocked(t.Context(), dir, nil, nil, map[string][]dep.Update{"Podfile": {{Dep: dep.Dep{Name: "A"}}}}); err == nil {
		t.Error("lockfile-only update without Podfile.lock")
	}
	if _, err := (PodLocker{}).Process(t.Context(), t.TempDir(), nil, files); err == nil {
		t.Error("missing Podfile")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (PodLocker{}).Process(t.Context(), dir, nil, files); err == nil || err.Error() != "pod not found (needed to update Podfile.lock)" {
		t.Errorf("no pod: %v", err)
	}
}
