package toolrun

import (
	"bytes"
	"fmt"
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

// TestDiffTreeLargeFiles covers files read in more than one comparison block: a difference
// past the first block must be found.
func TestDiffTreeLargeFiles(t *testing.T) {
	same := bytes.Repeat([]byte("vendored source\n"), compareBlock/4)
	tail := append(bytes.Clone(same[:len(same)-1]), 'x')
	cur, next := t.TempDir(), t.TempDir()
	if err := WriteFiles(cur, map[string][]byte{"big/same": same, "big/tail": same}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFiles(next, map[string][]byte{"big/same": same, "big/tail": tail}); err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	if err := DiffTree(cur, next, "vendor", out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !bytes.Equal(out["vendor/big/tail"], tail) {
		t.Errorf("DiffTree reported %d files, want only big/tail", len(out))
	}
}

// benchTree writes n files of the same size below dir, the last byte of each telling the
// trees apart when differ is set.
func benchTree(tb testing.TB, dir string, n int, differ bool) {
	tb.Helper()
	files := make(map[string][]byte, n)
	for i := range n {
		b := bytes.Repeat([]byte("package vendored\n"), 512) // 8.5 KiB, a typical vendored file
		if differ {
			b[len(b)-1] = 'x'
		}
		files[fmt.Sprintf("pkg%d/file.go", i)] = b
	}
	if err := WriteFiles(dir, files); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkDiffTreeUnchanged walks a regenerated vendor tree that turned out identical, the
// usual outcome of a dependency update that does not touch the vendored sources.
func BenchmarkDiffTreeUnchanged(b *testing.B) {
	cur, next := b.TempDir(), b.TempDir()
	benchTree(b, cur, 200, false)
	benchTree(b, next, 200, false)
	out := map[string][]byte{}
	b.ReportAllocs()
	for b.Loop() {
		clear(out)
		if err := DiffTree(cur, next, "vendor", out); err != nil {
			b.Fatal(err)
		}
		if len(out) != 0 {
			b.Fatalf("%d files reported as changed", len(out))
		}
	}
}

// BenchmarkDiffTreeChanged walks a tree whose files all differ in content but not in size,
// the worst case of the comparison.
func BenchmarkDiffTreeChanged(b *testing.B) {
	cur, next := b.TempDir(), b.TempDir()
	benchTree(b, cur, 200, false)
	benchTree(b, next, 200, true)
	out := map[string][]byte{}
	b.ReportAllocs()
	for b.Loop() {
		clear(out)
		if err := DiffTree(cur, next, "vendor", out); err != nil {
			b.Fatal(err)
		}
		if len(out) != 200 {
			b.Fatalf("%d files reported as changed", len(out))
		}
	}
}
