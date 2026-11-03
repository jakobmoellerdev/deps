package config

import (
	"fmt"
	"path"
	"slices"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// Policy is the effective rule outcome for one dep.
type Policy struct {
	Ignore       bool
	IgnoreReason string // why Ignore is set: default for the dep type, or the deciding rule
	Pin          string
	Allow        *semver.Constraints
	Updates      map[dep.UpdateType]bool
	Group        string        // "" = own PR
	Schedule     *schedule.Set // nil = always
	Rules        []int         // indices of the matching rules, in order
}

// defaultIgnored are the dep types ignored unless a rule sets `ignore: false`.
var defaultIgnored = map[string]bool{"indirect": true, "go": true, "peerDependencies": true}

// Policy evaluates rules top-to-bottom; each set field of a matching rule
// overwrites the policy (Updates is replaced, not merged). Indirect Go requirements,
// go directives and npm peerDependencies are ignored unless a rule sets `ignore: false`.
func (c *Config) Policy(d dep.Dep) Policy {
	p := Policy{
		Pin:      c.Pin,
		Updates:  make(map[dep.UpdateType]bool, len(dep.UpdateTypes)),
		Schedule: c.schedule,
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
		if r.Manager != "" && r.Manager != d.Manager {
			continue
		}
		if len(r.Names) > 0 && !matchAny(r.Names, d.Name) {
			continue
		}
		if len(r.DepTypes) > 0 && !slices.Contains(r.DepTypes, d.DepType) {
			continue
		}
		p.Rules = append(p.Rules, i)
		if r.Ignore != nil {
			p.Ignore = *r.Ignore
			p.IgnoreReason = fmt.Sprintf("ignored by rule #%d", i)
		}
		if r.Pin != "" {
			p.Pin = r.Pin
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
	}
	return p
}

func matchAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

// Accepts reports whether an update of kind to version v is allowed.
func (p Policy) Accepts(kind dep.UpdateType, v *semver.Version) bool {
	ok, _ := p.Check(kind, v)
	return ok
}

// Check is Accepts with the reason for a rejection: "update type <kind> not allowed"
// or "outside allow <constraint>".
func (p Policy) Check(kind dep.UpdateType, v *semver.Version) (bool, string) {
	if !p.Updates[kind] {
		return false, "update type " + string(kind) + " not allowed"
	}
	if p.Allow != nil && (v == nil || !p.Allow.Check(v)) {
		return false, "outside allow " + p.Allow.String()
	}
	return true, ""
}
