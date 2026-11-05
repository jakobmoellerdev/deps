package swift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// prunedDirs are not searched for root packages: the directories discovery prunes and
// SwiftPM's build directory, whose checkouts carry their own Package.resolved files.
var prunedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true, ".build": true}

// Locker is the swift post-processor (engine.PostProcessor): it moves the pins of updated
// packages in every Package.resolved whose root package reaches the updated manifest, in
// process. The new revision is the commit of the new version's tag from tags (the run's
// git-tags datasource, which already listed the repository's tags). The root packages of a
// scanned root are found once per Locker.
type Locker struct {
	tags datasource.Datasource

	mu    sync.Mutex
	roots map[string]*rootsEntry // by absolute scanned root
}

type rootsEntry struct {
	once  sync.Once
	roots []root
}

// NewLocker returns a Locker that looks up revisions with tags.
func NewLocker(tags datasource.Datasource) *Locker {
	return &Locker{tags: tags, roots: map[string]*rootsEntry{}}
}

// rootsOf returns the root packages below top (absolute): every directory with Package.swift
// and Package.resolved, outside prunedDirs, in path order.
func (l *Locker) rootsOf(top string) []root {
	top = filepath.Clean(top)
	l.mu.Lock()
	e := l.roots[top]
	if e == nil {
		e = &rootsEntry{}
		l.roots[top] = e
	}
	l.mu.Unlock()
	e.once.Do(func() {
		// discovery walked the same tree, so a walk error is not expected here; the roots found
		// up to it are kept.
		err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir():
				if p != top && prunedDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			case d.Name() != resolvedFile:
				return nil
			}
			rel, err := filepath.Rel(top, filepath.Dir(p))
			if err != nil {
				return err
			}
			if st, err := os.Stat(filepath.Join(filepath.Dir(p), manifestFile)); err == nil && st.Mode().IsRegular() {
				e.roots = append(e.roots, newRoot(top, filepath.ToSlash(rel)))
			}
			return nil
		})
		if err != nil {
			slog.Debug("searching root packages", "dir", top, "err", err)
		}
	})
	return e.roots
}

// Scope returns the scanned root itself when some root package reaches the manifest's
// package: the Package.resolved files of all root packages are post-processed together.
func (l *Locker) Scope(_ context.Context, top, manifest string) (string, bool) {
	dir := path.Dir(manifest)
	for _, r := range l.rootsOf(top) {
		if r.members[dir] {
			return ".", true
		}
	}
	return "", false
}

// Siblings is unused: ScopeSiblings lists the Package.resolved files.
func (*Locker) Siblings() []string { return nil }

// ScopeSiblings implements engine.ScopeSiblingLister: the Package.resolved of every root
// package below dir, the scanned root.
func (l *Locker) ScopeSiblings(dir string) []string {
	roots := l.rootsOf(dir)
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.resolvedPath()
	}
	return out
}

// ScopeInputs implements engine.ScopeInputLister: the Package.swift files read for the local
// graphs of the root packages that reach one of the manifests.
func (l *Locker) ScopeInputs(_ context.Context, top, _ string, manifests []string) (paths, digests []string, err error) {
	seen := map[string]bool{}
	for _, r := range l.rootsOf(top) {
		if !slices.ContainsFunc(manifests, func(m string) bool { return r.members[path.Dir(m)] }) {
			continue
		}
		for _, m := range r.manifests {
			seen[m] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil, nil
}

// Process changes nothing: the pins move with the updates, which the engine passes to
// ProcessUpdates.
func (*Locker) Process(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
	return nil, nil
}

// ProcessUpdates implements engine.UpdatePinner. For every root package whose local graph
// holds the manifest of an update (also a lockfile-only one), the Package.resolved pin of the
// update's repository gets the new version and the commit of its tag; a pin the new
// requirement admits that is already at or above the version is kept (pins are never moved
// down). Pins of other packages are kept: when the new version needs other versions of its
// own dependencies, SwiftPM's next resolution sees the pins no longer satisfy the graph and
// re-resolves them. When the root's own Package.swift was patched, the v3 `originHash` is
// recomputed as SwiftPM does (the SHA-256 of the root manifest), provided it matched the
// manifest before. dir is the scanned root; manifests are by slash path relative to it.
func (l *Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, r := range l.rootsOf(dir) {
		name := r.resolvedPath()
		src, ok := files[name]
		if !ok {
			continue
		}
		rootManifest := path.Join(r.dir, manifestFile)
		patched, rootPatched := manifests[rootManifest]
		var touching []dep.Update
		for _, byManifest := range [...]map[string][]dep.Update{ups, locks} {
			for _, m := range slices.Sorted(maps.Keys(byManifest)) {
				if r.members[path.Dir(m)] {
					touching = append(touching, byManifest[m]...)
				}
			}
		}
		if len(touching) == 0 {
			continue
		}
		res, err := parseResolved(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		edits, err := l.pinEdits(ctx, res, touching)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if rootPatched && res.originHash != "" && res.originSpan != (dep.Span{}) {
			old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rootManifest)))
			if h := sha256Hex(patched); err == nil && sha256Hex(old) == res.originHash && h != res.originHash {
				edits = append(edits, patch.Edit{Start: res.originSpan.Start, End: res.originSpan.End, Old: res.originHash, New: h})
			}
		}
		if len(edits) == 0 {
			continue
		}
		b, err := patch.Apply(src, edits)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = b
	}
	return out, nil
}

// pinEdits returns the version and revision edits of the pins the updates move.
func (l *Locker) pinEdits(ctx context.Context, res resolved, ups []dep.Update) ([]patch.Edit, error) {
	type move struct {
		version string
		u       dep.Update
	}
	moves := map[int]move{}
	for _, u := range ups {
		if u.Dep.Source == "" {
			continue
		}
		i := res.find(repoKey(u.Dep.Source))
		if i < 0 {
			continue
		}
		p := res.pins[i]
		if p.version == "" || p.versionSpan == (dep.Span{}) || p.revisionSpan == (dep.Span{}) {
			continue
		}
		v := strings.TrimPrefix(u.NewVersion, "v")
		if compare(p.version, v) >= 0 && newRequirement(u).admits(p.version) {
			continue
		}
		if m, ok := moves[i]; ok && compare(m.version, v) >= 0 {
			continue
		}
		moves[i] = move{v, u}
	}
	var edits []patch.Edit
	for _, i := range slices.Sorted(maps.Keys(moves)) {
		m, p := moves[i], res.pins[i]
		rev, err := l.tags.Digest(ctx, m.u.Dep, m.version)
		if err != nil {
			return nil, fmt.Errorf("commit of %s %s: %w", m.u.Dep.Name, m.version, err)
		}
		edits = append(edits,
			patch.Edit{Start: p.versionSpan.Start, End: p.versionSpan.End, Old: p.version, New: m.version},
			patch.Edit{Start: p.revisionSpan.Start, End: p.revisionSpan.End, Old: p.revision, New: rev})
	}
	return edits, nil
}

// newRequirement returns the requirement of u's dep after u: the range rewritten for the new
// version (kept for a lockfile-only update), or the exact new version.
func newRequirement(u dep.Update) requirement {
	v := strings.TrimPrefix(u.NewVersion, "v")
	if u.Dep.Range == "" {
		return requirement{kind: reqExact, lower: version{v: v}}
	}
	r := parseRequirement(u.Dep.Range)
	if u.Lockfile {
		return r
	}
	return parseRequirement(r.rewrite(u.Dep.Range, v, u.RangeStrategy))
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
