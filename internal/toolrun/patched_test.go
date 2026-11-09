package toolrun

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	disk, patched := filepath.Join(dir, "disk"), filepath.Join(dir, "patched")
	for _, p := range []string{disk, patched} {
		if err := os.WriteFile(p, []byte("on disk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if ctx := WithPatched(t.Context(), nil); ctx != t.Context() {
		t.Error("WithPatched without files wraps the context")
	}
	ctx := WithPatched(t.Context(), map[string][]byte{patched: []byte("patched")})
	for p, want := range map[string]string{disk: "on disk", patched: "patched"} {
		if b, err := ReadFile(ctx, p); err != nil || string(b) != want {
			t.Errorf("ReadFile(%s) = %q, %v; want %q", p, b, err, want)
		}
	}
	if b, err := ReadFile(t.Context(), patched); err != nil || string(b) != "on disk" {
		t.Errorf("ReadFile without patches = %q, %v", b, err)
	}
	if _, err := ReadFile(ctx, filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}
