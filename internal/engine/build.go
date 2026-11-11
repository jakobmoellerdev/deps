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
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// FileChange is the new content of one file. Path is a slash path relative to the root;
// Old is nil when the file does not exist yet, New is nil when the change deletes it. A
// Gitlink change moves the submodule at Path: Old and New are its commits (hex) instead of
// file contents, and Mode is unused.
type FileChange struct {
	Path     string
	Mode     fs.FileMode
	Old, New []byte
	Gitlink  bool
}

// PostProcessor regenerates the files derived from manifests (go.sum, a lockfile) after deps
// patched them. Implementations work on copies and never touch the working tree.
type PostProcessor interface {
	// Scope returns the directory owning the derived files of manifest: a slash path relative
	// to root ("." for root itself) that contains manifest, e.g. the module directory of a
	// go.mod or the workspace root of a package.json. ok is false when the manifest has no
	// derived files. root is absolute; Scope may read files below it.
	Scope(_ context.Context, root, manifest string) (dir string, ok bool)
	// Siblings lists the names of the files in the scope directory that Process reads or
	// regenerates.
	Siblings() []string
	// Process regenerates the derived files of one scope. dir is the absolute scope directory,
	// manifests the patched content of every manifest of the scope by slash path relative to
	// dir and files the current content of every existing sibling by name. A result under a
	// manifest's path replaces that patched manifest, one under a sibling's name the sibling;
	// absent files are unchanged.
	Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error)
}

// SharedScope is implemented by post-processors registered for several managers whose
// manifests of one scope directory must be processed together, in one Process call: the
// bazel-module and bazelisk managers' module files and `.bazelversion` all feed one
// MODULE.bazel.lock. Without it each (manager, scope) is processed on its own.
type SharedScope interface {
	SharedScope()
}

// LockfileUpdater is implemented by post-processors that also apply lockfile-only updates
// (dep.Update.Lockfile): the manifest keeps its range and only the lockfile moves to the new
// version. ProcessLocked is Process plus those updates, by manifest path relative to dir;
// their manifests are in manifests unchanged.
type LockfileUpdater interface {
	ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error)
}

// UpdatePinner is implemented by post-processors that also need the manifest updates of a
// scope: cargo pins Cargo.lock to an update's NewVersion when the rewritten requirement still
// admits the locked version, which `cargo update` alone would keep. The engine calls
// ProcessUpdates instead of Process and ProcessLocked; ups are the manifest updates and locks
// the lockfile-only ones, both by manifest path relative to dir.
type UpdatePinner interface {
	ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error)
}

// LockMaintainer is implemented by post-processors that refresh a whole lockfile for a lock
// file maintenance update (dep.LockFileMaintenance): every locked dependency moves to the
// newest version its manifest admits, the manifest's constraints stay unchanged. files is the
// current content of every existing sibling by name, as for Process; the result holds the
// changed siblings, and may replace the manifest under its path relative to dir where it
// records locked versions itself (the `// indirect` requirements of go.mod).
type LockMaintainer interface {
	Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error)
}

// ManifestMaintainer is LockMaintainer for post-processors whose lockfiles are the manifests
// themselves (compiled requirements files): MaintainManifests gets the scope's manifests of
// the lock file maintenance update, by path relative to dir, besides the siblings, and
// returns the changed ones under the same paths. The engine prefers it over Maintain.
type ManifestMaintainer interface {
	MaintainManifests(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error)
}

// ScopeSiblingLister is implemented by post-processors whose siblings depend on the scope
// (the gradle.lockfile of every project of a Gradle build): ScopeSiblings replaces Siblings
// and lists the slash paths relative to dir, the absolute scope directory.
type ScopeSiblingLister interface {
	ScopeSiblings(dir string) []string
}

// TreeRegenerator is implemented by post-processors that also regenerate whole directories
// of a scope, too large to pass in as siblings (the vendor directory of a Go module). Trees
// lists them as slash paths relative to dir, the absolute scope directory; "." is the whole
// scope (a copier template update may change any file of its project). The result of
// Process or Maintain may hold a file below one of them under its slash path relative to dir:
// its new content, or nil when the file is deleted; only changed files need to be listed.
type TreeRegenerator interface {
	Trees(dir string) []string
}

// postSem bounds concurrent post-processor runs (go mod tidy, npm install) across all Build
// calls.
var postSem = make(chan struct{}, 4)

// fileBuild is the patched state of one file of a Build call.
type fileBuild struct {
	changes []FileChange // changes[0] is the file itself
	post    string       // manager whose post-processor derives files from it; "" = none
	locks   []dep.Update // lockfile-only updates, left to the post-processor
	ups     []dep.Update // the updates patched into the file, for an UpdatePinner
	// maintain marks a lock file maintenance update, left to the post-processor.
	maintain bool
	err      error // patch failure
	dropped  bool  // its post-processing scope failed
}

// postScope is one post-processor run: every patched manifest of one manager sharing a scope.
type postScope struct {
	manager, dir string
	files        []int // indices into the Build files, in path order
	changes      []FileChange
	err          error
}

// Build re-reads the files touched by updates, patches them with the byte edits of each
// update's manager (looked up by dep.Dep.Manager in managers) and runs the post-processors
// registered by manager name in post (nil = none, as for plan). Patched manifests sharing a
// post-processor scope (a Go module, a workspace lockfile) are post-processed together in one
// run. Edits of other files (manager.FileEditor: import paths of a Go module) are applied
// with the update and join the post-processing scope of their own path; the update's files
// stand or fall together. A failing file, or every file of a failing scope together with its
// regenerated siblings, is dropped and its error returned; the remaining changes are returned
// sorted by Path.
func Build(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, post map[string]PostProcessor) ([]FileChange, []error) {
	byName := make(map[string]manager.Manager, len(managers))
	for _, m := range managers {
		byName[m.Name()] = m
	}
	byFile := map[string][]dep.Update{}
	var files []string
	addFile := func(f string) {
		if _, ok := byFile[f]; !ok {
			files = append(files, f)
			byFile[f] = nil
		}
	}
	for _, u := range updates {
		addFile(u.Dep.File)
		byFile[u.Dep.File] = append(byFile[u.Dep.File], u)
	}
	others, linked, preErr := fileEdits(root, byName, updates)
	for f := range others {
		addFile(f)
	}
	slices.Sort(files)

	builds := make([]fileBuild, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		if err := preErr[f]; err != nil {
			builds[i] = fileBuild{err: err}
			continue
		}
		wg.Go(func() { builds[i] = buildFile(ctx, root, f, byName, byFile[f], others[f], post) })
	}
	wg.Wait()
	dropLinked(files, builds, linked)

	// Post-processors read the other files of the group as patched (cargo: the rust-toolchain
	// file of a grouped toolchain update).
	ctx = toolrun.WithPatched(ctx, patchedFiles(root, files, builds))
	scopes := groupScopes(ctx, root, files, builds, post)
	for _, s := range scopes {
		wg.Go(func() { s.run(ctx, root, files, builds, post[s.manager]) })
	}
	wg.Wait()

	// Errors in the order of their first file; a scope's error follows its files' own errors.
	type keyed struct {
		file int
		err  error
	}
	var errKeyed []keyed
	var out []FileChange
	for i, b := range builds {
		if b.err != nil {
			errKeyed = append(errKeyed, keyed{i, &BuildError{Files: []string{files[i]}, Err: b.err}})
		} else if !b.dropped {
			for _, c := range b.changes {
				if !bytes.Equal(c.Old, c.New) { // a manifest with only lockfile-only updates
					out = append(out, c)
				}
			}
		}
	}
	for _, s := range scopes {
		if s.err != nil {
			names := make([]string, len(s.files))
			for j, i := range s.files {
				names[j] = files[i]
			}
			errKeyed = append(errKeyed, keyed{s.files[0], &BuildError{Files: names, Err: s.err}})
		} else {
			out = append(out, s.changes...)
		}
	}
	slices.SortFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	slices.SortStableFunc(errKeyed, func(a, b keyed) int { return a.file - b.file })
	var errList []error // nil without errors
	if len(errKeyed) > 0 {
		errList = make([]error, len(errKeyed))
		for i, e := range errKeyed {
			errList[i] = e.err
		}
	}
	return out, errList
}

// patchedFiles returns the new content of every file of builds that changed, by absolute
// path; nil when none did.
func patchedFiles(root string, files []string, builds []fileBuild) map[string][]byte {
	var out map[string][]byte
	for i, b := range builds {
		if b.err != nil || b.dropped || bytes.Equal(b.changes[0].Old, b.changes[0].New) {
			continue
		}
		if out == nil {
			out = map[string][]byte{}
		}
		out[filepath.Join(root, filepath.FromSlash(files[i]))] = b.changes[0].New
	}
	return out
}

// otherEdits are the edits of one file that come from updates of other files' deps
// (manager.FileEditor), with the manager that produced them.
type otherEdits struct {
	manager string
	edits   []patch.Edit
}

// fileEdits collects the manager.FileEditor edits of updates by file. linked lists, per
// update with such edits, its dep's file followed by the other files; preErr holds the
// failures by dep file.
func fileEdits(root string, managers map[string]manager.Manager, updates []dep.Update) (map[string]otherEdits, [][]string, map[string]error) {
	var others map[string]otherEdits
	var linked [][]string
	var preErr map[string]error
	for _, u := range updates {
		fe, ok := managers[u.Dep.Manager].(manager.FileEditor)
		if !ok {
			continue
		}
		m, err := fe.FileEdits(root, u)
		if err != nil {
			if preErr == nil {
				preErr = map[string]error{}
			}
			preErr[u.Dep.File] = fmt.Errorf("%s: %w", u.Dep.Name, err)
			continue
		}
		if len(m) == 0 {
			continue
		}
		if others == nil {
			others = map[string]otherEdits{}
		}
		set := []string{u.Dep.File}
		for f, e := range m {
			o := others[f]
			o.manager = u.Dep.Manager
			o.edits = append(o.edits, e...)
			others[f] = o
			set = append(set, f)
		}
		linked = append(linked, set)
	}
	return others, linked, preErr
}

// dropLinked drops every file of a linked set (fileEdits) in which some file failed; the
// failing file reports the error.
func dropLinked(files []string, builds []fileBuild, linked [][]string) {
	for _, set := range linked {
		idx := make([]int, len(set))
		failed := false
		for j, f := range set {
			idx[j], _ = slices.BinarySearch(files, f)
			failed = failed || builds[idx[j]].err != nil
		}
		if !failed {
			continue
		}
		for _, i := range idx {
			builds[i].dropped = builds[i].err == nil
		}
	}
}

func buildFile(ctx context.Context, root, file string, managers map[string]manager.Manager, ups []dep.Update, other otherEdits, post map[string]PostProcessor) fileBuild {
	edits := make([]patch.Edit, 0, len(ups)+1+len(other.edits))
	var b fileBuild
	setPost := func(name string) error {
		if post[name] != nil && name != b.post {
			switch {
			case b.post == "":
				b.post = name
			case !samePost(post[b.post], post[name]):
				// Managers sharing a file share its post-processor (pep621 and poetry in
				// pyproject.toml); gomod and npm manifests are disjoint.
				return fmt.Errorf("updates of post-processed managers %s and %s", b.post, name)
			}
		}
		return nil
	}
	for _, u := range ups {
		if u.Lockfile || u.Maintenance() {
			if err := setPost(u.Dep.Manager); err != nil {
				return fileBuild{err: err}
			}
			if u.Maintenance() {
				b.maintain = true
			} else {
				b.locks = append(b.locks, u)
			}
			continue
		}
		m := managers[u.Dep.Manager]
		if m == nil {
			return fileBuild{err: fmt.Errorf("unknown manager %q", u.Dep.Manager)}
		}
		e, err := m.Edits(u)
		if err != nil {
			return fileBuild{err: fmt.Errorf("%s: %w", u.Dep.Name, err)}
		}
		b.ups = append(b.ups, u)
		edits = append(edits, e...)
		if err := setPost(u.Dep.Manager); err != nil {
			return fileBuild{err: err}
		}
	}
	if len(other.edits) > 0 {
		edits = append(edits, other.edits...)
		if err := setPost(other.manager); err != nil {
			return fileBuild{err: err}
		}
	}
	abs := filepath.Join(root, filepath.FromSlash(file))
	st, err := os.Stat(abs)
	if err != nil {
		return fileBuild{err: err}
	}
	old, err := os.ReadFile(abs)
	if err != nil {
		return fileBuild{err: err}
	}
	patched, err := patch.Apply(old, edits)
	if err != nil {
		return fileBuild{err: err}
	}
	slog.DebugContext(ctx, "file patched", "file", file, "edits", len(edits))
	b.changes = []FileChange{{Path: file, Mode: st.Mode().Perm(), Old: old, New: patched}}
	for _, u := range b.ups {
		if d := u.Dep; d.Gitlink != "" && u.NewDigest != "" && u.NewDigest != d.Digest {
			b.changes = append(b.changes, FileChange{Path: d.Gitlink, Gitlink: true, Old: []byte(d.Digest), New: []byte(u.NewDigest)})
		}
	}
	return b
}

// groupScopes groups the patched files by (post-processing manager, scope), in path order of
// their first file; managers sharing a SharedScope post-processor share the scope. Files whose
// post-processor reports no scope are left alone.
func groupScopes(ctx context.Context, root string, files []string, builds []fileBuild, post map[string]PostProcessor) []*postScope {
	var scopes []*postScope
	index := map[[2]string]*postScope{}
	for i, b := range builds {
		if b.err != nil || b.dropped || b.post == "" {
			continue
		}
		dir, ok := post[b.post].Scope(ctx, root, files[i])
		if !ok {
			switch {
			case b.maintain:
				builds[i].err = errors.New("lock file maintenance without a lockfile")
				continue
			case len(b.locks) > 0:
				builds[i].err = fmt.Errorf("lockfile-only update of %s without a lockfile", b.locks[0].Dep.Name)
				continue
			}
			slog.DebugContext(ctx, "no post-processing scope", "file", files[i], "manager", b.post)
			continue
		}
		key := [2]string{b.post, dir}
		s := index[key]
		if _, shared := post[b.post].(SharedScope); s == nil && shared {
			for _, o := range scopes {
				if o.dir == dir && samePost(post[o.manager], post[b.post]) {
					s = o
					break
				}
			}
		}
		if s == nil {
			s = &postScope{manager: b.post, dir: dir}
			scopes = append(scopes, s)
		}
		index[key] = s
		s.files = append(s.files, i)
	}
	return scopes
}

// sibling is the current state of a file in a scope directory.
type sibling struct {
	path    string // slash path relative to the root
	mode    fs.FileMode
	content []byte // nil when the file does not exist
}

// run post-processes the scope with p: patched manifests are replaced by their results and a
// change is recorded for every sibling whose content changed (mode 0644 when it is new). On
// failure every file of the scope is dropped.
func (s *postScope) run(ctx context.Context, root string, files []string, builds []fileBuild, p PostProcessor) {
	if s.changes, s.err = s.process(ctx, root, files, builds, p); s.err != nil {
		for _, i := range s.files {
			builds[i].dropped = true
		}
	}
}

func (s *postScope) process(ctx context.Context, root string, files []string, builds []fileBuild, p PostProcessor) ([]FileChange, error) {
	manifests := make(map[string][]byte, len(s.files))
	rels := make([]string, len(s.files))
	var locks, ups map[string][]dep.Update
	maintain := false
	for j, i := range s.files {
		rel := files[i]
		if s.dir != "." {
			var ok bool
			if rel, ok = strings.CutPrefix(rel, s.dir+"/"); !ok {
				return nil, fmt.Errorf("post-processing scope %s does not contain %s", s.dir, files[i])
			}
		}
		rels[j] = rel
		manifests[rel] = builds[i].changes[0].New
		maintain = maintain || builds[i].maintain
		if len(builds[i].locks) > 0 {
			if locks == nil {
				locks = map[string][]dep.Update{}
			}
			locks[rel] = builds[i].locks
		}
		if len(builds[i].ups) > 0 {
			if ups == nil {
				ups = map[string][]dep.Update{}
			}
			ups[rel] = builds[i].ups
		}
	}
	lu, _ := p.(LockfileUpdater)
	up, _ := p.(UpdatePinner)
	if locks != nil && lu == nil && up == nil {
		return nil, fmt.Errorf("the %s post-processor cannot apply lockfile-only updates", s.manager)
	}
	lm, _ := p.(LockMaintainer)
	mm, _ := p.(ManifestMaintainer)
	switch {
	case maintain && lm == nil && mm == nil:
		return nil, fmt.Errorf("the %s post-processor cannot maintain lockfiles", s.manager)
	case maintain && (locks != nil || slices.ContainsFunc(s.files, func(i int) bool { c := builds[i].changes[0]; return !bytes.Equal(c.Old, c.New) })):
		return nil, errors.New("lock file maintenance cannot be combined with other updates of its lockfile")
	}
	abs := filepath.Join(root, filepath.FromSlash(s.dir))
	names := p.Siblings()
	if l, ok := p.(ScopeSiblingLister); ok {
		names = l.ScopeSiblings(abs)
	}
	sibs := make([]sibling, len(names))
	contents := make(map[string][]byte, len(names))
	for j, n := range names {
		sb := sibling{path: path.Join(s.dir, n), mode: 0o644}
		abs := filepath.Join(root, filepath.FromSlash(sb.path))
		b, err := os.ReadFile(abs)
		switch {
		case err == nil:
			sb.content = b
			contents[n] = b
			if st, err := os.Stat(abs); err == nil {
				sb.mode = st.Mode().Perm()
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		sibs[j] = sb
	}

	select {
	case postSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	start := time.Now()
	var out map[string][]byte
	var err error
	switch {
	case maintain && mm != nil:
		out, err = mm.MaintainManifests(ctx, abs, manifests, contents)
	case maintain:
		out, err = lm.Maintain(ctx, abs, contents)
	case up != nil:
		out, err = up.ProcessUpdates(ctx, abs, manifests, contents, ups, locks)
	case locks != nil:
		out, err = lu.ProcessLocked(ctx, abs, manifests, contents, locks)
	default:
		out, err = p.Process(ctx, abs, manifests, contents)
	}
	<-postSem
	if err != nil {
		return nil, err
	}
	for j, i := range s.files {
		if b, ok := out[rels[j]]; ok {
			builds[i].changes[0].New = b
		}
	}
	var changes []FileChange
	var changed []string
	for j, n := range names {
		b, ok := out[n]
		sb := sibs[j]
		if !ok || bytes.Equal(b, sb.content) {
			continue
		}
		changed = append(changed, n)
		changes = append(changes, FileChange{Path: sb.path, Mode: sb.mode, Old: sb.content, New: b})
	}
	tree, err := treeChanges(root, s.dir, abs, p, out, rels, names)
	if err != nil {
		return nil, err
	}
	for _, c := range tree {
		changed = append(changed, c.Path)
	}
	changes = append(changes, tree...)
	slog.DebugContext(ctx, "post-processed", "scope", s.dir, "manager", s.manager, "manifests", rels, "changed", changed, "duration", time.Since(start))
	return changes, nil
}

// treeChanges turns the results of p below its TreeRegenerator trees into changes against
// the files in root; the old content is read for those files only. Results that are neither
// a manifest (rels), a sibling (names) nor below a tree are ignored. A regenerated tree
// holds thousands of files (a Go module's vendor directory) and a scope hundreds of
// manifests, so the manifests and siblings are looked up in a set built once.
func treeChanges(root, dir, abs string, p PostProcessor, out map[string][]byte, rels, names []string) ([]FileChange, error) {
	tr, ok := p.(TreeRegenerator)
	if !ok {
		return nil, nil
	}
	trees := tr.Trees(abs)
	if len(trees) == 0 {
		return nil, nil
	}
	whole := slices.Contains(trees, ".") // the whole scope: every result is below a tree
	scope := make(map[string]bool, len(rels)+len(names))
	for _, n := range rels {
		scope[n] = true
	}
	for _, n := range names {
		scope[n] = true
	}
	var changes []FileChange
	for rel, b := range out {
		if scope[rel] || !whole && !slices.ContainsFunc(trees, func(t string) bool { return strings.HasPrefix(rel, t+"/") }) {
			continue
		}
		c := FileChange{Path: path.Join(dir, rel), Mode: 0o644, New: b}
		file := filepath.Join(root, filepath.FromSlash(c.Path))
		switch st, err := os.Stat(file); {
		case err == nil:
			c.Mode = st.Mode().Perm()
			if c.Old, err = os.ReadFile(file); err != nil {
				return nil, err
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		if c.Old == nil && c.New == nil || c.Old != nil && c.New != nil && bytes.Equal(c.Old, c.New) {
			continue
		}
		changes = append(changes, c)
	}
	slices.SortFunc(changes, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return changes, nil
}

// samePost reports whether a and b are the same post-processor value (both of one
// comparable type and equal).
func samePost(a, b PostProcessor) bool {
	t := reflect.TypeOf(a)
	return t == reflect.TypeOf(b) && t.Comparable() && a == b
}
