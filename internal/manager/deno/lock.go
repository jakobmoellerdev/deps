package deno

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/jsr"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// lockFile is the default lockfile, next to the configuration file of a scope.
const lockFile = "deno.lock"

// configNames are the configuration file names, in the order deno prefers them.
var configNames = []string{"deno.json", "deno.jsonc"}

// configHead is the part of a configuration file that decides where its lockfile is.
type configHead struct {
	Lock      jsontext.Value `json:"lock"`
	Workspace jsontext.Value `json:"workspace"`
}

// parseHead parses the configuration file src (JSON with comments).
func parseHead(src []byte) (configHead, error) {
	var h configHead
	err := json.Unmarshal(jsonc.Strip(src), &h, jsontext.AllowDuplicateNames(true))
	return h, err
}

// readHead reads the configuration file at the absolute path p; ok is false when it cannot
// be read.
func readHead(p string) (configHead, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return configHead{}, false
	}
	h, err := parseHead(b)
	return h, err == nil
}

// lockPath returns the lockfile of the configuration (slash path relative to its directory):
// deno.lock, or the `lock` string or `lock.path`; "" for `lock: false`.
func (h configHead) lockPath() string {
	switch h.Lock.Kind() {
	case 'f':
		return ""
	case '"':
		var s string
		if json.Unmarshal(h.Lock, &s) == nil && s != "" {
			return path.Clean(filepath.ToSlash(s))
		}
	case '{':
		var o struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(h.Lock, &o) == nil && o.Path != "" {
			return path.Clean(filepath.ToSlash(o.Path))
		}
	}
	return lockFile
}

// members returns the `workspace` member patterns (an array, or `workspace.members`), as
// slash paths relative to the configuration's directory.
func (h configHead) members() []string {
	var ms []string
	switch h.Workspace.Kind() {
	case '[':
		_ = json.Unmarshal(h.Workspace, &ms)
	case '{':
		var o struct {
			Members []string `json:"members"`
		}
		_ = json.Unmarshal(h.Workspace, &o)
		ms = o.Members
	}
	for i, m := range ms {
		ms[i] = path.Clean(strings.TrimPrefix(filepath.ToSlash(m), "./"))
	}
	return ms
}

// scope is where the lockfile of a configuration file is: the directory of the workspace
// root that lists it as a member (or its own), that directory's configuration file and its
// lockfile, all slash paths relative to the scanned root.
type scope struct {
	dir, config, lock string
}

// scopeOf returns the scope of the configuration file m (slash path relative to root): the
// nearest ancestor directory whose configuration lists m's directory as a `workspace` member,
// else m's own directory. ok is false when the scope has no lockfile (`lock: false`, a lock
// path outside its directory, or none on disk).
func scopeOf(root, m string) (scope, bool) {
	s := scope{dir: path.Dir(m), config: m}
	h, found := readHead(filepath.Join(root, filepath.FromSlash(m)))
	for a := s.dir; a != "."; {
		a = path.Dir(a)
		rel := path.Dir(m)
		if a != "." {
			rel = strings.TrimPrefix(rel, a+"/")
		}
		if c, hc, ok := configIn(root, a); ok && memberOf(hc.members(), rel) {
			s, h, found = scope{dir: a, config: c}, hc, true
			break
		}
	}
	if !found {
		return s, false
	}
	lock := h.lockPath()
	if lock == "" || !filepath.IsLocal(lock) {
		return s, false
	}
	s.lock = path.Join(s.dir, lock)
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(s.lock)))
	return s, err == nil && st.Mode().IsRegular()
}

// configIn returns the configuration file of the directory dir (slash path relative to root)
// and its head; ok is false without a readable one.
func configIn(root, dir string) (string, configHead, bool) {
	for _, n := range configNames {
		c := path.Join(dir, n)
		if h, ok := readHead(filepath.Join(root, filepath.FromSlash(c))); ok {
			return c, h, true
		}
	}
	return "", configHead{}, false
}

// memberOf reports whether the directory rel matches one of the workspace member patterns.
func memberOf(patterns []string, rel string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// lockable reports whether d is locked by deno.lock: a versioned `jsr:` or `npm:` specifier of
// the configuration (tasks are run, not installed).
func lockable(d dep.Dep) bool {
	return d.Skip == "" && d.Range != "" && (d.Datasource == jsr.Name || d.Datasource == "npm") &&
		d.DepType != DepTypeTasks && d.DepType != DepTypeTaskCommand
}

// lockContent is what deps reads of a deno.lock: its `specifiers` (version 4 and 5; version 3
// keeps them in `packages.specifiers` with whole specifiers as values) mapped to the locked
// versions without the peer suffix of npm packages (`7.1.3_pkgname@4.0.3`), and the
// requirements of the configuration files the `workspace` section lists.
type lockContent struct {
	specs  map[string]string
	direct map[string]bool
}

// parseLock parses a deno.lock; one without `version` is none.
func parseLock(b []byte) (*lockContent, error) {
	var l struct {
		Version    string            `json:"version"`
		Specifiers map[string]string `json:"specifiers"`
		Packages   struct {
			Specifiers map[string]string `json:"specifiers"`
		} `json:"packages"`
		Workspace any `json:"workspace"`
	}
	if err := json.Unmarshal(b, &l, jsontext.AllowDuplicateNames(true)); err != nil {
		return nil, err
	}
	if l.Version == "" {
		return nil, errors.New("not a deno.lock (no version)")
	}
	c := &lockContent{specs: make(map[string]string, len(l.Specifiers)+len(l.Packages.Specifiers)), direct: map[string]bool{}}
	for k, v := range l.Packages.Specifiers {
		if sp, ok := ParseSpecifier(v); ok {
			v = sp.Version
		}
		c.specs[k], _, _ = strings.Cut(v, "_")
	}
	for k, v := range l.Specifiers {
		c.specs[k], _, _ = strings.Cut(v, "_")
	}
	collectStrings(l.Workspace, c.direct)
	return c, nil
}

// locked returns the version the lock locks d to: its own specifier's, else, as deno writes
// requirements normalized (`^1.0.0` as `1`), the newest version locked for a requirement of
// the package that d's range admits, those of the configuration files (`workspace`) first;
// "" when none is.
func (c *lockContent) locked(d dep.Dep) string {
	if v, ok := c.specs[d.Datasource+":"+d.Name+"@"+d.Range]; ok && semverScheme.Valid(v) {
		return v
	}
	best, bestDirect := "", false
	for k, v := range c.specs {
		sp, ok := ParseSpecifier(k)
		if !ok || sp.Datasource != d.Datasource || sp.Name != d.Name || !semverScheme.Valid(v) || !npm.InRange(d, v) {
			continue
		}
		direct := c.direct[k]
		if best == "" || direct && !bestDirect || direct == bestDirect && semverScheme.Compare(v, best) > 0 {
			best, bestDirect = v, direct
		}
	}
	return best
}

// ReadLocks records the version the deno.lock of each configuration file's scope (scopeOf)
// locks its `jsr:` and `npm:` specifiers to (dep.Dep.Locked). An unreadable lockfile is
// reported once and its deps are bumped (LockUnread).
func (denoManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		lock *lockContent
		err  error
	}
	locks := map[string]*lockState{}
	var errs []error
	for _, f := range files {
		file := ""
		for j := range f {
			d := &f[j]
			if !lockable(*d) {
				continue
			}
			if file == "" {
				file = "-"
				if s, ok := scopeOf(root, d.File); ok {
					file = s.lock
				}
			}
			if file == "-" {
				break
			}
			st := locks[file]
			if st == nil {
				st = &lockState{}
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
				if err == nil {
					st.lock, err = parseLock(b)
				}
				if err != nil {
					st.err = fmt.Errorf("%s: %w", file, err)
					errs = append(errs, st.err)
				}
				locks[file] = st
			}
			if st.err != nil {
				d.LockUnread = true
				continue
			}
			d.Locked = st.lock.locked(*d)
		}
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the lockfile of each scope (scopeOf), once,
// with the scope's configuration file.
func (denoManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	seen := map[string]bool{}
	for _, f := range files {
		s, ok := scopeOf(root, f)
		if !ok || seen[s.lock] {
			continue
		}
		seen[s.lock] = true
		out = append(out, manager.LockRoot{Manifest: s.config, Lockfile: s.lock})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Locker is the deno post-processor (engine.PostProcessor, UpdatePinner and LockMaintainer):
// it updates the deno.lock of a patched configuration with `deno install --lockfile-only` in a
// temporary copy of the scope. The working tree is never touched.
type Locker struct{}

// Siblings lists deno.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// ScopeSiblings implements engine.ScopeSiblingLister: the lockfile the configuration file of
// the absolute scope directory dir names (`lock`), deno.lock by default.
func (Locker) ScopeSiblings(dir string) []string {
	if h, ok := rootHead(dir, nil); ok {
		if p := h.lockPath(); p != "" && filepath.IsLocal(p) {
			return []string{p}
		}
	}
	return []string{lockFile}
}

// Scope returns the directory of the deno.lock that locks the configuration file manifest
// (scopeOf).
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	s, ok := scopeOf(root, manifest)
	return s.dir, ok
}

// Process updates the lockfile for the patched configuration files.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated `jsr:`/`npm:` package, manifest updates (ups) and
// lockfile-only ones (locks) alike, to exactly the update's version. In a copy of the scope,
// each specifier of an updated package whose range admits the new version is pinned to it and
// `deno install --lockfile-only` locks it (and what it needs). Since deno.lock records the
// requirement as written, the pinned specifiers are then renamed back to the ranges of the
// configuration files deps writes, the lockfile's `workspace` section is dropped and deno runs
// once more on the final configuration, which rewrites the lockfile as deno writes it without
// resolving anything anew. It returns the lockfile when it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	lockName := scopeLock(dir, manifests)
	orig, ok := files[lockName]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockName, dir)
		}
		return nil, nil
	}
	final, err := scopeConfigs(dir, manifests)
	if err != nil {
		return nil, err
	}
	pinned := maps.Clone(final)
	renames := map[string][]string{}
	for _, byManifest := range []map[string][]dep.Update{ups, locks} {
		for _, rel := range slices.Sorted(maps.Keys(byManifest)) {
			src, ok := pinned[rel]
			if !ok {
				continue
			}
			if pinned[rel], err = pin(rel, src, byManifest[rel], renames); err != nil {
				return nil, err
			}
		}
	}
	lock := orig
	if len(renames) > 0 {
		if lock, err = runDeno(ctx, dir, pinned, lockName, lock); err != nil {
			return nil, err
		}
		if lock, err = rename(lock, renames); err != nil {
			return nil, fmt.Errorf("%s: %w", lockName, err)
		}
	}
	if lock, err = runDeno(ctx, dir, final, lockName, lock); err != nil {
		return nil, err
	}
	if bytes.Equal(lock, orig) {
		return nil, nil
	}
	return map[string][]byte{lockName: lock}, nil
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): deno.lock is regenerated
// from the configuration files with `deno install --lockfile-only`, which moves every locked
// package, transitive ones included, to the newest version the ranges admit. The `remote`
// hashes of URL modules are kept: the configuration does not name the modules the code
// imports by URL. It returns the lockfile when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lockName := scopeLock(dir, nil)
	orig, ok := files[lockName]
	if !ok {
		return nil, nil
	}
	configs, err := scopeConfigs(dir, nil)
	if err != nil {
		return nil, err
	}
	var l struct {
		Version string         `json:"version"`
		Remote  jsontext.Value `json:"remote,omitzero"`
	}
	if err := json.Unmarshal(orig, &l, jsontext.AllowDuplicateNames(true)); err != nil {
		return nil, fmt.Errorf("%s: %w", lockName, err)
	}
	fresh, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	lock, err := runDeno(ctx, dir, configs, lockName, fresh)
	if err != nil || bytes.Equal(lock, orig) {
		return nil, err
	}
	return map[string][]byte{lockName: lock}, nil
}

// rootHead returns the head of the configuration file of the absolute scope directory dir,
// from patched (by slash path relative to dir) when there, else from disk.
func rootHead(dir string, patched map[string][]byte) (configHead, bool) {
	name, src, ok := rootConfig(dir, patched)
	if !ok {
		return configHead{}, false
	}
	h, err := parseHead(src)
	if err != nil {
		slog.Debug("deno config unreadable", "file", filepath.Join(dir, name), "err", err)
	}
	return h, err == nil
}

// rootConfig returns the name and content of the configuration file of the absolute scope
// directory dir, from patched when there, else from disk.
func rootConfig(dir string, patched map[string][]byte) (string, []byte, bool) {
	for _, n := range configNames {
		if src, ok := patched[n]; ok {
			return n, src, true
		}
		if src, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			return n, src, true
		}
	}
	return "", nil, false
}

// scopeLock returns the lockfile of the absolute scope directory dir (relative to it).
func scopeLock(dir string, patched map[string][]byte) string {
	if h, ok := rootHead(dir, patched); ok {
		if p := h.lockPath(); p != "" {
			return p
		}
	}
	return lockFile
}

// scopeConfigs returns the configuration files of the absolute scope directory dir by slash
// path relative to it: its own and those of its `workspace` members, from patched when
// there, else from disk. Each is written into the copy, so deno reads every member as a file
// of the copy, never through a symlink into the checkout.
func scopeConfigs(dir string, patched map[string][]byte) (map[string][]byte, error) {
	out := maps.Clone(patched)
	if out == nil {
		out = map[string][]byte{}
	}
	name, src, ok := rootConfig(dir, patched)
	if !ok {
		return nil, fmt.Errorf("no deno.json in %s", dir)
	}
	out[name] = src
	h, _ := parseHead(src) // a configuration deno cannot read lists no members; deno reports it
	for _, m := range h.members() {
		dirs, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(m)))
		for _, d := range dirs {
			rel, err := filepath.Rel(dir, d)
			if err != nil {
				return nil, err
			}
			for _, n := range configNames {
				p := path.Join(filepath.ToSlash(rel), n)
				if _, ok := out[p]; ok {
					break
				}
				if b, err := os.ReadFile(filepath.Join(d, n)); err == nil {
					out[p] = b
					break
				}
			}
		}
	}
	return out, nil
}

// pin returns the configuration file src (rel) with every specifier of a package ups update
// whose range admits the update's version set to exactly that version, and records in
// renames the pinned specifier (`jsr:@std/path@1.1.0`) and the specifiers as written in the
// configuration deps writes (`jsr:@std/path@^1.1.0`).
func pin(rel string, src []byte, ups []dep.Update, renames map[string][]string) ([]byte, error) {
	ds, err := denoManager{}.Extract(rel, src, manager.Env{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	var edits []patch.Edit
	for _, u := range ups {
		if !lockable(u.Dep) {
			continue
		}
		for _, d := range ds {
			if !lockable(d) || d.Datasource != u.Dep.Datasource || d.Name != u.Dep.Name || !npm.InRange(d, u.NewVersion) {
				continue
			}
			if slices.ContainsFunc(edits, func(e patch.Edit) bool { return e.Start == d.RefSpan.Start }) {
				continue
			}
			edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: u.NewVersion})
			key, want := d.Datasource+":"+d.Name+"@"+u.NewVersion, d.Datasource+":"+d.Name+"@"+d.Range
			if !slices.Contains(renames[key], want) {
				renames[key] = append(renames[key], want)
			}
		}
	}
	slices.SortFunc(edits, func(a, b patch.Edit) int { return b.Start - a.Start })
	out := slices.Clone(src)
	for _, e := range edits {
		out = slices.Concat(out[:e.Start], []byte(e.New), out[e.End:])
	}
	return out, nil
}

// rename returns the lockfile lock with each pinned specifier of renames replaced by the
// specifiers as written (same locked version) and the `workspace` section dropped, so deno
// rewrites the lockfile in its own form on the next run. A requirement of the configuration
// files (listed in `workspace`) of the same package that admits the pinned version is dropped
// too: it may be the same requirement written otherwise (`1` for `^1.0.0`), and deno resolves
// it again against the new version.
func rename(lock []byte, renames map[string][]string) ([]byte, error) {
	var top map[string]jsontext.Value
	if err := json.Unmarshal(lock, &top, jsontext.AllowDuplicateNames(true)); err != nil {
		return nil, err
	}
	specs := map[string]string{}
	if v, ok := top["specifiers"]; ok {
		if err := json.Unmarshal(v, &specs); err != nil {
			return nil, err
		}
	}
	direct := map[string]bool{}
	var ws any
	if json.Unmarshal(top["workspace"], &ws) == nil {
		collectStrings(ws, direct)
	}
	for _, key := range slices.Sorted(maps.Keys(renames)) {
		v, ok := specs[key]
		if !ok {
			continue
		}
		delete(specs, key)
		sp, _ := ParseSpecifier(key)
		for k := range specs {
			ks, ok := ParseSpecifier(k)
			if ok && direct[k] && ks.Datasource == sp.Datasource && ks.Name == sp.Name && npm.InRange(dep.Dep{Range: ks.Version}, v) {
				delete(specs, k)
			}
		}
		for _, want := range renames[key] {
			specs[want] = v
		}
	}
	b, err := json.Marshal(specs, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	top["specifiers"] = b
	delete(top, "workspace")
	return json.Marshal(top, json.Deterministic(true))
}

// collectStrings adds every string in the decoded JSON value v to set.
func collectStrings(v any, set map[string]bool) {
	switch v := v.(type) {
	case string:
		set[v] = true
	case []any:
		for _, e := range v {
			collectStrings(e, set)
		}
	case map[string]any:
		for _, e := range v {
			collectStrings(e, set)
		}
	}
}

// skipEntry leaves deno's install output (node_modules, the vendor directory of the scope)
// and the repository metadata out of the copy, so deno never writes into the checkout.
func skipEntry(rel string, d fs.DirEntry) bool {
	return d.Name() == "node_modules" || d.Name() == ".git" || rel == "vendor"
}

// runDeno runs `deno install --lockfile-only --frozen=false` in a temporary mirror of the
// absolute scope directory dir (toolrun.Mirror) holding the configuration files configs and
// lock as lockName, and returns the lockfile it leaves; a failure carries the tail of deno's
// output, with the mirror's paths shown as dir's.
func runDeno(ctx context.Context, dir string, configs map[string][]byte, lockName string, lock []byte) ([]byte, error) {
	bin, err := exec.LookPath("deno")
	if err != nil {
		return nil, fmt.Errorf("deno not found (needed to update %s)", lockName)
	}
	all := maps.Clone(configs)
	all[lockName] = lock
	tmp, base, err := toolrun.Mirror(dir, toolrun.MirrorOptions{Pattern: "deps-deno-*", Files: all, Skip: skipEntry})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := []string{"install", "--lockfile-only", "--frozen=false"}
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = base
	cmd.Env = toolrun.Env("DENO_NO_UPDATE_CHECK=1", "NO_COLOR=1")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "deno command", "bin", bin, "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	if err != nil {
		msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), base, dir), err.Error())
		return nil, fmt.Errorf("deno install in %s: %s", dir, msg)
	}
	return os.ReadFile(filepath.Join(base, filepath.FromSlash(lockName)))
}
