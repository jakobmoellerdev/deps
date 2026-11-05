package toolrun

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFiles(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "gradlew")
	if err := os.WriteFile(script, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"gradlew": []byte("new"), "a/b/c.txt": []byte("c")}
	if err := WriteFiles(root, files); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "a", "b", "c.txt")); err != nil || string(b) != "c" {
		t.Fatalf("a/b/c.txt = %q, %v", b, err)
	}
	st, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("gradlew mode = %v, want the existing 0755 kept", st.Mode().Perm())
	}
	if !Changed(root, map[string][]byte{"gradlew": []byte("old")}) || Changed(root, files) {
		t.Error("Changed does not compare against the written files")
	}
	if !Changed(root, map[string][]byte{"missing": nil}) {
		t.Error("an unreadable file counts as changed")
	}

	// a regular file in the way of a parent directory fails
	if err := WriteFiles(root, map[string][]byte{"gradlew/x": nil}); err == nil {
		t.Error("WriteFiles under a regular file succeeded")
	}
}
