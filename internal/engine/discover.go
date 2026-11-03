package engine

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"

	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// prunedDirs are never descended into.
var prunedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true}

// discovered lists matched files per manager, as slash paths relative to root.
type discovered struct {
	actions []string
	gomod   []string
}

// discover walks root once, pruning ignored directories, and matches paths without opening files.
func discover(root string, ignore []string) (discovered, error) {
	var out discovered
	ignored := func(rel string) bool {
		for _, p := range ignore {
			if ok, _ := doublestar.Match(p, rel); ok {
				return true
			}
		}
		return false
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
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
		switch {
		case actions.Match(rel):
			out.actions = append(out.actions, rel)
		case gomod.Match(rel):
			out.gomod = append(out.gomod, rel)
		}
		return nil
	})
	slices.Sort(out.actions)
	slices.Sort(out.gomod)
	return out, err
}
