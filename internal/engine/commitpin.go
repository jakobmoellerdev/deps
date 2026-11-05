package engine

import (
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// commitUpdates resolves an action pinned to a bare SHA (config.UnversionedPin).
//
// When tags point at the commit, the dep is at the newest of their versions: its policies are
// evaluated again for that version (it is no longer ignored by default) and it resolves like a
// pin with a version comment (versionUpdates); the manager writes the new version as a
// `# <version>` comment.
//
// Otherwise the dep tracks the default branch: under its policies (ignored by default, like
// Renovate's unversioned-reference) it gets a digest update to the commit of the repository's
// HEAD (headDigest).
func (r *resolver) commitUpdates(t *Trace, scheme versioning.Scheme, vulns func() []Vuln) ([]dep.Update, error) {
	dir := filepath.Join(r.root, filepath.FromSlash(path.Dir(r.d.File)))
	rels, err := r.ds.Releases(r.ctx, r.d, dir)
	if err != nil {
		return nil, err
	}
	if v := taggedVersion(scheme, rels, r.d.Digest); v != "" {
		slog.DebugContext(r.ctx, "commit pin at a tag", "name", r.d.Name, "digest", r.d.Digest, "version", v)
		r.d.Version, r.cur, t.Dep.Version = v, v, v
		_, timed := r.base.(datasource.Timer)
		pol := newPolicies(r.pol.cfg, r.d, r.pol.kindRules)
		pol.aged = timed && pol.cfg.WantsCurrentAge(pol.d)
		r.pol, t.Rules, t.Policy = pol, pol.pre.Rules, pol.pre
		if !pol.aged {
			if reason := pol.skip(r.d); reason != "" {
				return nil, &dep.SkipError{Reason: reason}
			}
		}
		return r.versionUpdates(t, scheme, vulns)
	}
	// The current version stays unknown, so a currentAge rule cannot match (as applyAge would
	// find): the pre-policy decides.
	if reason := r.pol.skip(r.d); reason != "" {
		return nil, &dep.SkipError{Reason: reason}
	}
	return r.headDigest(t, dir)
}

// taggedVersion returns the newest version among the tags pointing at commit (v4.2.0 rather
// than the floating v4 next to it), "" when none does.
func taggedVersion(scheme versioning.Scheme, rels []Release, commit string) string {
	var best string
	for _, rel := range rels {
		if !strings.EqualFold(rel.Digest, commit) || !scheme.Valid(rel.Version) {
			continue
		}
		if best == "" {
			best = rel.Version
			continue
		}
		if c := scheme.Compare(rel.Version, best); c > 0 || c == 0 && len(rel.Version) > len(best) {
			best = rel.Version
		}
	}
	return best
}

// headDigest is the digest update of a commit pin to the commit of its repository's HEAD
// (datasource.HeadSource), under the digest policy (updates, ignore) and its minimumReleaseAge
// against the commit time (datasource.CommitTimer, asked only when a minimum applies; an
// unknown time only holds it back under timestamp-required). A head equal to the pin, or none
// (an empty repository), gives no update.
func (r *resolver) headDigest(t *Trace, dir string) ([]dep.Update, error) {
	d := r.d
	hs, ok := r.base.(datasource.HeadSource)
	if !ok || !r.pol.allows(dep.Digest) {
		return nil, nil
	}
	rev, err := hs.Head(r.ctx, d, dir)
	if err != nil {
		return nil, fmt.Errorf("default branch head: %w", err)
	}
	if rev.Digest == "" || strings.EqualFold(rev.Digest, d.Digest) {
		slog.DebugContext(r.ctx, "commit pin at the default branch head", "name", d.Name, "digest", d.Digest, "head", rev.Digest)
		return nil, nil
	}
	p := r.pol.of(dep.Digest)
	if p.MinAge > 0 && rev.Time.IsZero() {
		if ct, ok := r.base.(datasource.CommitTimer); ok {
			rev.Time = r.commitTime(ct, rev.Digest)
		}
	}
	short := dep.ShortDigest(rev.Digest)
	if reason := r.ageReason(p, short, rev.Time); reason != "" {
		if r.trace {
			t.Rejected = map[string][]string{reason: {short}}
		}
		r.holdBack(d, short, reason)
		return nil, nil
	}
	return []dep.Update{{Dep: d, Type: dep.Digest, NewDigest: rev.Digest}}, nil
}

// commitTime asks ct when commit was made; a failed lookup is logged and reported as zero.
func (r *resolver) commitTime(ct datasource.CommitTimer, commit string) time.Time {
	t, err := ct.CommitTime(r.ctx, r.d, commit)
	if err != nil {
		slog.DebugContext(r.ctx, "commit time lookup failed", "name", r.d.Name, "commit", commit, "err", err)
		return time.Time{}
	}
	return t
}
