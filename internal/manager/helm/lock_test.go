package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata/aliased chart and its Chart.lock were written by `helm dependency update`
// (Helm v4.1.3): a repository alias, condition, tags, import-values, a dependency alias and
// a version range. lockedDigest is the digest `helm dependency build` accepted after deps
// bumped redis to 17.17.1.
const lockedDigest = "sha256:188e8f5f50d7a87adcc409fded959f622069bd9954ac07e31733ac5e81d24c9c"

func chartDir(t *testing.T, chart, lock string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, chartFile), []byte(chart), 0o644); err != nil {
		t.Fatal(err)
	}
	if lock != "" {
		if err := os.WriteFile(filepath.Join(dir, lockFile), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "aliased", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLockerHelmDigest(t *testing.T) {
	chart, lock := readTestdata(t, chartFile), readTestdata(t, lockFile)
	dir := chartDir(t, chart, lock)
	patched := strings.Replace(chart, "version: 17.11.3", "version: 17.17.1", 1)
	out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{chartFile: []byte(patched)}, map[string][]byte{lockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := "sha256:fb043b317cc540aded9e8cc0528c80ebea7077268aa8ca4405a0d15fc6611c82"
	want := strings.Replace(strings.Replace(lock, "version: 17.11.3", "version: 17.17.1", 1), oldDigest, lockedDigest, 1)
	if got := string(out[lockFile]); got != want {
		t.Fatalf("Chart.lock =\n%s\nwant\n%s", got, want)
	}
}

func TestLockerVersions(t *testing.T) {
	const chart = `apiVersion: v2
name: app
version: 0.1.0
dependencies:
  - name: a
    version: v1.0.0
    repository: https://example.com/charts
  - name: b
    version: 2.0.0
    repository: oci://example.com/charts
  - name: c
    version: ~3.0.0
    repository: https://example.com/charts
`
	reqs, err := chartRequirements([]byte(chart))
	if err != nil {
		t.Fatal(err)
	}
	locked := []*chartDependency{
		{Name: "a", Repository: "https://example.com/charts", Version: "1.0.0"}, // index without "v"
		{Name: "b", Repository: "oci://example.com/charts", Version: "2.0.0"},
		{Name: "c", Repository: "https://example.com/charts", Version: "3.0.4"},
	}
	digest, err := hashReq(reqs, locked)
	if err != nil {
		t.Fatal(err)
	}
	lock := "dependencies:\n- name: a\n  repository: https://example.com/charts\n  version: 1.0.0\n" +
		"- name: b\n  repository: oci://example.com/charts\n  version: \"2.0.0\"\n" +
		"- name: c\n  repository: https://example.com/charts\n  version: 3.0.4\n" +
		"digest: " + digest + "\ngenerated: \"2026-01-01T00:00:00Z\"\n"
	dir := chartDir(t, chart, lock)
	patched := strings.NewReplacer("version: v1.0.0", "version: v1.1.0", "version: 2.0.0", "version: 2.1.0", "~3.0.0", "~3.1.0").Replace(chart)
	out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{chartFile: []byte(patched)}, map[string][]byte{lockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	newReqs, _ := chartRequirements([]byte(patched))
	newLocked := []*chartDependency{
		{Name: "a", Repository: "https://example.com/charts", Version: "1.1.0"},
		{Name: "b", Repository: "oci://example.com/charts", Version: "2.1.0"},
		{Name: "c", Repository: "https://example.com/charts", Version: "3.0.4"}, // ranges are left to helm
	}
	newDigest, _ := hashReq(newReqs, newLocked)
	want := strings.NewReplacer("version: 1.0.0", "version: 1.1.0", `version: "2.0.0"`, `version: "2.1.0"`, digest, newDigest).Replace(lock)
	if got := string(out[lockFile]); got != want {
		t.Fatalf("Chart.lock =\n%s\nwant\n%s", got, want)
	}

	// Unchanged versions: nothing to write.
	out, err = Locker{}.Process(t.Context(), dir, map[string][]byte{chartFile: []byte(chart)}, map[string][]byte{lockFile: []byte(lock)})
	if err != nil || out != nil {
		t.Errorf("unchanged: %v, %v", out, err)
	}
	// No lock, or no patched Chart.yaml: nothing to do.
	if out, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{chartFile: []byte(patched)}, map[string][]byte{}); err != nil || out != nil {
		t.Errorf("no lock: %v, %v", out, err)
	}
	if out, err := (Locker{}).Process(t.Context(), dir, map[string][]byte{}, map[string][]byte{lockFile: []byte(lock)}); err != nil || out != nil {
		t.Errorf("no manifest: %v, %v", out, err)
	}
}

func TestLockerErrors(t *testing.T) {
	chart, lock := readTestdata(t, chartFile), readTestdata(t, lockFile)
	patched := []byte(strings.Replace(chart, "version: 17.11.3", "version: 17.17.1", 1))
	run := func(dir, lock string, patched []byte) error {
		_, err := Locker{}.Process(t.Context(), dir, map[string][]byte{chartFile: patched}, map[string][]byte{lockFile: []byte(lock)})
		return err
	}
	dir := chartDir(t, chart, lock)
	stale := strings.Replace(lock, "sha256:fb04", "sha256:0000", 1)
	for name, err := range map[string]error{
		"out of sync":      run(dir, stale, patched),
		"broken lock":      run(dir, "dependencies: [\n", patched),
		"empty lock":       run(dir, "", patched),
		"no digest":        run(dir, "dependencies: []\n", patched),
		"nil dependency":   run(dir, "dependencies:\n- \ndigest: x\n", patched),
		"bad dependencies": run(dir, "dependencies: {a: 1}\ndigest: x\n", patched),
		"broken patched":   run(dir, lock, []byte("dependencies: [\n")),
		"broken original":  run(chartDir(t, "dependencies: [\n", lock), lock, patched),
		"missing chart":    run(t.TempDir(), lock, patched),
		"block digest":     run(dir, strings.Replace(lock, "digest: sha256", "digest: |-\n  sha256", 1), patched),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := run(dir, lock, patched); err != nil {
		t.Errorf("valid lock: %v", err)
	}
	if !strings.Contains(run(dir, stale, patched).Error(), "out of sync") {
		t.Error("stale lock: wrong error")
	}
}

func TestLockerScope(t *testing.T) {
	l := Locker{}
	if dir, ok := l.Scope("/root", "charts/app/Chart.yaml"); !ok || dir != "charts/app" {
		t.Errorf("Scope(Chart.yaml) = %q, %v", dir, ok)
	}
	if dir, ok := l.Scope("/root", "Chart.yaml"); !ok || dir != "." {
		t.Errorf("Scope(root Chart.yaml) = %q, %v", dir, ok)
	}
	if _, ok := l.Scope("/root", "helmfile.yaml"); ok {
		t.Error("helmfile has a scope")
	}
	if s := l.Siblings(); len(s) != 1 || s[0] != lockFile {
		t.Errorf("Siblings = %v", s)
	}
	if p, d, err := l.ScopeInputs("/root", "charts/app", []string{"Chart.yaml"}); err != nil || len(p) != 1 || p[0] != "charts/app/Chart.yaml" || d != nil {
		t.Errorf("ScopeInputs = %q, %q, %v", p, d, err)
	}
	if got := lockStyle("v1.0.0", "1.1.0"); got != "v1.1.0" {
		t.Errorf("lockStyle = %q", got)
	}
}
