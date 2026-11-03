package config

import (
	"path"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Policy is the effective rule outcome for one dep.
type Policy struct {
	Ignore  bool
	Pin     string
	Allow   *semver.Constraints
	Updates map[dep.UpdateType]bool
}

// Policy evaluates rules top-to-bottom; each set field of a matching rule
// overwrites the policy (Updates is replaced, not merged).
func (c *Config) Policy(d dep.Dep) Policy {
	p := Policy{Pin: c.Pin, Updates: make(map[dep.UpdateType]bool, len(dep.UpdateTypes))}
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
		if r.Ignore != nil {
			p.Ignore = *r.Ignore
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
	return p.Updates[kind] && (p.Allow == nil || (v != nil && p.Allow.Check(v)))
}
