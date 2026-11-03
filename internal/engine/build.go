package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// FileChange is the new content of one file. Path is a slash path relative to the root;
// Old is nil when the file does not exist yet.
type FileChange struct {
	Path     string
	Mode     fs.FileMode
	Old, New []byte
}

// tidySem bounds concurrent `go mod tidy` processes across all Build calls.
var tidySem = make(chan struct{}, 4)

// Build re-reads the files touched by updates, patches their byte spans and, when tidy is
// non-nil, runs go mod tidy for go.mod files. A failing file (and its go.sum) is dropped and
// its error returned; the remaining changes are returned sorted by Path.
func Build(ctx context.Context, root string, updates []dep.Update, tidy Tidier) ([]FileChange, []error) {
	byFile := map[string][]dep.Update{}
	var files []string
	for _, u := range updates {
		if _, ok := byFile[u.Dep.File]; !ok {
			files = append(files, u.Dep.File)
		}
		byFile[u.Dep.File] = append(byFile[u.Dep.File], u)
	}
	slices.Sort(files)

	results := make([][]FileChange, len(files))
	errs := make([]error, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Go(func() {
			results[i], errs[i] = buildFile(ctx, root, f, byFile[f], tidy)
			if errs[i] != nil {
				errs[i] = fmt.Errorf("%s: %w", f, errs[i])
			}
		})
	}
	wg.Wait()

	var out []FileChange
	for _, r := range results {
		out = append(out, r...)
	}
	slices.SortFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	var errList []error
	for _, e := range errs {
		if e != nil {
			errList = append(errList, e)
		}
	}
	return out, errList
}

func buildFile(ctx context.Context, root, file string, ups []dep.Update, tidy Tidier) ([]FileChange, error) {
	abs := filepath.Join(root, filepath.FromSlash(file))
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	old, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	edits := make([]patch.Edit, 0, len(ups)+1)
	for _, u := range ups {
		d := u.Dep
		switch {
		case u.NewDigest == "":
			edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
		case d.Digest != "":
			edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Digest, New: u.NewDigest})
			if u.NewVersion != d.Version {
				edits = append(edits, patch.Edit{Start: d.VersionSpan.Start, End: d.VersionSpan.End, Old: d.Version, New: u.NewVersion})
			}
		default:
			edits = append(edits,
				patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewDigest},
				patch.Edit{Start: d.CommentInsert, End: d.CommentInsert, New: " # " + u.NewVersion})
		}
	}
	patched, err := patch.Apply(old, edits)
	if err != nil {
		return nil, err
	}
	changes := []FileChange{{Path: file, Mode: st.Mode().Perm(), Old: old, New: patched}}
	if ups[0].Dep.Manager != gomod.Name || tidy == nil {
		return changes, nil
	}

	sumPath := path.Join(path.Dir(file), "go.sum")
	sumAbs := filepath.Join(root, filepath.FromSlash(sumPath))
	sumMode := fs.FileMode(0o644)
	oldSum, err := os.ReadFile(sumAbs)
	switch {
	case err == nil:
		if sst, err := os.Stat(sumAbs); err == nil {
			sumMode = sst.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
		oldSum = nil
	default:
		return nil, err
	}

	select {
	case tidySem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	newMod, newSum, err := tidy.Tidy(ctx, filepath.Dir(abs), patched, oldSum)
	<-tidySem
	if err != nil {
		return nil, err
	}
	changes[0].New = newMod
	if !bytes.Equal(newSum, oldSum) {
		changes = append(changes, FileChange{Path: sumPath, Mode: sumMode, Old: oldSum, New: newSum})
	}
	return changes, nil
}
