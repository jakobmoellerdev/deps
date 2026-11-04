package engine

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// goPseudo reports whether d is a go.mod requirement on a pseudo-version
// (vX.Y.Z-<timestamp>-<commit>): it tracks a commit, so it gets digest updates to newer
// pseudo-versions, or an update to a newer release.
func goPseudo(d dep.Dep) bool {
	return d.Manager == gomod.Name && d.Versioning == "go" && module.IsPseudoVersion(d.Version)
}

// pseudoCandidates returns the version releases of a pseudo-version cur are ranked against,
// and the releases newer than cur (semver order: a pseudo-version sorts just above its base
// release). The go scheme rejects pseudo-versions, so ranking uses the base release
// (v1.2.3 for v1.2.4-0.<ts>-<rev>), or vX.0.0-0 for vX.0.0-<ts>-<rev>, which has none; kinds
// are relative to it. Prereleases count only when the base is one.
func pseudoCandidates(cur string, names []string, reject func(v, reason string)) (string, []string) {
	base, _ := module.PseudoVersionBase(cur)
	rank := base
	if base == "" {
		rank = semver.Major(cur) + ".0.0-0"
	}
	pre := semver.Prerelease(base) != ""
	kept := make([]string, 0, len(names))
	for _, n := range names {
		switch {
		case !semver.IsValid(n) || module.IsPseudoVersion(n):
			continue // the scheme reports them
		case semver.Compare(n, cur) <= 0:
			if reject != nil {
				reject(n, "not newer")
			}
		case semver.Prerelease(n) != "" && !pre:
			if reject != nil {
				reject(n, "prerelease")
			}
		default:
			kept = append(kept, n)
		}
	}
	return rank, kept
}

// headUpdate is the digest update of a pseudo-version: the module head's newer
// pseudo-version (datasource.HeadSource), under the digest policy (updates, ignore) and the
// digest minimumReleaseAge against the commit time. A head that is a release, of another
// major, or not newer gives none.
func (r *resolver) headUpdate(dir string, reject func(v, reason string)) ([]dep.Update, error) {
	d := r.d
	hs, ok := r.ds.(datasource.HeadSource)
	if !ok || !r.pol.allows(dep.Digest) {
		return nil, nil
	}
	rev, err := hs.Head(r.ctx, d, dir)
	if err != nil {
		return nil, fmt.Errorf("latest commit: %w", err)
	}
	v := rev.Version
	if !module.IsPseudoVersion(v) || semver.Compare(v, d.Version) <= 0 || semver.Major(v) != semver.Major(d.Version) {
		slog.DebugContext(r.ctx, "no newer pseudo-version", "name", d.Name, "current", d.Version, "head", v)
		return nil, nil
	}
	if age := r.pol.of(dep.Digest).MinAge; age > 0 && !rev.Time.IsZero() && r.now.Sub(rev.Time) < age {
		reason := fmt.Sprintf("too new (released %s, minimum %s)", rev.Time.UTC().Format(time.DateOnly), config.FormatAge(age))
		if reject != nil {
			reject(v, reason)
		}
		if r.pending.Version == "" {
			r.pending = Pending{Dep: d, Version: v, Reason: reason}
		}
		return nil, nil
	}
	return []dep.Update{{Dep: d, Type: dep.Digest, NewVersion: v, NewDigest: rev.Digest}}, nil
}

// majorPathUpdates adds the major module path updates of a go.mod requirement (gomodUpdateImportPaths):
// per successor path (datasource.MajorPathSource, `/v<N+1>` …) its newest release accepted
// under the major policy and old enough, in the PR bucket of a major candidate. A later
// (higher) path replaces an update of the same bucket, except a security update. Updates
// are ordered lowest first, so these come last.
func (r *resolver) majorPathUpdates(t *Trace, ups []dep.Update, scheme versioning.Scheme, dir string, accept func(string, dep.UpdateType) (bool, string), reject func(v, reason string)) ([]dep.Update, error) {
	d := r.d
	mps, ok := r.ds.(datasource.MajorPathSource)
	if !ok || d.Manager != gomod.Name || !r.pol.cfg.GomodUpdateImportPathsEnabled() || !r.pol.allows(dep.Major) {
		return ups, nil
	}
	paths, err := mps.MajorPaths(r.ctx, d, dir)
	if err != nil {
		return nil, fmt.Errorf("major module paths: %w", err)
	}
	timer, _ := r.ds.(datasource.Timer)
	for _, mp := range paths {
		t.Candidates += len(mp.Releases)
		v, ok := r.bestOfPath(mp, scheme, timer, accept, reject)
		if !ok {
			continue
		}
		c := versioning.Candidate{Version: v, Kind: dep.Major, Major: strings.TrimPrefix(semver.Major(v), "v")}
		bucket := r.pol.bucket(c).label()
		if slices.ContainsFunc(ups, func(u dep.Update) bool { return u.Bucket == bucket && len(u.Advisories) > 0 }) {
			continue
		}
		ups = slices.DeleteFunc(ups, func(u dep.Update) bool { return u.Bucket == bucket })
		ups = append(ups, dep.Update{Dep: d, Type: dep.Major, NewVersion: v, NewName: mp.Name, Bucket: bucket})
	}
	return ups, nil
}

// bestOfPath returns the newest release of the module path mp that is a stable version,
// accepted as a major update and, under the major minimumReleaseAge, old enough.
func (r *resolver) bestOfPath(mp datasource.ModulePath, scheme versioning.Scheme, timer datasource.Timer, accept func(string, dep.UpdateType) (bool, string), reject func(v, reason string)) (string, bool) {
	cands := make([]string, 0, len(mp.Releases))
	for _, rel := range mp.Releases {
		v := rel.Version
		switch {
		case !scheme.Valid(v):
			continue
		case semver.Prerelease(v) != "":
			if reject != nil {
				reject(mp.Name+"@"+v, "prerelease")
			}
			continue
		}
		if ok, why := accept(v, dep.Major); !ok {
			if reject != nil {
				reject(mp.Name+"@"+v, "policy: "+why)
			}
			continue
		}
		cands = append(cands, v)
	}
	slices.SortFunc(cands, func(a, b string) int { return scheme.Compare(b, a) })
	age := r.pol.of(dep.Major).MinAge
	nd := r.d
	nd.Name = mp.Name
	for _, v := range cands[:min(len(cands), maxRetries)] {
		if age <= 0 || timer == nil {
			return v, true
		}
		tb, err := timer.ReleaseTime(r.ctx, nd, v)
		if err != nil || tb.IsZero() || r.now.Sub(tb) >= age {
			return v, true
		}
		reason := fmt.Sprintf("too new (released %s, minimum %s)", tb.UTC().Format(time.DateOnly), config.FormatAge(age))
		if reject != nil {
			reject(mp.Name+"@"+v, reason)
		}
		if r.pending.Version == "" {
			r.pending = Pending{Dep: r.d, Version: mp.Name + "@" + v, Reason: reason}
		}
	}
	return "", false
}
