package engine

import (
	"cmp"
	"log/slog"
	"slices"
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
// of the newest version (one lookup, shared with minimumReleaseAge). The abandonment warning
// is advisory: a failed lookup only loses it (it is cached, so a guard that needs the same
// version still fails the dep) instead of failing a dep that has updates to propose.
func (r *resolver) checkLifecycle(scheme versioning.Scheme) {
	if ds, ok := r.base.(datasource.DeprecationSource); ok {
		// The base datasource knows release names, not versions extracted from them.
		installed := r.upstream(cmp.Or(r.d.Locked, r.d.Version))
		if e, ok := r.ds.(releaseNamer); ok {
			installed = e.name(installed)
		}
		if why := ds.Deprecation(r.d, installed); why != "" {
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
	newest := r.newestReleased(scheme)
	if newest == "" {
		return
	}
	t, err := r.releaseTime(timer, newest)
	if err != nil {
		slog.DebugContext(r.ctx, "abandonment check without a release time", "name", r.d.Name, "version", newest, "err", err)
		return
	}
	if !t.IsZero() && t.Before(cutoff) {
		r.abandoned("newest release "+newest+" published "+day(t), threshold)
	}
}

func (r *resolver) abandoned(reason string, threshold time.Duration) {
	r.warnings = append(r.warnings, Warning{Dep: r.d, Kind: WarnAbandoned,
		Reason: reason + " (abandonmentThreshold " + config.FormatAge(threshold) + ")"})
}

// newestReleased returns the newest stable release the abandonment check dates: the highest
// valid non-prerelease version whose go.mod declares the dep's own module path, since a Go
// module's version list can carry tags of an unrelated line (a repository that once held
// another module), which no update would ever propose. At most abandonPathChecks versions
// are asked for their path; if they all name another module, the dep is not dated.
func (r *resolver) newestReleased(scheme versioning.Scheme) string {
	stable := stableVersions(scheme, r.rels)
	slices.SortFunc(stable, func(a, b string) int { return scheme.Compare(b, a) })
	for i, v := range stable {
		if r.declared == nil {
			return v
		}
		if i == abandonPathChecks {
			slog.DebugContext(r.ctx, "abandonment check without a version of this module", "name", r.d.Name, "checked", i)
			return ""
		}
		if r.declaresOtherPath(r.d.Name, v) == "" {
			return v
		}
	}
	return ""
}

// abandonPathChecks bounds the declared-path lookups of newestReleased.
const abandonPathChecks = 5

// stableVersions returns the valid releases that are no prerelease.
func stableVersions(scheme versioning.Scheme, rels []datasource.Release) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		v := rel.Version
		if !scheme.Valid(v) {
			continue
		}
		if sv := scheme.Semver(v); sv != nil && sv.Prerelease() != "" {
			continue
		}
		out = append(out, v)
	}
	return out
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
