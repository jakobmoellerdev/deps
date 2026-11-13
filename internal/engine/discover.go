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
// manager, indexed like managers. A root that is a symlink to a directory is followed. With
// gitIgnore, the .gitignore files of the repository (and its .git/info/exclude) prune
// directories and files as they do for git. A path the walk cannot read (an unreadable
// directory) is logged and left out, so one of them does not cost the run every other file.
func discover(root string, ignore []string, gitIgnore bool, managers []manager.Manager) ([][]string, error) {
	out := make([][]string, len(managers))
	ign, err := pattern.CompileCaseSensitive(ignore)
	if err != nil {
		return nil, err
	}
	// filepath.WalkDir lstats its root, so a --dir that is itself a symlink to a directory
	// would walk nothing: the callback is handed the link, takes the root branch and returns.
	// The link is resolved first, which also lets the .gitignore levels above the root be read
	// from the real repository. Paths are reported relative to the root either way. A root that
	// cannot be resolved (it does not exist) is walked as given, so WalkDir reports its error.
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		walkRoot = root
	}
	base := filepath.Clean(walkRoot)
	ignored := ign.Match
	var gi *gitignore.Matcher
	if gitIgnore {
		gi = gitignore.New(walkRoot)
	}
	err = filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == walkRoot {
				return err // nothing to walk at all
			}
			// One unreadable directory (or a file that went away mid-walk) leaves the rest
			// of the tree discoverable.
			rel, relErr := relPath(base, walkRoot, p)
			if relErr != nil {
				rel = p
			}
			slog.Warn("path skipped, it cannot be read", "path", filepath.ToSlash(rel), "err", err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == walkRoot {
			return nil
		}
		rel, err := relPath(base, walkRoot, p)
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
		for i, m := range managers {
			if !m.Match(rel) {
				continue
			}
			if !checked {
				checked, skip = true, ignored(rel) || gi.Ignored(rel, false)
				if skip {
					// A manager claims the file, so its absence from the plan would
					// otherwise be unexplained.
					slog.Debug("file ignored", "file", rel)
				}
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
