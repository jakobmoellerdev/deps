package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// ScopeInputLister is implemented by post-processors that name what they read of the
// repository for a scope, so that a build input fingerprint (Inputs) follows those files
// instead of everything below the scope directory.
type ScopeInputLister interface {
	// ScopeInputs lists what a run in the scope directory dir (a slash path relative to
	// root) reads besides its manifests (slash paths relative to dir, sorted) and siblings,
	// which Inputs adds itself. paths are slash paths relative to root whose content counts
	// (a directory counts by its whole tree); a file read only when it exists is listed when
	// it exists. digests are opaque strings over inputs that no path names whole (the import
	// paths of a Go module's .go files), computed from the files below root. An input that
	// cannot be named either way is an error: the group is then always built.
	ScopeInputs(_ context.Context, root, dir string, manifests []string) (paths, digests []string, err error)
}

// BuildInputs is what Build reads of the repository for one group: Paths are slash paths
// relative to root ("." is root itself) whose content counts, Digests the ScopeInputs
// digests. Both are sorted, without duplicates.
type BuildInputs struct {
	Paths, Digests []string
}

// Inputs lists, for each group of updates (nil skips the group), what Build makes of them
// with post depends on: every file the updates edit (their deps' files and the
// manager.FileEditor edits) and, for each post-processing scope, the siblings present in
// root plus what its post-processor names (ScopeInputLister), or else the scope directory
// itself. Inputs reads files only (Scope, FileEdits, ScopeInputs) and runs no
// post-processor; scopes shared by several groups are listed once. A group whose inputs
// cannot be listed, or include a path outside root, gets an error instead. The bot hashes
// the inputs, with the objects at the paths in the base commit, into the input fingerprint
// of a PR.
func Inputs(ctx context.Context, root string, managers []manager.Manager, post map[string]PostProcessor, groups [][]dep.Update) ([]BuildInputs, []error) {
	out := make([]BuildInputs, len(groups))
	errs := make([]error, len(groups))
	type scopeInputs struct {
		paths, digests []string
		err            error
	}
	listed := map[string]scopeInputs{} // by manager, scope directory and manifests
	for g, ups := range groups {
		if ups == nil {
			continue
		}
		files, err := groupFiles(root, managers, ups)
		if err != nil {
			errs[g] = err
			continue
		}
		paths := map[string]bool{}
		digests := map[string]bool{}
		type scope struct{ mgr, dir string }
		scopes := map[scope][]string{} // manifests relative to the scope directory
		for _, f := range files {
			paths[f.path] = true
			p := post[f.mgr]
			if p == nil {
				continue
			}
			dir, ok := p.Scope(ctx, root, f.path)
			if !ok {
				continue
			}
			rel := f.path
			if dir != "." {
				if rel, ok = strings.CutPrefix(rel, dir+"/"); !ok {
					errs[g] = fmt.Errorf("post-processing scope %s does not contain %s", dir, f.path)
					break
				}
			}
			k := scope{f.mgr, dir}
			if !slices.Contains(scopes[k], rel) {
				scopes[k] = append(scopes[k], rel)
			}
		}
		if errs[g] != nil {
			continue
		}
		for k, manifests := range scopes {
			slices.Sort(manifests)
			key := k.mgr + "\x00" + k.dir + "\x00" + strings.Join(manifests, "\x00")
			in, ok := listed[key]
			if !ok {
				in.paths, in.digests, in.err = scopeInputsOf(ctx, root, k.dir, manifests, post[k.mgr])
				listed[key] = in
			}
			if in.err != nil {
				errs[g] = fmt.Errorf("%s: %w", path.Join(k.dir, manifests[0]), in.err)
				break
			}
			for _, p := range in.paths {
				paths[p] = true
			}
			for _, d := range in.digests {
				digests[d] = true
			}
		}
		if errs[g] != nil {
			continue
		}
		for p := range paths {
			if p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
				errs[g] = fmt.Errorf("input %s is outside the root", p)
				break
			}
		}
		if errs[g] == nil {
			out[g] = BuildInputs{Paths: slices.Sorted(maps.Keys(paths)), Digests: slices.Sorted(maps.Keys(digests))}
		}
	}
	return out, errs
}

// managedFile is a file a group edits, with the manager whose post-processor derives from it.
type managedFile struct{ mgr, path string }

// groupFiles lists the files the updates edit: their deps' files and the manager.FileEditor
// edits, in update order, possibly repeated.
func groupFiles(root string, managers []manager.Manager, ups []dep.Update) ([]managedFile, error) {
	var files []managedFile
	for _, u := range ups {
		files = append(files, managedFile{u.Dep.Manager, u.Dep.File})
		for _, m := range managers {
			fe, ok := m.(manager.FileEditor)
			if !ok || m.Name() != u.Dep.Manager {
				continue
			}
			edits, err := fe.FileEdits(root, u)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", u.Dep.File, u.Dep.Name, err)
			}
			for _, f := range slices.Sorted(maps.Keys(edits)) {
				files = append(files, managedFile{u.Dep.Manager, f})
			}
		}
	}
	return files, nil
}

// scopeInputsOf lists the inputs of one post-processing scope besides its manifests: the
// siblings Build reads that exist and the ScopeInputs of p, or the scope directory when p
// names none.
func scopeInputsOf(ctx context.Context, root, dir string, manifests []string, p PostProcessor) (paths, digests []string, err error) {
	l, ok := p.(ScopeInputLister)
	if !ok {
		return []string{dir}, nil, nil
	}
	names := p.Siblings()
	if sl, ok := p.(ScopeSiblingLister); ok {
		names = sl.ScopeSiblings(filepath.Join(root, filepath.FromSlash(dir)))
	}
	for _, n := range names {
		sib := path.Join(dir, n)
		switch _, err := os.Stat(filepath.Join(root, filepath.FromSlash(sib))); {
		case err == nil:
			paths = append(paths, sib)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, nil, err
		}
	}
	extra, digests, err := l.ScopeInputs(ctx, root, dir, manifests)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range extra {
		paths = append(paths, path.Clean(e))
	}
	return paths, digests, nil
}
