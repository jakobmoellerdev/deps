package toolrun

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// compareBlock is the block size of the content comparison of two files of equal size; it
// holds whole vendored sources, which are read in one go.
const compareBlock = 32 << 10

// DiffTree adds to out, under prefix plus their slash path below the directories, the
// regular files of next whose content differs from, or is missing in, cur, and nil for the
// files of cur missing in next. A file whose size changed is reported without comparing
// contents, files of equal size are compared block by block, and only a file that really
// differs is read whole, so an unchanged tree costs no allocation per file. Entries other
// than regular files (symlinks) are neither compared nor reported.
func DiffTree(cur, next, prefix string, out map[string][]byte) error {
	var bufs [2][]byte
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
		old := filepath.Join(cur, filepath.FromSlash(rel))
		if unchanged(old, p, e, &bufs) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
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

// unchanged reports whether the regular file next, walked as e, is already in the tree as
// old with the same content. A file that cannot be stat'ed, read or sized is reported as
// changed: the caller then reads next, which reports a real error on it.
func unchanged(old, next string, e fs.DirEntry, bufs *[2][]byte) bool {
	st, err := os.Stat(old)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	fi, err := e.Info()
	if err != nil || fi.Size() != st.Size() {
		return false
	}
	if bufs[0] == nil {
		bufs[0], bufs[1] = make([]byte, compareBlock), make([]byte, compareBlock)
	}
	return equalFiles(old, next, bufs)
}

// equalFiles reports whether the files a and b hold the same bytes, reading both in blocks
// and stopping at the first difference. A read error reports them as different.
func equalFiles(a, b string, bufs *[2][]byte) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	for {
		na, aErr := io.ReadFull(fa, bufs[0])
		nb, bErr := io.ReadFull(fb, bufs[1])
		if na != nb || !bytes.Equal(bufs[0][:na], bufs[1][:nb]) {
			return false
		}
		if aErr != nil || bErr != nil {
			return atEOF(aErr) && atEOF(bErr)
		}
	}
}

// atEOF reports whether err is how io.ReadFull ends a file, partially filled or not.
func atEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
