package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
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

// Build re-reads the files touched by updates, patches them with the byte edits of each
// update's manager (looked up by dep.Dep.Manager in managers) and, when tidy is non-nil,
// runs go mod tidy for files with gomod updates. A failing file (and its go.sum) is dropped
// and its error returned; the remaining changes are returned sorted by Path.
func Build(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, tidy Tidier) ([]FileChange, []error) {
	byName := make(map[string]manager.Manager, len(managers))
	for _, m := range managers {
		byName[m.Name()] = m
	}
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
			results[i], errs[i] = buildFile(ctx, root, f, byName, byFile[f], tidy)
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

func buildFile(ctx context.Context, root, file string, managers map[string]manager.Manager, ups []dep.Update, tidy Tidier) ([]FileChange, error) {
	edits := make([]patch.Edit, 0, len(ups)+1)
	isMod := false
	for _, u := range ups {
		m := managers[u.Dep.Manager]
		if m == nil {
			return nil, fmt.Errorf("unknown manager %q", u.Dep.Manager)
		}
		e, err := m.Edits(u)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", u.Dep.Name, err)
		}
		edits = append(edits, e...)
		isMod = isMod || u.Dep.Manager == gomod.Name
	}
	abs := filepath.Join(root, filepath.FromSlash(file))
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	old, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	patched, err := patch.Apply(old, edits)
	if err != nil {
		return nil, err
	}
	slog.DebugContext(ctx, "file patched", "file", file, "edits", len(edits))
	changes := []FileChange{{Path: file, Mode: st.Mode().Perm(), Old: old, New: patched}}
	if !isMod || tidy == nil {
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
	start := time.Now()
	newMod, newSum, err := tidy.Tidy(ctx, filepath.Dir(abs), patched, oldSum)
	<-tidySem
	if err != nil {
		return nil, err
	}
	slog.DebugContext(ctx, "go mod tidy", "file", file, "goSumChanged", !bytes.Equal(newSum, oldSum), "duration", time.Since(start))
	changes[0].New = newMod
	if !bytes.Equal(newSum, oldSum) {
		changes = append(changes, FileChange{Path: sumPath, Mode: sumMode, Old: oldSum, New: newSum})
	}
	return changes, nil
}
