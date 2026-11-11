package engine

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitignore"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pattern"
)

// prunedDirs are never descended into.
var prunedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true}

// discover walks root once, pruning ignored directories, and matches paths without opening
// files. A file goes to every manager that matches it (a custom manager may target a file a
// built-in one handles too); the result holds the sorted slash paths (relative to root) per
// manager, indexed like managers. With gitIgnore, the .gitignore files of the repository
// (and its .git/info/exclude) prune directories and files as they do for git.
func discover(root string, ignore []string, gitIgnore bool, managers []manager.Manager) ([][]string, error) {
	out := make([][]string, len(managers))
	ign, err := pattern.CompileCaseSensitive(ignore)
	if err != nil {
		return nil, err
	}
	base := filepath.Clean(root)
	ignored := ign.Match
	var gi *gitignore.Matcher
	if gitIgnore {
		gi = gitignore.New(root)
	}
	disp := newDispatch(managers)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
			if prunedDirs[d.Name()] || ignored(rel) || gi.Ignored(rel, true) {
				slog.Debug("directory pruned", "dir", rel)
				return fs.SkipDir
			}
			gi.Enter(rel)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// ignorePaths is checked only once a manager claims the file: most files match none.
		var checked, skip bool
		for _, i := range disp.candidates(rel) {
			if !managers[i].Match(rel) {
				continue
			}
			if !checked {
				checked, skip = true, ignored(rel) || gi.Ignored(rel, false)
			}
			if skip {
				break
			}
			out[i] = append(out[i], rel)
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
