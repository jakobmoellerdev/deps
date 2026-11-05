package toolrun

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyTree copies the tree at src to dst, keeping file modes and symlinks. Directories below
// src for which skip reports true are left out.
func CopyTree(src, dst string, skip func(path string, d fs.DirEntry) bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch t := d.Type(); {
		case t.IsDir():
			if rel != "." && skip(p, d) {
				return filepath.SkipDir
			}
			return os.MkdirAll(to, 0o755)
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case t.IsRegular():
			return copyFile(p, to)
		}
		return nil // sockets, devices
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
