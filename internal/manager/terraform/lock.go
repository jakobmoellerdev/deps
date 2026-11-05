package terraform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// LockFile is the dependency lock file of a Terraform working directory.
const LockFile = ".terraform.lock.hcl"

// Locker is the terraform post-processor (engine.PostProcessor and LockfileUpdater). When
// deps changed the version constraint of a provider that the sibling .terraform.lock.hcl
// locks, or moves a locked provider within its constraint (a lockfile-only update), the lock
// entries of those providers are dropped and `terraform init -backend=false` (`tofu` for
// OpenTofu) runs on a temporary copy of the directory, so the lock gets the new version with
// its hashes, as Terraform records them. Other entries keep their versions. The working tree
// is never touched.
type Locker struct{}

// Scope returns the directory of a configuration file that has a lock file next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	dir := path.Dir(manifest)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), LockFile)); err != nil {
		return "", false
	}
	return dir, true
}

// Siblings lists the lock file.
func (Locker) Siblings() []string { return []string{LockFile} }

// Process regenerates the lock when a locked provider's constraint changed; otherwise (module
// or required_version updates, unlocked providers) it returns nothing.
func (Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return refresh(ctx, dir, manifests, files, nil)
}

// ProcessLocked is Process plus lockfile-only updates (rangeStrategy update-lockfile): each
// provider is locked to exactly its update's version. For the init run an override file
// (`deps_lock_override.tf`) pins the provider's constraint to `= <version>`; the lock then
// gets the constraints of the unchanged configuration back.
func (Locker) ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	pins := map[string]dep.Update{}
	for _, ups := range locks {
		for _, u := range ups {
			pins[lockName(u.Dep.Name)] = u
		}
	}
	return refresh(ctx, dir, manifests, files, pins)
}

// overrideFile pins lockfile-only updates during the init run.
const overrideFile = "deps_lock_override.tf"

// refresh regenerates the lock entries of the providers whose constraint changed and of the
// pinned ones (by lockName).
func refresh(ctx context.Context, dir string, manifests, files map[string][]byte, pins map[string]dep.Update) (map[string][]byte, error) {
	lock, ok := files[LockFile]
	if !ok {
		return nil, nil
	}
	changed, tofu, err := changedProviders(dir, manifests)
	if err != nil || len(changed) == 0 && len(pins) == 0 {
		return nil, err
	}
	lb, err := parseHCL(lock)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", LockFile, err)
	}
	var drop []patch.Edit
	constraints := map[string]*attr{} // old constraints of pinned entries; nil = none
	for _, blk := range lb.blocks {
		if blk.typ != "provider" || len(blk.labels) != 1 {
			continue
		}
		name := lockName(blk.labels[0])
		_, pinned := pins[name]
		if !changed[name] && !pinned {
			continue
		}
		if strings.HasPrefix(strings.ToLower(blk.labels[0]), OpenTofuRegistry+"/") {
			tofu = true
		}
		if pinned {
			constraints[name] = nil
			for i, a := range blk.body.attrs {
				if a.name == "constraints" && a.val.kind == kindString {
					constraints[name] = &blk.body.attrs[i]
				}
			}
		}
		drop = append(drop, patch.Edit{Start: blk.span.Start, End: blk.span.End, Old: string(lock[blk.span.Start:blk.span.End])})
	}
	for name, u := range pins {
		if _, ok := constraints[name]; !ok {
			return nil, fmt.Errorf("%s does not lock %s", LockFile, u.Dep.Name)
		}
	}
	if len(drop) == 0 {
		return nil, nil
	}
	stripped, err := patch.Apply(lock, drop)
	if err != nil {
		return nil, err
	}
	tool, bin, err := lockTool(tofu)
	if err != nil {
		return nil, err
	}
	content := make(map[string][]byte, len(manifests)+2)
	for rel, b := range manifests {
		content[filepath.FromSlash(rel)] = b
	}
	content[LockFile] = stripped
	if len(pins) > 0 {
		if content[overrideFile], err = pinOverride(dir, manifests, pins); err != nil {
			return nil, err
		}
	}
	out, err := initLock(ctx, dir, tool, bin, content)
	if err != nil {
		return nil, err
	}
	if len(pins) > 0 {
		if out, err = restoreConstraints(out, lock, constraints, pins); err != nil {
			return nil, err
		}
	}
	return map[string][]byte{LockFile: out}, nil
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `terraform init -upgrade
// -backend=false` on a mirror of dir moves every locked provider to the newest version its
// constraints admit, with fresh hashes; the configuration is unchanged. It returns the lock
// when it changed; without a lock file it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[LockFile]
	if !ok {
		return nil, nil
	}
	tofu := bytes.Contains(lock, []byte(`"`+OpenTofuRegistry+"/"))
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			tofu = tofu || isTofu(e.Name())
		}
	}
	tool, bin, err := lockTool(tofu)
	if err != nil {
		return nil, err
	}
	out, err := initLock(ctx, dir, tool, bin, map[string][]byte{LockFile: lock}, "-upgrade")
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{LockFile: out}, nil
}

// LockRoots implements manager.LockMaintainer: the .terraform.lock.hcl next to each
// configuration file, with the first configuration file of its directory as the manifest.
func (tfManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	seen := map[string]bool{}
	for _, f := range files {
		dir, ok := Locker{}.Scope(context.Background(), root, f)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, manager.LockRoot{Manifest: f, Lockfile: path.Join(dir, LockFile)})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// lockTool finds the CLI that refreshes a lock: tofu for OpenTofu, else terraform.
func lockTool(tofu bool) (tool, bin string, err error) {
	tool = "terraform"
	if tofu {
		tool = "tofu"
	}
	if bin, err = exec.LookPath(tool); err != nil {
		return "", "", fmt.Errorf("%s not found (needed to update %s)", tool, LockFile)
	}
	return tool, bin, nil
}

// initLock runs init (with args) on a mirror of dir holding content and returns the lock file
// it wrote.
func initLock(ctx context.Context, dir, tool, bin string, content map[string][]byte, args ...string) ([]byte, error) {
	work, cleanup, err := mirror(dir, localDepth(dir), content)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := runInit(ctx, bin, work, args...); err != nil {
		return nil, fmt.Errorf("%s init in %s: %w", tool, dir, err)
	}
	return os.ReadFile(filepath.Join(work, LockFile))
}

// lockName is the `namespace/type` of a provider address or lock entry (lowercase, without
// the registry host): lock entries are matched on it, so an OpenTofu lock
// (`registry.opentofu.org/hashicorp/aws`) and a configured default registry match a hostless
// source.
func lockName(addr string) string {
	addr = strings.ToLower(addr)
	if strings.Count(addr, "/") == 2 {
		_, addr, _ = strings.Cut(addr, "/")
	}
	return addr
}

// changedProviders returns the lockNames (as before the patch) of the providers whose version
// constraint or address (a replacement) differs between the files in dir and their patched
// content, and whether any of them is an OpenTofu file.
func changedProviders(dir string, manifests map[string][]byte) (map[string]bool, bool, error) {
	changed := map[string]bool{}
	tofu := false
	for rel, patched := range manifests {
		tofu = tofu || isTofu(rel)
		old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, false, err
		}
		before, err := New().Extract(rel, old, manager.Env{})
		if err != nil {
			return nil, false, err
		}
		after, err := New().Extract(rel, patched, manager.Env{})
		if err != nil {
			return nil, false, err
		}
		if len(before) != len(after) {
			return nil, false, fmt.Errorf("%s: patched file has %d deps, want %d", rel, len(after), len(before))
		}
		for i, d := range after {
			// A renamed provider (replacement) drops the old name's entry; init locks the new one.
			if d.Datasource == providerDatasource && (d.Version != before[i].Version || d.Name != before[i].Name || d.Source != before[i].Source) {
				changed[lockName(before[i].Name)] = true
			}
		}
	}
	return changed, tofu, nil
}

// pinOverride returns an override file setting the constraint of each pinned provider to
// `= <version>`, under the local name and source the directory's configuration gives it.
func pinOverride(dir string, manifests map[string][]byte, pins map[string]dep.Update) ([]byte, error) {
	local := localNames(dir, manifests)
	var b strings.Builder
	b.WriteString("terraform {\n  required_providers {\n")
	names := make([]string, 0, len(pins))
	for name := range pins {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		ln, ok := local[name]
		if !ok {
			return nil, fmt.Errorf("no required_providers entry for %s in %s", pins[name].Dep.Name, dir)
		}
		fmt.Fprintf(&b, "    %s = {\n      source  = %q\n      version = %q\n    }\n", ln.name, ln.source, "= "+pins[name].NewVersion)
	}
	b.WriteString("  }\n}\n")
	return []byte(b.String()), nil
}

type localName struct{ name, source string }

// localNames maps the lockNames of the required providers of the configuration in dir (the
// patched manifests replacing their files) to their local name and source as written.
func localNames(dir string, manifests map[string][]byte) map[string]localName {
	out := map[string]localName{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !isConfigFile(e.Name()) {
			continue
		}
		src, ok := manifests[e.Name()]
		if !ok {
			if src, err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
				continue
			}
		}
		b, err := parseConfig(e.Name(), src)
		if err != nil {
			continue
		}
		for _, blk := range b.blocks {
			if blk.typ != "terraform" {
				continue
			}
			for _, rp := range blk.body.blocks {
				if rp.typ != "required_providers" {
					continue
				}
				for _, a := range rp.body.attrs {
					source := "hashicorp/" + a.name
					for _, it := range a.val.obj {
						if it.name == "source" && it.val.kind == kindString {
							source = it.val.str
						}
					}
					if _, name, ok := providerAddress(source, DefaultRegistry); ok {
						out[lockName(name)] = localName{a.name, source}
					}
				}
			}
		}
	}
	return out
}

// restoreConstraints puts the old `constraints` of each pinned entry back into the regenerated
// lock (the override's `= <version>` is not part of the configuration) and checks that the
// entry locks the pinned version.
func restoreConstraints(out, old []byte, constraints map[string]*attr, pins map[string]dep.Update) ([]byte, error) {
	b, err := parseHCL(out)
	if err != nil {
		return nil, fmt.Errorf("regenerated %s: %w", LockFile, err)
	}
	var edits []patch.Edit
	for _, blk := range b.blocks {
		if blk.typ != "provider" || len(blk.labels) != 1 {
			continue
		}
		name := lockName(blk.labels[0])
		u, ok := pins[name]
		if !ok {
			continue
		}
		for _, a := range blk.body.attrs {
			switch {
			case a.name == "version" && a.val.str != u.NewVersion:
				return nil, fmt.Errorf("%s locked %s %s, want %s", LockFile, u.Dep.Name, a.val.str, u.NewVersion)
			case a.name == "constraints" && a.val.kind == kindString:
				edit := patch.Edit{Start: a.val.span.Start, End: a.val.span.End, Old: a.val.str}
				if c := constraints[name]; c != nil {
					edit.New = string(old[c.val.span.Start:c.val.span.End])
				} else {
					// No constraints before: drop the whole line.
					edit.Start, edit.End = lineBounds(out, a.off)
					edit.Old = string(out[edit.Start:edit.End])
				}
				edits = append(edits, edit)
			}
		}
	}
	return patch.Apply(out, edits)
}

// lineBounds returns the span of the line containing off, including its newline.
func lineBounds(src []byte, off int) (int, int) {
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	end := bytes.IndexByte(src[off:], '\n')
	if end < 0 {
		return start, len(src)
	}
	return start, off + end + 1
}

// ReadLocks implements manager.LockReader: the version the .terraform.lock.hcl next to a
// configuration file locks each of its provider deps to (dep.Dep.Locked). Lock entries are
// matched by lockName. It returns the lock files it could not parse.
func (tfManager) ReadLocks(root string, files [][]dep.Dep) []error {
	locks := map[string]map[string]string{} // dir -> lockName -> version
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Datasource != providerDatasource || d.Range == "" || d.Skip != "" {
				continue
			}
			dir := path.Dir(d.File)
			l, seen := locks[dir]
			if !seen {
				var err error
				if l, err = readLock(filepath.Join(root, filepath.FromSlash(dir), LockFile)); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", path.Join(dir, LockFile), err))
				}
				locks[dir] = l
			}
			d.Locked = l[lockName(d.Name)]
		}
	}
	return errs
}

// readLock returns the locked version per lockName of a lock file; nil when there is none.
func readLock(p string) (map[string]string, error) {
	src, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := parseHCL(src)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, blk := range b.blocks {
		if blk.typ != "provider" || len(blk.labels) != 1 {
			continue
		}
		for _, a := range blk.body.attrs {
			if a.name == "version" && a.val.kind == kindString {
				out[lockName(blk.labels[0])] = a.val.str
			}
		}
	}
	return out, nil
}

// localDepth returns how many directories above dir the local module sources of the
// configuration in dir (followed through the local modules) reach.
func localDepth(dir string) int {
	depth := 0
	seen := map[string]bool{}
	var walk func(rel string)
	walk = func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		up := 0
		for p := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
			if p != ".." {
				break
			}
			up++
		}
		depth = max(depth, up)
		for _, src := range localSources(filepath.Join(dir, rel)) {
			walk(filepath.Join(rel, filepath.FromSlash(src)))
		}
	}
	walk(".")
	return depth
}

// localSources returns the `./` and `../` module sources of the configuration in dir.
func localSources(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !isConfigFile(e.Name()) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		b, err := parseHCL(src)
		if err != nil {
			continue
		}
		for _, blk := range b.blocks {
			if blk.typ != "module" {
				continue
			}
			for _, a := range blk.body.attrs {
				if a.name == "source" && a.val.kind == kindString && (strings.HasPrefix(a.val.str, "./") || strings.HasPrefix(a.val.str, "../")) {
					out = append(out, a.val.str)
				}
			}
		}
	}
	return out
}

// mirror builds a temporary tree in which relative paths from the returned work directory
// resolve as they do from dir: dir's regular files are copied (content overrides some by
// name), its subdirectories are symlinked (its .terraform data directory is left out), and the
// other entries of each of the depth directories above dir are symlinked next to the copy.
func mirror(dir string, depth int, content map[string][]byte) (work string, cleanup func(), err error) {
	tmp, err := os.MkdirTemp("", "deps-terraform-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	dir = filepath.Clean(dir)
	// The path from the top mirrored ancestor down to dir.
	var names []string
	top := dir
	for range depth {
		parent := filepath.Dir(top)
		if parent == top {
			break
		}
		names = append([]string{filepath.Base(top)}, names...)
		top = parent
	}
	orig, mirrored := top, tmp
	for _, name := range names {
		if err := linkEntries(orig, mirrored, name); err != nil {
			return "", nil, err
		}
		orig, mirrored = filepath.Join(orig, name), filepath.Join(mirrored, name)
		if err := os.Mkdir(mirrored, 0o755); err != nil {
			return "", nil, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	for _, e := range entries {
		name, src, dst := e.Name(), filepath.Join(dir, e.Name()), filepath.Join(mirrored, e.Name())
		switch {
		case name == ".terraform":
		case content[name] != nil:
		case e.Type().IsRegular():
			b, err := os.ReadFile(src)
			if err != nil {
				return "", nil, err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return "", nil, err
			}
		default:
			if err := os.Symlink(src, dst); err != nil {
				return "", nil, err
			}
		}
	}
	for name, b := range content {
		if err := os.WriteFile(filepath.Join(mirrored, name), b, 0o644); err != nil {
			return "", nil, err
		}
	}
	return mirrored, cleanup, nil
}

// linkEntries symlinks every entry of dir except skip into mirrored.
func linkEntries(dir, mirrored, skip string) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	for _, e := range entries {
		if e.Name() == skip {
			continue
		}
		if err := os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(mirrored, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// runInit runs `<bin> init -backend=false [args]` in work, non-interactively.
func runInit(ctx context.Context, bin, work string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, append([]string{"init", "-backend=false", "-input=false", "-no-color"}, args...)...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1", "TF_DATA_DIR="+filepath.Join(work, ".terraform"))
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "terraform init", "bin", bin, "dir", work, "duration", time.Since(start), "err", err)
	if err != nil {
		if t := toolrun.Tail(out.Bytes(), 20); t != "" {
			return errors.New(t)
		}
		return err
	}
	return nil
}
