package config

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
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
	// IgnoredByDefault: Ignore is the dep type's default (IgnoredByDefault), no rule set ignore.
	IgnoredByDefault bool
	Pin              string
	// RangeStrategy is the npm and Python range strategy (RangeStrategies); never "" in a Policy.
	RangeStrategy string
	Allow         *Allowed
	// AllowedUpdateTypes are the allowed update types. The map is shared between policies
	// and must not be modified.
	AllowedUpdateTypes map[dep.UpdateType]bool
	Group              string        // "" = own PR
	SecurityGroup      string        // group of the dep's security updates, "" = own PR each
	Schedule           *schedule.Set // nil = always
	Rules              []int         // indices of the matching user rules, in order
	Monorepo           string        // built-in monorepo that set the initial Group, "" = none
	Toolchain          string        // built-in toolchain group (RubyGroup) that set the initial Group, "" = none
	Registry           string        // built-in registry group (VcpkgGroup) that set the initial Group, "" = none
	CommitType         string        // conventional commit type: fix for production deps, chore otherwise
	CommitScope        string        // conventional commit scope, "" = none
	// Templates are the prTitle, prHeader and prFooter templates; nil members use the defaults.
	Templates prtemplate.Set
	Kind      dep.UpdateType // update type of a PolicyFor result, "" for Policy
	MinAge    time.Duration  // minimum release age of new versions, 0 = off
	// TimestampRequired (minimumReleaseAgeBehaviour timestamp-required): with a MinAge, a
	// version whose release time is unknown is held back.
	TimestampRequired bool
	// AbandonAfter (abandonmentThreshold) flags the dep as abandoned when its newest release is
	// older; 0 = off.
	AbandonAfter   time.Duration
	Versioning     string // versioning scheme set by a rule, "" = the dep's own
	ExtractVersion string // extractVersion set by a rule, "" = the dep's own
	// ReplacementName is the package a replacement update moves the dep to (a rule's
	// replacementName, or the built-in Replacement), "" = none; ReplacementVersion its version,
	// "" = the current one.
	ReplacementName, ReplacementVersion string
	Replacement                         string // built-in replacement that set ReplacementName, "" = none or a rule
	Automerge                           bool   // automerge the update's PR or branch
	// AutomergeStrategy is the merge method of an automerged PR (AutomergeStrategies).
	AutomergeStrategy string
	// AutomergeType is how the update is automerged (AutomergeTypes).
	AutomergeType string
	// DashboardApproval holds back the update's new PR until it is approved on the dashboard.
	DashboardApproval bool
	// DraftPR opens the update's new PR as a draft; PRCreation is when it is opened
	// (PRCreations).
	DraftPR    bool
	PRCreation string
	// Labels, Reviewers and Assignees go on the update's new PR.
	Labels, Reviewers, Assignees []string
	// Tasks are the postUpgradeTasks of the last matching rule that sets them.
	Tasks dep.Tasks
	// PR buckets (separateMajorMinor, separateMultipleMajor, separateMinorPatch).
	SeparateMajorMinor, SeparateMultipleMajor, SeparateMinorPatch bool
	// AdditionalBranchPrefix is the additionalBranchPrefix template (ExpandBranchPrefix).
	AdditionalBranchPrefix string
}

// production reports whether d is a production dep, which gets the fix commit type by
// default (Renovate's :semanticPrefixFixDepsChoreOthers).
func production(d dep.Dep) bool {
	switch d.Manager {
	case "gomod":
		return d.DepType == "direct"
	case "npm", "cargo", "poetry", "pub":
		return d.DepType == "dependencies"
	case "bundler":
		return d.DepType == "dependencies" || d.DepType == "runtime"
	case "composer":
		return d.DepType == "require"
	case "julia":
		return d.DepType == "deps"
	case "mix":
		return d.DepType == "prod"
	case "gleam":
		return d.DepType == "dependencies"
	case "pep621", "pep723":
		return d.DepType == "project.dependencies"
	case "setup-cfg", "pip_setup":
		return d.DepType == "install"
	case "pipenv":
		return d.DepType == "packages"
	case "elm":
		return d.DepType == "direct" || d.DepType == "dependencies"
	case "maven":
		return mavenProduction[d.DepType]
	}
	return false
}

// mavenProduction are the maven dep types that ship with the artifact (Renovate: maven deps of
// these types use fix).
var mavenProduction = map[string]bool{"compile": true, "provided": true, "runtime": true, "system": true, "import": true, "parent": true}

// defaultIgnored are the dep types ignored unless a rule sets `ignore: false`: git submodules
// are disabled by default as in Renovate, and so are Azure Pipelines tasks (Renovate disables
// the azure-pipelines manager: a public task version may not be installed in the organization);
// indirect and test-indirect are the transitive pins of Go modules, compiled requirements and Elm
// applications; uv's constraint, override and build constraint lists usually cap versions on
// purpose (Renovate does not extract them).
var defaultIgnored = map[string]bool{"indirect": true, "test-indirect": true, "go": true, "peerDependencies": true, "engines": true, "submodule": true, "task": true,
	"tool.uv.constraint-dependencies": true, "tool.uv.override-dependencies": true, "tool.uv.build-constraint-dependencies": true}

// allUpdates allows every update type; policies share it until a rule sets allowedUpdateTypes.
var allUpdates = updateSet(nil)

// nonMajorUpdates allows every update type but major: the default of travis deps, whose
// `node_js` lists are test matrices (Renovate's travis manager disables major updates too).
var nonMajorUpdates = updateSet([]string{string(dep.Minor), string(dep.Patch), string(dep.Digest), string(dep.Pin)})

// updateSet returns the set of types, or of every update type when types is nil.
func updateSet(types []string) map[dep.UpdateType]bool {
	if types == nil {
		m := make(map[dep.UpdateType]bool, len(dep.UpdateTypes))
		for _, t := range dep.UpdateTypes {
			m[t] = true
		}
		return m
	}
	m := make(map[dep.UpdateType]bool, len(types))
	for _, u := range types {
		m[dep.UpdateType(u)] = true
	}
	return m
}

// IgnoredByDefault reports whether deps of depType are ignored unless a rule sets
// `ignore: false`.
func IgnoredByDefault(depType string) bool { return defaultIgnored[depType] }

// UnversionedReason is the ignore reason of an action or pre-commit hook repository pinned to a
// commit no tag points at (UnversionedPin).
const UnversionedReason = "ignored by default (commit pin without version; enable with a rule setting ignore: false)"

// UnversionedPin reports whether d is an action or a pre-commit hook repository pinned to a
// bare commit SHA without a version (Renovate's unversioned-reference): ignored unless a rule
// sets `ignore: false`. The engine resolves a commit a tag points at to that tag's version
// first, which is not ignored.
func UnversionedPin(d dep.Dep) bool {
	return (d.Manager == "github-actions" && d.DepType == "action" || d.Manager == "pre-commit" && d.DepType == "repository") &&
		d.Digest != "" && d.Version == ""
}

// Policy is the pre-policy of d: the policy before the type of an update is known. It
// evaluates the built-in monorepo and Ruby group rules (unless monorepos is false), then the
// user rules without updateTypes top-to-bottom; each set apply field of a matching rule
// overwrites the policy (allowedUpdateTypes is replaced, not merged). Indirect Go requirements
// and pip-compile pins, go directives, npm peerDependencies and engines, actions and pre-commit
// hook repositories pinned to a bare SHA (UnversionedPin), git submodules and Azure Pipelines
// tasks are ignored unless a
// rule sets `ignore: false`. Its Versioning and ExtractVersion replace the dep's before
// resolution (only rules without updateTypes may set them). The age of the current version is
// unknown, so no currentAge rule matches (see PolicyAt).
func (c *Config) Policy(d dep.Dep) Policy { return c.policy(d, "", UnknownAge) }

// PolicyFor is the policy of an update of kind to d: like Policy, but rules with updateTypes
// are evaluated too, in file order, when they list kind. Without such rules it equals Policy
// (apart from Kind). The engine buckets each candidate with the Separate* flags of the policy
// of its kind, so an updateTypes rule can change them for its types.
func (c *Config) PolicyFor(d dep.Dep, kind dep.UpdateType) Policy {
	return c.policy(d, kind, UnknownAge)
}

// PolicyAt is PolicyFor (Policy for kind "") with age, the age of d's current version, for
// currentAge rules; UnknownAge (any negative age) matches none of them.
func (c *Config) PolicyAt(d dep.Dep, kind dep.UpdateType, age time.Duration) Policy {
	return c.policy(d, kind, age)
}

// HasUpdateTypeRules reports whether any rule sets updateTypes, i.e. whether PolicyFor can
// differ from Policy.
func (c *Config) HasUpdateTypeRules() bool {
	return slices.ContainsFunc(c.Rules, func(r Rule) bool { return len(r.UpdateTypes) > 0 })
}

// WantsCurrentAge reports whether the policies of d depend on the age of its current version:
// a rule with currentAge matches d on its other selectors (updateTypes aside). Only then is
// the release time of the current version worth a lookup.
func (c *Config) WantsCurrentAge(d dep.Dep) bool {
	for i := range c.Rules {
		if r := &c.Rules[i]; r.currentAge != nil && c.matchesDep(r, d) {
			return true
		}
	}
	return false
}

// matches reports whether r applies to d; kind "" skips rules with updateTypes, a negative
// age those with currentAge.
func (c *Config) matches(r *Rule, d dep.Dep, kind dep.UpdateType, age time.Duration) bool {
	switch {
	case len(r.UpdateTypes) > 0 && (kind == "" || !slices.Contains(r.UpdateTypes, string(kind))),
		r.currentAge != nil && (age < 0 || !r.currentAge.matches(age)):
		return false
	}
	return c.matchesDep(r, d)
}

// matchesDep reports whether the match fields of r other than updateTypes and currentAge
// match d on the base branch of c (ForBaseBranch; rules with baseBranches match nothing
// without one).
func (c *Config) matchesDep(r *Rule, d dep.Dep) bool {
	switch {
	case len(r.Managers) > 0 && !slices.Contains(r.Managers, d.Manager),
		len(r.Names) > 0 && !r.names.Match(d.Name),
		len(r.DepTypes) > 0 && !slices.Contains(r.DepTypes, d.DepType),
		len(r.Paths) > 0 && !r.paths.Match(d.File),
		len(r.Datasources) > 0 && !slices.ContainsFunc(r.Datasources, func(ds string) bool { return CanonicalDatasource(ds) == d.Datasource }),
		len(r.BaseBranches) > 0 && (c.baseBranch == "" || !matchBranches(r.baseBranches, c.baseBranch)):
		return false
	}
	return r.currentVersion == nil || c.currentMatches(r.currentVersion, d)
}

// currentMatches reports whether d's current version (the locked one when known) satisfies a,
// converted with the versioning scheme d is resolved with.
func (c *Config) currentMatches(a *Allowed, d dep.Dep) bool {
	return a.Check(cmp.Or(d.Locked, d.Version), func(v string) *semver.Version {
		s, ok := versioning.Get(c.versioningOf(d))
		if !ok {
			return nil
		}
		return s.Semver(v)
	})
}

// versioningOf is the versioning scheme d is resolved with: the last matching rule's
// versioning, else d's own. Rules with versioning have neither currentVersion nor currentAge,
// so matching them does not recurse.
func (c *Config) versioningOf(d dep.Dep) string {
	v := d.Versioning
	for i := range c.Rules {
		if r := &c.Rules[i]; r.Versioning != "" && c.matches(r, d, "", UnknownAge) {
			v = r.Versioning
		}
	}
	return v
}

// RegistryURLs returns the registry URLs (whitespace-separated, the dep Source form) the last
// matching rule with registryUrls gives d, "" when none does or d's datasource takes none
// (RegistryURLDatasources). Rules with registryUrls have neither updateTypes nor currentAge,
// so it holds before the update type and the age of the current version are known.
func (c *Config) RegistryURLs(d dep.Dep) string {
	if !slices.Contains(RegistryURLDatasources, d.Datasource) {
		return ""
	}
	var urls []string
	for i := range c.Rules {
		if r := &c.Rules[i]; len(r.RegistryURLs) > 0 && c.matches(r, d, "", UnknownAge) {
			urls = r.RegistryURLs
		}
	}
	return strings.Join(urls, " ")
}

// UnusedRules returns the indices of the rules that match none of deps (the deps of a run,
// skipped ones included); updateTypes and currentAge are not considered, as a rule matching
// a dep applies to it once an update of a listed type exists or its release time is known.
func (c *Config) UnusedRules(deps []dep.Dep) []int {
	var out []int
	for i := range c.Rules {
		r := &c.Rules[i]
		if len(r.BaseBranches) > 0 && c.baseBranch == "" {
			continue // without a base branch it cannot match; that says nothing about the rule
		}
		if !slices.ContainsFunc(deps, func(d dep.Dep) bool { return c.matchesDep(r, d) }) {
			out = append(out, i)
		}
	}
	return out
}

// DefaultPolicy is the policy of the top-level settings alone, before the dep is known: no
// built-in group, replacement or rule applies. Lock file maintenance updates use it.
func (c *Config) DefaultPolicy() Policy {
	p := Policy{
		RangeStrategy:      RangeAuto,
		AllowedUpdateTypes: allUpdates,
		CommitType:         CommitChore,
		CommitScope:        DefaultScope,
		SeparateMajorMinor: true,
		AutomergeStrategy:  StrategyAuto,
		AutomergeType:      AutomergeTypePR,
		PRCreation:         PRCreationImmediate,
	}
	p.apply(&c.Settings)
	return p
}

// apply overwrites p with the settings s sets: the top level's, then each matching rule's.
func (p *Policy) apply(s *Settings) {
	p.Pin = cmp.Or(s.Pin, p.Pin)
	p.RangeStrategy = cmp.Or(s.RangeStrategy, p.RangeStrategy)
	if s.Schedule != nil {
		p.Schedule = s.schedule
	}
	setPtr(&p.AdditionalBranchPrefix, s.AdditionalBranchPrefix)
	setPtr(&p.MinAge, s.minAge)
	if s.MinimumReleaseAgeBehaviour != "" {
		p.TimestampRequired = s.MinimumReleaseAgeBehaviour == TimestampRequired
	}
	setPtr(&p.AbandonAfter, s.abandon)
	setPtr(&p.SeparateMajorMinor, s.SeparateMajorMinor)
	setPtr(&p.SeparateMultipleMajor, s.SeparateMultipleMajor)
	setPtr(&p.SeparateMinorPatch, s.SeparateMinorPatch)
	setPtr(&p.Automerge, s.Automerge)
	p.AutomergeStrategy = cmp.Or(s.AutomergeStrategy, p.AutomergeStrategy)
	p.AutomergeType = cmp.Or(s.AutomergeType, p.AutomergeType)
	setPtr(&p.DraftPR, s.DraftPR)
	p.PRCreation = cmp.Or(s.PRCreation, p.PRCreation)
	setList(&p.Labels, s.Labels)
	setList(&p.Reviewers, s.Reviewers)
	setList(&p.Assignees, s.Assignees)
	if s.PRTitle != nil {
		p.Templates.Title = s.templates.Title
	}
	if s.PRHeader != nil {
		p.Templates.Header = s.templates.Header
	}
	if s.PRFooter != nil {
		p.Templates.Footer = s.templates.Footer
	}
	setPtr(&p.DashboardApproval, s.DashboardApproval)
}

// setPtr sets *dst to *v when v is not nil.
func setPtr[T any](dst, v *T) {
	if v != nil {
		*dst = *v
	}
}

// setList sets *dst to v when v is not nil ([] clears it).
func setList(dst *[]string, v []string) {
	if v != nil {
		*dst = v
	}
}

func (c *Config) policy(d dep.Dep, kind dep.UpdateType, age time.Duration) Policy {
	p := c.DefaultPolicy()
	p.Kind = kind
	if production(d) {
		p.CommitType = CommitFix
	}
	if d.Manager == "travis" {
		p.AllowedUpdateTypes = nonMajorUpdates
	}
	if c.MonoreposEnabled() {
		switch m := monorepoOf(d.Manager, d.Name); {
		case m != nil:
			p.Monorepo, p.Group = m.Name, MonorepoGroup(m.Name)
		case d.Datasource == rubyDatasource:
			p.Toolchain, p.Group = RubyGroup, RubyGroup
		case d.Manager == vcpkgManager:
			p.Registry, p.Group = VcpkgGroup, VcpkgGroup
		}
	}
	if c.ReplacementsEnabled() {
		if rp := c.replacementOf(d); rp != nil {
			p.Replacement, p.ReplacementName, p.ReplacementVersion = rp.Name, rp.NewName(packageName(d)), rp.Version
		}
	}
	switch {
	case defaultIgnored[d.DepType]:
		p.Ignore, p.IgnoredByDefault = true, true
		p.IgnoreReason = "ignored by default (" + d.DepType + "; enable with a rule setting ignore: false)"
	case UnversionedPin(d):
		p.Ignore, p.IgnoredByDefault, p.IgnoreReason = true, true, UnversionedReason
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if !c.matches(r, d, kind, age) {
			continue
		}
		p.Rules = append(p.Rules, i)
		if r.ReplacementName != "" && strings.HasPrefix(d.Name, r.ReplacementPrefix) {
			name := r.ReplacementName
			if r.ReplacementPrefix != "" {
				name += d.Name[len(r.ReplacementPrefix):]
			}
			p.Replacement, p.ReplacementName, p.ReplacementVersion = "", name, r.ReplacementVersion
		}
		if r.Ignore != nil {
			p.Ignore, p.IgnoredByDefault = *r.Ignore, false
			p.IgnoreReason = fmt.Sprintf("ignored by rule #%d", i)
			if len(r.UpdateTypes) > 0 {
				p.IgnoreReason += " for " + string(kind)
			}
		}
		if r.allow != nil {
			p.Allow = r.allow
		}
		if r.AllowedUpdateTypes != nil {
			p.AllowedUpdateTypes = r.allowedUpdateTypes
		}
		if r.Group != "" {
			p.Group = r.Group
		}
		if r.SecurityGroup != "" {
			p.SecurityGroup = r.SecurityGroup
		}
		if r.CommitType != "" {
			p.CommitType = r.CommitType
		}
		if r.CommitScope != nil {
			p.CommitScope = *r.CommitScope
		}
		if r.Versioning != "" {
			p.Versioning = r.Versioning
		}
		if r.ExtractVersion != "" {
			p.ExtractVersion = r.ExtractVersion
		}
		p.apply(&r.Settings)
		for _, l := range r.AddLabels {
			if !slices.Contains(p.Labels, l) {
				p.Labels = append(slices.Clip(p.Labels), l)
			}
		}
		if r.PostUpgradeTasks != nil {
			p.Tasks = dep.Tasks{Commands: r.PostUpgradeTasks.Commands, Update: r.PostUpgradeTasks.ExecutionMode == ExecutionUpdate, Files: r.PostUpgradeTasks.Files}
		}
	}
	return p
}

// MatchGlobs reports whether name matches globs as Renovate matches pattern lists: no entry
// starting with '!' matches it with the rest of the entry, and some other entry matches it,
// unless every entry is such an exclusion.
func MatchGlobs(globs []string, name string) bool {
	positive, hit := false, false
	for _, g := range globs {
		if pat, neg := strings.CutPrefix(g, "!"); neg {
			if ok, _ := doublestar.Match(pat, name); ok {
				return false
			}
			continue
		}
		positive = true
		if !hit {
			hit, _ = doublestar.Match(g, name)
		}
	}
	return hit || !positive
}

// Check reports whether an update of kind to version v is allowed, with the reason for
// a rejection: "update type <kind> not allowed"
// or "outside allow <allow>". semverOf converts v for a constraint allow and is only called
// then (Allowed.Check).
func (p Policy) Check(kind dep.UpdateType, v string, semverOf func(string) *semver.Version) (bool, string) {
	if !p.AllowedUpdateTypes[kind] {
		return false, "update type " + string(kind) + " not allowed"
	}
	if p.Allow != nil && !p.Allow.Check(v, semverOf) {
		return false, "outside allow " + p.Allow.String()
	}
	return true, ""
}
