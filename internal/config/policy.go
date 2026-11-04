package config

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// Commit types and the default scope.
const (
	CommitFix    = "fix"
	CommitChore  = "chore"
	DefaultScope = "deps"
)

// Policy is the effective rule outcome for one dep (Config.Policy), or for one update type
// of it (Config.PolicyFor).
type Policy struct {
	Ignore       bool
	IgnoreReason string // why Ignore is set: default for the dep type, or the deciding rule
	Pin          string
	// RangeStrategy is the npm range strategy (RangeStrategies); never "" in a Policy.
	RangeStrategy string
	Allow         *Allowed
	Updates       map[dep.UpdateType]bool
	Group         string         // "" = own PR
	Schedule      *schedule.Set  // nil = always
	Rules         []int          // indices of the matching user rules, in order
	Monorepo      string         // built-in monorepo that set the initial Group, "" = none
	CommitType    string         // conventional commit type: fix for production deps, chore otherwise
	CommitScope   string         // conventional commit scope, "" = none
	Kind          dep.UpdateType // update type of a PolicyFor result, "" for Policy
	MinAge        time.Duration  // minimum release age of new versions, 0 = off
	Versioning    string         // versioning scheme set by a rule, "" = the dep's own
	Automerge     bool           // request platform auto-merge for the PR
	// AutomergeStrategy is the merge method of an automerged PR (AutomergeStrategies).
	AutomergeStrategy string
	// PR buckets (separateMajorMinor, separateMultipleMajor, separateMinorPatch).
	SeparateMajorMinor, SeparateMultipleMajor, SeparateMinorPatch bool
}

// production reports whether d is a production dep, which gets the fix commit type by
// default (Renovate's :semanticPrefixFixDepsChoreOthers).
func production(d dep.Dep) bool {
	return d.Manager == "gomod" && d.DepType == "direct" || d.Manager == "npm" && d.DepType == "dependencies"
}

// defaultIgnored are the dep types ignored unless a rule sets `ignore: false`.
var defaultIgnored = map[string]bool{"indirect": true, "go": true, "peerDependencies": true, "engines": true}

// IgnoredByDefault reports whether deps of depType are ignored unless a rule sets
// `ignore: false`.
func IgnoredByDefault(depType string) bool { return defaultIgnored[depType] }

// Policy is the pre-policy of d: the policy before the type of an update is known. It
// evaluates the built-in monorepo rule (unless monorepos is false), then the user rules
// without updateTypes top-to-bottom; each set field of a matching rule overwrites the
// policy (Updates is replaced, not merged). Indirect Go requirements, go directives, npm
// peerDependencies and engines are ignored unless a rule sets `ignore: false`. Its Versioning replaces the
// dep's scheme before resolution (only rules without updateTypes may set it).
func (c *Config) Policy(d dep.Dep) Policy { return c.policy(d, "") }

// PolicyFor is the policy of an update of kind to d: like Policy, but rules with updateTypes
// are evaluated too, in file order, when they list kind. Without such rules it equals Policy
// (apart from Kind). The engine buckets each candidate with the Separate* flags of the policy
// of its kind, so an updateTypes rule can change them for its types.
func (c *Config) PolicyFor(d dep.Dep, kind dep.UpdateType) Policy { return c.policy(d, kind) }

// HasUpdateTypeRules reports whether any rule sets updateTypes, i.e. whether PolicyFor can
// differ from Policy.
func (c *Config) HasUpdateTypeRules() bool {
	return slices.ContainsFunc(c.Rules, func(r Rule) bool { return len(r.UpdateTypes) > 0 })
}

// matches reports whether r applies to d; kind "" skips rules with updateTypes.
func (r *Rule) matches(d dep.Dep, kind dep.UpdateType) bool {
	switch {
	case r.Manager != "" && r.Manager != d.Manager,
		len(r.Names) > 0 && !matchAny(r.Names, d.Name),
		len(r.DepTypes) > 0 && !slices.Contains(r.DepTypes, d.DepType),
		len(r.Paths) > 0 && !matchAny(r.Paths, d.File),
		len(r.Datasources) > 0 && !slices.ContainsFunc(r.Datasources, func(ds string) bool { return CanonicalDatasource(ds) == d.Datasource }),
		len(r.UpdateTypes) > 0 && (kind == "" || !slices.Contains(r.UpdateTypes, string(kind))):
		return false
	}
	return true
}

func (c *Config) policy(d dep.Dep, kind dep.UpdateType) Policy {
	p := Policy{
		Pin:                   c.Pin,
		RangeStrategy:         cmp.Or(c.RangeStrategy, RangeAuto),
		Updates:               make(map[dep.UpdateType]bool, len(dep.UpdateTypes)),
		Schedule:              c.schedule,
		MinAge:                c.minAge,
		CommitType:            CommitChore,
		CommitScope:           DefaultScope,
		Kind:                  kind,
		SeparateMajorMinor:    c.SeparateMajorMinor == nil || *c.SeparateMajorMinor,
		SeparateMultipleMajor: c.SeparateMultipleMajor != nil && *c.SeparateMultipleMajor,
		SeparateMinorPatch:    c.SeparateMinorPatch != nil && *c.SeparateMinorPatch,
		Automerge:             c.Automerge,
		AutomergeStrategy:     cmp.Or(c.AutomergeStrategy, StrategyAuto),
	}
	if production(d) {
		p.CommitType = CommitFix
	}
	if c.MonoreposEnabled() {
		if m := monorepoOf(d.Manager, d.Name); m != nil {
			p.Monorepo, p.Group = m.Name, MonorepoGroup(m.Name)
		}
	}
	if defaultIgnored[d.DepType] {
		p.Ignore = true
		p.IgnoreReason = "ignored by default (" + d.DepType + "; enable with a rule setting ignore: false)"
	}
	for _, t := range dep.UpdateTypes {
		p.Updates[t] = true
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if !r.matches(d, kind) {
			continue
		}
		p.Rules = append(p.Rules, i)
		if r.Ignore != nil {
			p.Ignore = *r.Ignore
			p.IgnoreReason = fmt.Sprintf("ignored by rule #%d", i)
			if len(r.UpdateTypes) > 0 {
				p.IgnoreReason += " for " + string(kind)
			}
		}
		if r.Pin != "" {
			p.Pin = r.Pin
		}
		if r.RangeStrategy != "" {
			p.RangeStrategy = r.RangeStrategy
		}
		if r.allow != nil {
			p.Allow = r.allow
		}
		if r.Updates != nil {
			clear(p.Updates)
			for _, u := range r.Updates {
				p.Updates[dep.UpdateType(u)] = true
			}
		}
		if r.Group != "" {
			p.Group = r.Group
		}
		if r.Schedule != nil {
			p.Schedule = r.schedule
		}
		if r.CommitType != "" {
			p.CommitType = r.CommitType
		}
		if r.CommitScope != nil {
			p.CommitScope = *r.CommitScope
		}
		if r.minAge != nil {
			p.MinAge = *r.minAge
		}
		if r.Versioning != "" {
			p.Versioning = r.Versioning
		}
		if r.SeparateMajorMinor != nil {
			p.SeparateMajorMinor = *r.SeparateMajorMinor
		}
		if r.SeparateMultipleMajor != nil {
			p.SeparateMultipleMajor = *r.SeparateMultipleMajor
		}
		if r.SeparateMinorPatch != nil {
			p.SeparateMinorPatch = *r.SeparateMinorPatch
		}
		if r.Automerge != nil {
			p.Automerge = *r.Automerge
		}
		if r.AutomergeStrategy != "" {
			p.AutomergeStrategy = r.AutomergeStrategy
		}
	}
	return p
}

func matchAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, name); ok {
			return true
		}
	}
	return false
}

// Accepts reports whether an update of kind to version v is allowed; see Check.
func (p Policy) Accepts(kind dep.UpdateType, v string, semverOf func(string) *semver.Version) bool {
	ok, _ := p.Check(kind, v, semverOf)
	return ok
}

// Check is Accepts with the reason for a rejection: "update type <kind> not allowed"
// or "outside allow <allow>". semverOf converts v for a constraint allow and is only called
// then (Allowed.Check).
func (p Policy) Check(kind dep.UpdateType, v string, semverOf func(string) *semver.Version) (bool, string) {
	if !p.Updates[kind] {
		return false, "update type " + string(kind) + " not allowed"
	}
	if p.Allow != nil && !p.Allow.Check(v, semverOf) {
		return false, "outside allow " + p.Allow.String()
	}
	return true, ""
}
