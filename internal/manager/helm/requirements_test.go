package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// testdata/requirements is stable/prometheus-operator of github.com/helm/charts, locked by
// `helm dependency update` with Helm 2: its digest is Helm 2's hash of the requirements alone.
func readRequirements(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "requirements", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func requirementsDir(t *testing.T, reqs, lock string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, requirementsFile), []byte(reqs), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, requirementsLock), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRequirementsDeps(t *testing.T) {
	const src = `dependencies:
  - name: mariadb
    version: 7.3.14
    repository: https://charts.bitnami.com/bitnami
    condition: mariadb.enabled
  - name: redis
    version: "10.5.*"
    repository: https://charts.bitnami.com/bitnami
  - name: common
    version: 0.0.5
    repository: "@stable"
  - name: local
    version: 1.0.0
    repository: file://../local
`
	m := New()
	for _, file := range []string{"chart/requirements.yaml", "requirements.yml"} {
		if !m.Match(file) {
			t.Fatalf("Match(%s) = false", file)
		}
		got, err := m.Extract(file, []byte(src), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		checkChartDeps(t, src, got, []wantChartDep{
			{name: "mariadb", source: "https://charts.bitnami.com/bitnami", version: "7.3.14", span: "7.3.14", line: 3},
			{name: "redis", source: "https://charts.bitnami.com/bitnami", version: "10.5.0", span: "10.5.*", line: 7},
			{name: "common", source: "@stable", skip: "repository alias", line: 10},
			{name: "local", skip: "local chart", line: 12},
		}, TypeRequirements)
	}
	// An Ansible Galaxy requirements.yml has no dependencies.
	got, err := m.Extract("requirements.yml", []byte("roles:\n  - name: geerlingguy.java\n"), manager.Env{})
	if err != nil || len(got) != 0 {
		t.Errorf("galaxy requirements: %v, %v", got, err)
	}
}

func TestLockerRequirementsHelm2Digest(t *testing.T) {
	reqs, lock := readRequirements(t, requirementsFile), readRequirements(t, requirementsLock)
	dir := requirementsDir(t, reqs, lock)
	// A range moves: no locked version changes, the Helm 2 digest follows the requirements.
	patched := strings.Replace(reqs, `"2.8.*"`, `"2.9.*"`, 1)
	out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{requirementsFile: []byte(patched)}, map[string][]byte{requirementsLock: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	newReqs, err := chartRequirements([]byte(patched))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := hashReqV2(newReqs)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(lock, "sha256:4cafebfa80daacbd651defea2a7cad3074e8022114d27d76a3baa1861998981b", sum, 1)
	if got := string(out[requirementsLock]); got != want {
		t.Fatalf("requirements.lock =\n%s\nwant\n%s", got, want)
	}
	if _, ok := out[lockFile]; ok {
		t.Error("wrote Chart.lock")
	}
}

func TestLockerRequirementsVersions(t *testing.T) {
	const reqs = `dependencies:
  - name: mariadb
    version: 7.3.14
    repository: https://charts.bitnami.com/bitnami
  - name: common
    version: 0.0.5
    repository: "@stable"
`
	locked := []*chartDependency{
		{Name: "mariadb", Repository: "https://charts.bitnami.com/bitnami", Version: "7.3.14"},
		{Name: "common", Repository: "https://charts.helm.sh/stable", Version: "0.0.5"},
	}
	parsed, err := chartRequirements([]byte(reqs))
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(reqs, "7.3.14", "7.10.4", 1)
	newParsed, err := chartRequirements([]byte(patched))
	if err != nil {
		t.Fatal(err)
	}
	newLocked := []*chartDependency{{Name: "mariadb", Repository: "https://charts.bitnami.com/bitnami", Version: "7.10.4"}, locked[1]}
	k := lockKinds[1]
	oldSums, err := k.digests(parsed, locked)
	if err != nil {
		t.Fatal(err)
	}
	newSums, err := k.digests(newParsed, newLocked)
	if err != nil {
		t.Fatal(err)
	}
	// Helm 3 (hashReq over requirements and lock), Helm 2 with the alias unresolved and resolved.
	for algo, name := range []string{"helm3", "helm2", "helm2 resolved"} {
		t.Run(name, func(t *testing.T) {
			lock := "dependencies:\n- name: mariadb\n  repository: https://charts.bitnami.com/bitnami\n  version: 7.3.14\n" +
				"- name: common\n  repository: https://charts.helm.sh/stable\n  version: 0.0.5\n" +
				"digest: " + oldSums[algo] + "\ngenerated: \"2020-06-22T12:36:10Z\"\n"
			dir := requirementsDir(t, reqs, lock)
			out, err := Locker{}.Process(t.Context(), dir, map[string][]byte{requirementsFile: []byte(patched)}, map[string][]byte{requirementsLock: []byte(lock)})
			if err != nil {
				t.Fatal(err)
			}
			want := strings.NewReplacer("version: 7.3.14", "version: 7.10.4", oldSums[algo], newSums[algo]).Replace(lock)
			if got := string(out[requirementsLock]); got != want {
				t.Fatalf("requirements.lock =\n%s\nwant\n%s", got, want)
			}
		})
	}

	// A lock no algorithm reproduces is out of sync.
	dir := requirementsDir(t, reqs, "dependencies: []\ndigest: sha256:00\n")
	_, err = Locker{}.Process(t.Context(), dir, map[string][]byte{requirementsFile: []byte(patched)}, map[string][]byte{requirementsLock: []byte("dependencies: []\ndigest: sha256:00\n")})
	if err == nil || !strings.Contains(err.Error(), "requirements.lock is out of sync with requirements.yaml") {
		t.Errorf("out of sync: %v", err)
	}
}

func TestLockerRequirementsScope(t *testing.T) {
	l := Locker{}
	if dir, ok := l.Scope(t.Context(), "", "charts/app/requirements.yaml"); !ok || dir != "charts/app" {
		t.Errorf("Scope = %q, %v", dir, ok)
	}
	if _, ok := l.Scope(t.Context(), "", "requirements.yml"); ok {
		t.Error("requirements.yml has a scope; Helm only reads requirements.yaml")
	}
	paths, _, err := l.ScopeInputs(t.Context(), "", "charts/app", []string{chartFile, requirementsFile})
	if err != nil || strings.Join(paths, " ") != "charts/app/Chart.yaml charts/app/requirements.yaml" {
		t.Errorf("ScopeInputs = %v, %v", paths, err)
	}
}
