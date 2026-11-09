package toolrun

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DiffTree adds to out, under prefix plus their slash path below the directories, the
// regular files of next whose content differs from, or is missing in, cur, and nil for the
// files of cur missing in next. Contents are read one file at a time and kept only when
// they changed; a size difference skips reading the old file. Entries other than regular files
// (symlinks) are neither compared nor reported.
func DiffTree(cur, next, prefix string, out map[string][]byte) error {
	seen := map[string]bool{}
	err := filepath.WalkDir(next, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(next, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		old := filepath.Join(cur, filepath.FromSlash(rel))
		if st, err := os.Stat(old); err == nil && st.Mode().IsRegular() && st.Size() == int64(len(b)) {
			ob, err := os.ReadFile(old)
			if err != nil {
				return err
			}
			if bytes.Equal(ob, b) {
				return nil
			}
		}
		out[prefix+"/"+rel] = b
		return nil
	})
	if err != nil {
		return err
	}
	err = filepath.WalkDir(cur, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(cur, p)
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); !seen[rel] {
			out[prefix+"/"+rel] = nil
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
