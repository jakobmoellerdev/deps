package pep621

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
)

// TestLockRoots checks the uv workspace root and that pep621 and poetry, which both extract
// every pyproject.toml, claim disjoint lockfiles.
func TestLockRoots(t *testing.T) {
	var _ manager.LockMaintainer = &pep621Manager{}
	root := t.TempDir()
	for p, c := range map[string]string{
		"pyproject.toml":            "[project]\nname = \"root\"\n[tool.uv.workspace]\nmembers = [\"packages/*\"]\n",
		"uv.lock":                   "",
		"packages/a/pyproject.toml": "",
		"poetry2/pyproject.toml":    "[project]\nname = \"p\"\ndependencies = [\"six>=1\"]\n",
		"poetry2/poetry.lock":       "",
		"pdm/pyproject.toml":        "",
		"pdm/pdm.lock":              "",
		"uvpoetry/pyproject.toml":   "",
		"uvpoetry/uv.lock":          "",
		"uvpoetry/poetry.lock":      "",
		"unlocked/pyproject.toml":   "",
	} {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"packages/a/pyproject.toml", "pdm/pyproject.toml", "poetry2/pyproject.toml", "pyproject.toml",
		"unlocked/pyproject.toml", "uvpoetry/pyproject.toml"}
	got := New().(manager.LockMaintainer).LockRoots(root, files)
	want := []manager.LockRoot{
		{Manifest: "pdm/pyproject.toml", Lockfile: "pdm/pdm.lock"},
		{Manifest: "pyproject.toml", Lockfile: "uv.lock"},
		{Manifest: "uvpoetry/pyproject.toml", Lockfile: "uvpoetry/uv.lock"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %v, want %v", got, want)
	}
	other := poetry.New().(manager.LockMaintainer).LockRoots(root, files)
	if w := []manager.LockRoot{{Manifest: "poetry2/pyproject.toml", Lockfile: "poetry2/poetry.lock"}}; !slices.Equal(other, w) {
		t.Errorf("poetry LockRoots = %v, want %v", other, w)
	}
}
