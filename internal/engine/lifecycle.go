package engine

import (
	"cmp"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Warning kinds.
const (
	// WarnDeprecated: the registry marks the package or the installed version deprecated,
	// yanked or inactive, or its source repository is archived (datasource.DeprecationSource).
	WarnDeprecated = "deprecated"
	// WarnAbandoned: the newest release is older than abandonmentThreshold.
	WarnAbandoned = "abandoned"
)

// Warning flags a dep that likely needs replacing rather than updating; the dependency
// dashboard lists them and the dep's update PRs repeat them.
type Warning struct {
	Dep    dep.Dep
	Kind   string // WarnDeprecated or WarnAbandoned
	Reason string // e.g. "package deprecated: use x" or "no release since 2020-02-11"
}

// checkLifecycle records the warnings of the dep after Releases: what the datasource reports
// as deprecated for the installed version (no request), and, with an abandonmentThreshold,
// whether the newest release is older than it. The latter is decided from the datasource's
// release bound when that suffices (npm `modified`, no request), else from the release time
// of the newest version (one lookup, shared with minimumReleaseAge).
func (r *resolver) checkLifecycle(scheme versioning.Scheme) {
	if ds, ok := r.base.(datasource.DeprecationSource); ok {
		if why := ds.Deprecation(r.d, r.upstream(cmp.Or(r.d.Locked, r.d.Version))); why != "" {
			r.warnings = append(r.warnings, Warning{Dep: r.d, Kind: WarnDeprecated, Reason: why})
		}
	}
	threshold := r.pol.pre.AbandonAfter
	timer, ok := r.ds.(datasource.Timer)
	if threshold <= 0 || !ok {
		return
	}
	cutoff := r.now.Add(-threshold)
	if b, ok := timer.(datasource.ReleaseBound); ok {
		if bound := b.ReleasedBy(r.d); !bound.IsZero() {
			if bound.Before(cutoff) {
				r.abandoned("no release since "+day(bound), threshold)
				return
			}
		}
	}
	newest := newestStable(scheme, r.rels)
	if newest == "" {
		return
	}
	if t := r.releaseTime(timer, newest); !t.IsZero() && t.Before(cutoff) {
		r.abandoned("newest release "+newest+" published "+day(t), threshold)
	}
}

func (r *resolver) abandoned(reason string, threshold time.Duration) {
	r.warnings = append(r.warnings, Warning{Dep: r.d, Kind: WarnAbandoned,
		Reason: reason + " (abandonmentThreshold " + config.FormatAge(threshold) + ")"})
}

// newestStable returns the highest valid release that is not a prerelease, "" if none.
func newestStable(scheme versioning.Scheme, rels []datasource.Release) string {
	newest := ""
	for _, rel := range rels {
		v := rel.Version
		if !scheme.Valid(v) {
			continue
		}
		if sv := scheme.Semver(v); sv != nil && sv.Prerelease() != "" {
			continue
		}
		if newest == "" || scheme.Compare(v, newest) > 0 {
			newest = v
		}
	}
	return newest
}

// day renders t as a UTC date.
func day(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// warningTexts renders the warnings for an update PR body: "<kind>: <reason>".
func warningTexts(ws []Warning) []string {
	if len(ws) == 0 {
		return nil
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Kind + ": " + w.Reason
	}
	return out
}
