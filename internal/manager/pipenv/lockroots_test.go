package pipenv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestLockRoots(t *testing.T) {
	var _ manager.LockMaintainer = pipenvManager{}
	root := t.TempDir()
	for _, p := range []string{"Pipfile", "Pipfile.lock", "a/Pipfile", "a/Pipfile.lock", "a/pyproject.toml",
		"mixed/Pipfile", "mixed/Pipfile.lock", "mixed/pyproject.toml", "mixed/uv.lock", "none/Pipfile"} {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// mixed: its uv.lock is the pep621 manager's lockfile there.
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"none/Pipfile", "mixed/Pipfile", "a/Pipfile", "Pipfile"})
	want := []manager.LockRoot{
		{Manifest: "Pipfile", Lockfile: "Pipfile.lock"},
		{Manifest: "a/Pipfile", Lockfile: "a/Pipfile.lock"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %v, want %v", got, want)
	}
}
