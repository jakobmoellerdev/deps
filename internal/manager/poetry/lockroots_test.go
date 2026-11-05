package poetry

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestLockRoots(t *testing.T) {
	var _ manager.LockMaintainer = &poetryManager{}
	root := t.TempDir()
	for _, p := range []string{"pyproject.toml", "poetry.lock", "sub/pyproject.toml", "sub/poetry.lock", "uv/pyproject.toml",
		"uv/uv.lock", "uv/poetry.lock", "none/pyproject.toml"} {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"sub/pyproject.toml", "uv/pyproject.toml", "none/pyproject.toml", "pyproject.toml"})
	want := []manager.LockRoot{
		{Manifest: "pyproject.toml", Lockfile: "poetry.lock"},
		{Manifest: "sub/pyproject.toml", Lockfile: "sub/poetry.lock"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %v, want %v", got, want)
	}
}
