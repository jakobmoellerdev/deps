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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// FileChange is the new content of one file. Path is a slash path relative to the root;
// Old is nil when the file does not exist yet.
type FileChange struct {
	Path     string
	Mode     fs.FileMode
	Old, New []byte
}

// PostProcessor regenerates the files derived from a manifest (go.sum, package-lock.json)
// after deps patched it. Implementations work on copies and never touch the working tree.
type PostProcessor interface {
	// Siblings lists the names of the files next to the manifest that Process reads or
	// regenerates.
	Siblings() []string
	// Process returns regenerated files by base name. dir is the absolute directory of the
	// manifest, manifest its patched content and files the current content of every existing
	// sibling. A result under the manifest's own name replaces the patched manifest; siblings
	// absent from the result are unchanged.
	Process(ctx context.Context, dir string, manifest []byte, files map[string][]byte) (map[string][]byte, error)
}

// postSem bounds concurrent post-processor runs (go mod tidy, npm install) across all Build
// calls.
var postSem = make(chan struct{}, 4)

// Build re-reads the files touched by updates, patches them with the byte edits of each
// update's manager (looked up by dep.Dep.Manager in managers) and runs the post-processor
// registered for that manager in post (nil = none, as for plan). A failing file (and its
// regenerated siblings) is dropped and its error returned; the remaining changes are returned
// sorted by Path.
func Build(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, post map[string]PostProcessor) ([]FileChange, []error) {
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
			results[i], errs[i] = buildFile(ctx, root, f, byName, byFile[f], post)
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

func buildFile(ctx context.Context, root, file string, managers map[string]manager.Manager, ups []dep.Update, post map[string]PostProcessor) ([]FileChange, error) {
	edits := make([]patch.Edit, 0, len(ups)+1)
	var procs []string // managers of the file with a post-processor, in update order
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
		if post[u.Dep.Manager] != nil && !slices.Contains(procs, u.Dep.Manager) {
			procs = append(procs, u.Dep.Manager)
		}
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
	for _, name := range procs {
		if changes, err = postProcess(ctx, root, file, name, post[name], changes); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// sibling is the current state of a file next to a manifest.
type sibling struct {
	path    string // slash path relative to the root
	mode    fs.FileMode
	content []byte // nil when the file does not exist
}

// postProcess runs p on the manifest (changes[0]) and merges its output: the manifest is
// replaced, a sibling is added when its content changed (mode 0644 when it is new).
func postProcess(ctx context.Context, root, file, managerName string, p PostProcessor, changes []FileChange) ([]FileChange, error) {
	dir := path.Dir(file)
	names := p.Siblings()
	sibs := make(map[string]sibling, len(names))
	files := make(map[string][]byte, len(names))
	for _, n := range names {
		s := sibling{path: path.Join(dir, n), mode: 0o644}
		sAbs := filepath.Join(root, filepath.FromSlash(s.path))
		b, err := os.ReadFile(sAbs)
		switch {
		case err == nil:
			s.content = b
			files[n] = b
			if sst, err := os.Stat(sAbs); err == nil {
				s.mode = sst.Mode().Perm()
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		sibs[n] = s
	}

	select {
	case postSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	start := time.Now()
	out, err := p.Process(ctx, filepath.Join(root, filepath.FromSlash(dir)), changes[0].New, files)
	<-postSem
	if err != nil {
		return nil, err
	}
	var changed []string
	if b, ok := out[path.Base(file)]; ok {
		changes[0].New = b
	}
	for _, n := range names {
		b, ok := out[n]
		s := sibs[n]
		if !ok || bytes.Equal(b, s.content) {
			continue
		}
		changed = append(changed, n)
		changes = append(changes, FileChange{Path: s.path, Mode: s.mode, Old: s.content, New: b})
	}
	slog.DebugContext(ctx, "post-processed", "file", file, "manager", managerName, "changed", changed, "duration", time.Since(start))
	return changes, nil
}
