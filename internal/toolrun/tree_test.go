package toolrun

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffTree(t *testing.T) {
	cur, next := t.TempDir(), t.TempDir()
	if err := WriteFiles(cur, map[string][]byte{"a/same": []byte("s"), "a/changed": []byte("old"), "a/resized": []byte("o"), "gone": []byte("g")}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFiles(next, map[string][]byte{"a/same": []byte("s"), "a/changed": []byte("new"), "a/resized": []byte("longer"), "b/added": []byte("n")}); err != nil {
		t.Fatal(err)
	}
	// Symlinks (jb's links to local packages) are neither compared nor reported as deleted.
	for _, d := range []string{cur, next} {
		if err := os.Symlink("../a", filepath.Join(d, "link")); err != nil {
			t.Fatal(err)
		}
	}
	out := map[string][]byte{}
	if err := DiffTree(cur, next, "vendor", out); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{"vendor/a/changed": []byte("new"), "vendor/a/resized": []byte("longer"), "vendor/b/added": []byte("n"), "vendor/gone": nil}
	if !maps.EqualFunc(out, want, func(a, b []byte) bool { return bytes.Equal(a, b) && (a == nil) == (b == nil) }) {
		t.Errorf("DiffTree = %q, want %q", out, want)
	}

	out = map[string][]byte{}
	if err := DiffTree(filepath.Join(t.TempDir(), "missing"), next, "vendor", out); err != nil || string(out["vendor/b/added"]) != "n" || len(out) != 4 {
		t.Errorf("DiffTree into a missing tree = %q, %v", out, err)
	}
}
