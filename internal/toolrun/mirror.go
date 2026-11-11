package toolrun

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MirrorOptions configures Mirror.
type MirrorOptions struct {
	// Pattern names the temporary directory (os.MkdirTemp).
	Pattern string
	// Files are written into the mirror (slash paths relative to dir); a path that is a key is
	// always a real file, never a symlink to the checkout's, whatever its content.
	Files map[string][]byte
	// Skip reports whether the entry rel (slash path relative to dir) is left out of the
	// mirror; nil keeps every entry.
	Skip func(rel string, d fs.DirEntry) bool
	// Recreate reports whether the directory rel is recreated with its entries mirrored one by
	// one instead of symlinked whole; nil recreates only the directories leading to a Files
	// key, which always are.
	Recreate func(rel string, d fs.DirEntry) bool
}

// SkipGit is a MirrorOptions.Skip leaving out dir's .git.
func SkipGit(rel string, _ fs.DirEntry) bool { return rel == ".git" }

// Mirror builds a temporary tree for running a tool on dir without touching the checkout:
// below the mirrored repository ancestors (MirrorAncestors), the entries of dir are symlinked,
// except those Skip leaves out, the directories leading to a Files key or matching Recreate,
// which are recreated and mirrored the same way, and the Files, which are written. Path
// dependencies outside dir thus resolve as in the checkout. base is the mirror of dir; the
// caller removes tmp, which Mirror does itself when it fails.
func Mirror(dir string, o MirrorOptions) (tmp, base string, err error) {
	if tmp, err = os.MkdirTemp("", o.Pattern); err != nil {
		return "", "", err
	}
	if base, err = mirrorInto(dir, tmp, o); err != nil {
		os.RemoveAll(tmp)
		return "", "", err
	}
	return tmp, base, nil
}

// mirrorInto builds the mirror of dir (Mirror) below tmp and returns the mirror of dir.
func mirrorInto(dir, tmp string, o MirrorOptions) (string, error) {
	base, err := MirrorAncestors(dir, tmp)
	if err != nil {
		return "", err
	}
	parents := map[string]bool{} // the directories leading to a file
	for f := range o.Files {
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			parents[d] = true
		}
	}
	var mirror func(rel string) error
	mirror = func(rel string) error {
		entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := path.Join(rel, e.Name())
			if _, ok := o.Files[p]; ok || o.Skip != nil && o.Skip(p, e) {
				continue
			}
			target := filepath.Join(base, filepath.FromSlash(p))
			if recreate := parents[p] || o.Recreate != nil && o.Recreate(p, e); recreate && isDir(dir, p, e) {
				if err := os.Mkdir(target, 0o755); err != nil {
					return err
				}
				if err := mirror(p); err != nil {
					return err
				}
				continue
			}
			if err := os.Symlink(filepath.Join(dir, filepath.FromSlash(p)), target); err != nil {
				return err
			}
		}
		return nil
	}
	if err := mirror("."); err != nil {
		return "", err
	}
	return base, WriteFiles(base, o.Files)
}

// isDir reports whether the entry e of dir (at the slash path rel) is a directory, following a
// symlink: a symlinked directory holding a Files key must be recreated, or WriteFiles would
// write through it into the checkout.
func isDir(dir, rel string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil && st.IsDir()
}

// MirrorAncestors recreates the directories from the repository top (the nearest ancestor of
// dir holding .git) down to dir below tmp, with every other entry of each one symlinked, so
// path dependencies outside dir (`path = "../crates/x"`, `gem "x", path: "../x"`) resolve as in
// the checkout; a submodule's (a .git file) are those of its superproject. It returns the
// directory for dir's mirror: tmp itself when dir is a repository's top (a .git directory)
// or outside a repository.
func MirrorAncestors(dir, tmp string) (string, error) {
	if st, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && st.IsDir() {
		return tmp, nil
	}
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
