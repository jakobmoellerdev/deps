package toolrun

import (
	"bytes"
	"os"
	"path/filepath"
)

// WriteFiles writes files (slash paths relative to root) into root, creating parent
// directories as needed. New files get mode 0644; a file that already exists keeps its mode.
func WriteFiles(root string, files map[string][]byte) error {
	for rel, b := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Changed reports whether any of files (slash paths relative to dir) differs from its file
// in dir; a file that cannot be read counts as changed.
func Changed(dir string, files map[string][]byte) bool {
	for p, b := range files {
		if old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); err != nil || !bytes.Equal(old, b) {
			return true
		}
	}
	return false
}
