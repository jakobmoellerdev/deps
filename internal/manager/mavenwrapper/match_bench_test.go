package mavenwrapper

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// BenchmarkMatch runs Match over every file path of this repository, the mix discovery sees.
func BenchmarkMatch(b *testing.B) {
	var paths []string
	root := filepath.Join("..", "..", "..")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	m := New()
	for b.Loop() {
		for _, p := range paths {
			m.Match(p)
		}
	}
}
