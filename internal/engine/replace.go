package engine

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// ReplacementBucket is the PR bucket of replacements: their branch (`-replacement`) differs
// from the dep's usual one, so its usual updates get their own PRs once the replacement's PR
// was closed (dep.Update.Declined).
const ReplacementBucket = "replacement"

// updates decides, in one place, between a replacement and the dep's usual updates. A dep
// whose pre-policy names a replacement its manager can write (replaceable) gets it when the
// policy of its type, evaluated with the age of the current version for currentAge rules,
// does not ignore it; its usual updates become the replacement's Declined, which `run`
// proposes once the replacement's PR was closed. Otherwise, and when the replacement does not
// apply (see replacementUpdate), the usual updates are returned.
func (r *resolver) updates(t *Trace, scheme versioning.Scheme, vulns func() []Vuln) ([]dep.Update, error) {
	if !r.replaceable() {
		return r.usual(t, scheme, vulns)
	}
	if r.pol.aged {
		reason, err := r.applyAge(t)
		switch {
		case err != nil:
			return nil, err
		case reason != "":
			return nil, &dep.SkipError{Reason: reason}
		}
		// Asked before the releases are known, so maybe under another release name; the usual
		// lookup asks again.
		r.times = nil
	}
	if !r.pol.allows(dep.Replacement) {
		return r.usual(t, scheme, vulns)
	}
	up, err := r.replacementUpdate()
	if err != nil || up == nil {
		if err != nil {
			return nil, err
		}
		return r.usual(t, scheme, vulns)
	}
	declined, err := r.usual(t, scheme, vulns)
	if err != nil {
		// The replacement does not depend on the old name: its lookup failing (a removed
		// package) only loses the fallback.
		slog.DebugContext(r.ctx, "usual updates of a replaced dependency unavailable", "file", r.d.File, "line", r.d.Line, "name", r.d.Name, "err", err)
	}
	up.Declined = declined
	return []dep.Update{*up}, nil
}

// replaceable reports whether the pre-policy names a replacement (config.Policy.ReplacementName)
// that the dep's manager can write (manager.Renamer, dep.Dep.Renamable). A replacement it
// cannot write is logged: a warning for a rule's, at debug level for a built-in one.
func (r *resolver) replaceable() bool {
	p := &r.pol.pre
	if p.ReplacementName == "" {
		return false
	}
	if r.renamer == nil || !r.d.Renamable() {
		slog.Log(r.ctx, r.replacementLevel(), "replacement not supported for this dependency", "file", r.d.File, "line", r.d.Line, "name", r.d.Name, "manager", r.d.Manager, "replacement", p.ReplacementName)
		return false
	}
	return true
}

// replacementLevel is the log level of a replacement that does not apply: a warning for a
// rule's (the user asked for it), debug for a built-in one.
func (r *resolver) replacementLevel() slog.Level {
	if r.pol.pre.Replacement != "" {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// replacementUpdate is the replacement update of the dep: the name moves to the pre-policy's
// ReplacementName, the version to its ReplacementVersion (the current one when unset). The new
// name must have that version: its releases are listed once. A replacement that cannot apply
// (a missing version, or a new version of a shared version definition) is notApplied. A
// digest-pinned dep (or one under pin: digest) gets the digest of the new version, so the
// replaced reference stays pinned. Advisories of the old package do not apply. A replacement
// naming the dep's current name and version is no update (nil).
func (r *resolver) replacementUpdate() (*dep.Update, error) {
	d, p := r.d, &r.pol.pre
	nd, err := r.renamer.Rename(d, p.ReplacementName)
	if err != nil {
		return nil, fmt.Errorf("replacement %s: %w", p.ReplacementName, err)
	}
	version := cmp.Or(p.ReplacementVersion, d.Version)
	if nd.Name == d.Name && nd.Source == d.Source && version == d.Version {
		return nil, nil
	}
	if d.VersionRef != "" && version != d.Version {
		// A version definition (a Maven property, a Gradle version.ref) other deps can share:
		// writing the new package's version there would move them too.
		return r.notApplied("replacement " + p.ReplacementName + " would change the version definition " + d.VersionRef)
	}
	dir := filepath.Join(r.root, filepath.FromSlash(path.Dir(d.File)))
	rels, err := r.ds.Releases(r.ctx, nd, dir)
	if err != nil {
		return nil, fmt.Errorf("replacement %s: %w", p.ReplacementName, err)
	}
	// The version as written may be shorter than the release (a terraform `~> 5.0` against
	// 5.0.0): an exact match wins, else one equal up to trailing .0 parts.
	digest, found := "", false
	for _, rel := range rels {
		if rel.Version == version {
			digest, found = rel.Digest, true
			break
		}
		if !found && trimZeros(rel.Version) == trimZeros(version) {
			digest, found = rel.Digest, true
		}
	}
	if !found {
		return r.notApplied("replacement " + p.ReplacementName + " has no version " + version)
	}
	up := dep.Update{Dep: d, Type: dep.Replacement, NewName: p.ReplacementName, NewVersion: version, Bucket: ReplacementBucket}
	if digestCapable(d) && (d.Digest != "" || p.Pin == config.PinDigest) {
		if digest == "" {
			if digest, err = r.ds.Digest(r.ctx, nd, version); err != nil {
				return nil, fmt.Errorf("replacement %s: %w", p.ReplacementName, err)
			}
		}
		if digest == "" && d.Digest != "" {
			return nil, errors.New("replacement " + p.ReplacementName + ": no digest of version " + version + " to pin")
		}
		up.NewDigest = digest
	}
	return &up, nil
}

// notApplied handles a replacement that cannot apply for reason: a rule's skips the dep with
// it, a built-in one leaves the dep to its usual updates (nil, logged at debug level).
func (r *resolver) notApplied(reason string) (*dep.Update, error) {
	if r.pol.pre.Replacement != "" {
		slog.DebugContext(r.ctx, "built-in replacement not applied", "name", r.d.Name, "reason", reason)
		return nil, nil
	}
	return nil, &dep.SkipError{Reason: reason}
}

// trimZeros drops the trailing ".0" parts of a version (5.0.0 → 5).
func trimZeros(v string) string {
	for strings.HasSuffix(v, ".0") {
		v = v[:len(v)-2]
	}
	return v
}
