// Package nix extracts the inputs of Nix flakes: every input of a `flake.nix` that its
// `flake.lock` locks to a commit of a git repository (GitHub, GitLab, SourceHut, git URLs,
// Gitea/Forgejo archive tarballs and NixOS channels) is a dep, tracking a branch (digest
// updates of flake.lock) or a version tag (ref updates of flake.nix). flake.lock moves with
// `nix flake update <input>` (Locker).
package nix

import (
	"context"
	"errors"
	"path"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "nix"

// DepTypeInput is the dep type of every flake input.
const DepTypeInput = "input"

// Datasource names: git-tags for an input whose ref is a version tag, git-refs for a branch.
const (
	dsTags = "git-tags"
	dsRefs = "git-refs"
)

// defaultBranch is the version of an input without a ref: the repository's HEAD.
const defaultBranch = "HEAD"

const (
	manifestName = "flake.nix"
	lockFile     = "flake.lock"
)

// Skip reasons.
const (
	skipNoLock      = "no flake.lock"
	skipUnreadable  = "unreadable flake.lock"
	skipNotLocked   = "not in flake.lock"
	skipFollows     = "follows another input"
	skipIndirect    = "indirect input (flake registry)"
	skipPath        = "local path input"
	skipNoRev       = "no locked revision"
	skipURL         = "unrecognized url"
	skipTarball     = "unsupported tarball url"
	skipRefSpan     = "cannot locate ref"
	skipRevSpan     = "cannot locate rev"
	skipUnsupported = "unsupported input type "
)

var fileRe = regexp.MustCompile(`(^|/)flake\.nix$`)

type nixManager struct{}

// New returns the nix manager.
func New() manager.Manager { return nixManager{} }

func (nixManager) Name() string { return Name }

// Match reports whether the slash path is a flake.nix.
func (nixManager) Match(p string) bool { return fileRe.MatchString(p) }

// input is what the flake.nix says about one input.
type input struct {
	name          string
	line          int
	url, ref, rev *binding
	source        bool // a source attribute other than url, ref and rev (type, owner, repo, …)
}

// Extract returns one dep per input of the flake.nix that names a source (an input with only
// `follows` is none). The ref and rev written in its url (or its `ref`/`rev` attributes) are
// the dep's Version and Digest with their spans; ReadLocks completes the dep from flake.lock.
func (nixManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var inputs []*input
	for _, b := range parseInputs(src) {
		// inputs.<name>.<attr>; deeper paths are the input's own inputs (follows).
		if len(b.path) != 2 || b.path[0] == "" {
			continue
		}
		i := slices.IndexFunc(inputs, func(in *input) bool { return in.name == b.path[0] })
		if i < 0 {
			i = len(inputs)
			inputs = append(inputs, &input{name: b.path[0], line: b.line})
		}
		in := inputs[i]
		switch b.path[1] {
		case "url":
			in.url = &b
		case "ref":
			in.ref = &b
		case "rev":
			in.rev = &b
		case "follows", "flake":
		default:
			in.source = true
		}
	}
	var deps []dep.Dep
	for _, in := range inputs {
		if in.url == nil && in.ref == nil && in.rev == nil && !in.source {
			continue
		}
		var r refParts
		if in.url != nil && in.url.span != (dep.Span{}) {
			r = parseRef(in.url.value, in.url.span.Start)
		}
		if in.ref != nil {
			r.ref, r.refSpan = in.ref.value, in.ref.span
		}
		if in.rev != nil {
			r.rev, r.revSpan = in.rev.value, in.rev.span
		}
		deps = append(deps, dep.Dep{
			Manager: Name, Datasource: dsRefs, Versioning: "tag", DepType: DepTypeInput, File: file, Line: in.line, Name: in.name,
			Version: r.ref, RefSpan: r.refSpan, Digest: r.rev, DigestSpan: r.revSpan,
		})
	}
	return deps, nil
}

// Edits rewrites the ref of an input tracking a version tag and the rev of an input pinned to
// a commit in flake.nix; flake.lock moves with every update (Locker), so an update of an
// unpinned branch input has no edits.
func (nixManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewVersion != "" && u.NewVersion != d.Version {
		if d.Datasource != dsTags || d.RefSpan == (dep.Span{}) {
			return nil, errors.New("cannot change the branch of an input")
		}
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	if d.DigestSpan != (dep.Span{}) && u.NewDigest != "" && u.NewDigest != d.Digest {
		edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest})
	}
	return edits, nil
}

// LockRoots implements manager.LockMaintainer: the flake.lock of each flake.nix with one.
func (nixManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: lockPath(f)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// lockPath returns the slash path of the flake.lock of manifest (both relative to the root).
func lockPath(manifest string) string { return path.Join(path.Dir(manifest), lockFile) }

func validTag(s string) bool {
	_, ok := versioning.ParseTag(s)
	return ok
}
