package datasource

import (
	"path"
	"path/filepath"
	"strings"
)

// RootOf returns the scanned root: dir, the manifest's absolute directory, without the
// directory part of the slash path file. dir is returned unchanged when it does not end in
// that part.
func RootOf(file, dir string) string {
	rel := path.Dir(file)
	if rel == "." {
		return dir
	}
	suffix := string(filepath.Separator) + filepath.FromSlash(rel)
	if strings.HasSuffix(dir, suffix) {
		return dir[:len(dir)-len(suffix)]
	}
	return dir
}
