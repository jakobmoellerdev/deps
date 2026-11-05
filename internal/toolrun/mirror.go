package toolrun

import (
	"os"
	"path/filepath"
	"strings"
)

// MirrorAncestors recreates the directories from the repository top (the nearest ancestor of
// dir holding .git) down to dir below tmp, with every other entry of each one symlinked, so
// path dependencies outside dir (`path = "../crates/x"`, `gem "x", path: "../x"`) resolve as in
// the checkout. It returns the directory for dir's mirror: tmp itself outside a repository.
func MirrorAncestors(dir, tmp string) (string, error) {
	top := ""
	for d := filepath.Dir(dir); d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			top = d
			break
		}
	}
	if top == "" {
		return tmp, nil
	}
	rel, err := filepath.Rel(top, dir)
	if err != nil {
		return "", err
	}
	cur, base := top, tmp
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.Name() == part || e.Name() == ".git" {
				continue
			}
			if err := os.Symlink(filepath.Join(cur, e.Name()), filepath.Join(base, e.Name())); err != nil {
				return "", err
			}
		}
		cur, base = filepath.Join(cur, part), filepath.Join(base, part)
		if err := os.Mkdir(base, 0o755); err != nil {
			return "", err
		}
	}
	return base, nil
}
