package engine

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// prunedDirs are never descended into.
var prunedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true}

// discover walks root once, pruning ignored directories, and matches paths without opening
// files. A file goes to every manager that matches it (a custom manager may target a file a
// built-in one handles too); the result holds the sorted slash paths (relative to root) per
// manager, indexed like managers.
func discover(root string, ignore []string, managers []manager.Manager) ([][]string, error) {
	out := make([][]string, len(managers))
	ignored := func(rel string) bool {
		for _, p := range ignore {
			if ok, _ := doublestar.Match(p, rel); ok {
				return true
			}
		}
		return false
	}
	base := filepath.Clean(root)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := relPath(base, root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if prunedDirs[d.Name()] || ignored(rel) {
				slog.Debug("directory pruned", "dir", rel)
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || ignored(rel) {
			return nil
		}
		for i, m := range managers {
			if m.Match(rel) {
				out[i] = append(out[i], rel)
			}
		}
		return nil
	})
	for _, files := range out {
		slices.Sort(files)
	}
	return out, err
}

// relPath returns p relative to root without filepath.Rel: WalkDir builds every entry below
// root as filepath.Join(root, ...), so p is base (the cleaned root) plus a separator and the
// relative path. Anything else falls back to filepath.Rel.
func relPath(base, root, p string) (string, error) {
	switch {
	case base == ".":
		return p, nil
	case strings.HasSuffix(base, string(filepath.Separator)) && strings.HasPrefix(p, base) && len(p) > len(base):
		return p[len(base):], nil
	case len(p) > len(base) && p[len(base)] == filepath.Separator && strings.HasPrefix(p, base):
		return p[len(base)+1:], nil
	}
	return filepath.Rel(root, p)
}
