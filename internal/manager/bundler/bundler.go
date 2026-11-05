// Package bundler extracts the gems of Ruby Gemfiles, reads the versions Gemfile.lock locks
// them to, and updates Gemfile.lock after an update with `bundle lock` (see Locker).
package bundler

import (
	"fmt"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "bundler"

// Datasource is the datasource of gems.
const Datasource = "rubygems"

// Versioning is the versioning scheme of gems.
const Versioning = "ruby"

// DepTypes.
const (
	DepTypeDependencies = "dependencies" // outside groups
	DepTypeGroup        = "group"        // in a `group … do` block or with a `group:`/`groups:` option
)

// Skip reasons.
const (
	skipGit              = "git dependency"
	skipPath             = "path dependency"
	skipNonLiteralSource = "non-literal source"
	skipNonLiteral       = "non-literal requirement"
	skipUnsupported      = "unsupported requirement"
	skipNoLock           = "no Gemfile.lock version for a requirement without a floor"
)

// anyVersion is the range of a gem without requirements (Gem::Requirement.default).
const anyVersion = ">= 0"

var matchRe = regexp.MustCompile(`(^|/)Gemfile$`)

type bundlerManager struct{}

// New returns the bundler manager.
func New() manager.Manager { return bundlerManager{} }

func (bundlerManager) Name() string { return Name }

// Match reports whether the slash path is a Gemfile. The suffix check skips the regexp for the
// paths that cannot match.
func (bundlerManager) Match(p string) bool {
	return strings.HasSuffix(p, "Gemfile") && matchRe.MatchString(p)
}

// Extract returns one Dep per `gem` statement, in file order. Range is the gem's requirements
// joined with ", " (`~> 7.1, >= 7.1.2`; `>= 0` without any); Version is the version of its
// first lower bound, "" without one (ReadLocks takes the locked version then). A single
// requirement with a lower bound (`2.241.2`, `= 1.2`, `~> 7.1`, `>= 1.2`, `> 1.2`) gets a
// RefSpan on its version, so the version is rewritten; any other requirement list is never
// rewritten (RangeFixed), only moved in Gemfile.lock.
func (bundlerManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	f := parse(src)
	global := strings.Join(f.sources, " ")
	deps := make([]dep.Dep, 0, len(f.gems))
	for _, g := range f.gems {
		d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepTypeDependencies, File: file, Line: g.line, Name: g.name, Source: global}
		if g.group {
			d.DepType = DepTypeGroup
		}
		if g.ownSource {
			d.Source = g.source
		}
		switch {
		case g.skip != "":
			d.Skip = g.skip
		case g.nonLiteral:
			d.Skip = skipNonLiteral
		default:
			setRequirements(&d, g.reqs)
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// floorOps are the operators of requirements with a lower bound, whose version a bump raises.
var floorOps = map[string]bool{"": true, "=": true, "~>": true, ">=": true, ">": true}

// setRequirements sets the Range, Version and RefSpan of d from its requirement strings.
func setRequirements(d *dep.Dep, reqs []token) {
	if len(reqs) == 0 {
		d.Range, d.RangeFixed = anyVersion, true
		return
	}
	parts := make([]string, len(reqs))
	var floor *versioning.RubyClause
	for i, r := range reqs {
		c, err := versioning.ParseRubyRequirement(r.text)
		if err != nil {
			d.Version, d.Skip = r.text, skipUnsupported
			return
		}
		parts[i] = strings.TrimSpace(r.text)
		if floor == nil && floorOps[c.Op] {
			floor = &c
			if len(reqs) == 1 {
				d.RefSpan = dep.Span{Start: r.vs + c.Off, End: r.vs + c.Off + len(c.Version)}
			}
		}
	}
	d.Range = strings.Join(parts, ", ")
	if floor != nil {
		d.Version = floor.Version
	}
	d.RangeFixed = d.RefSpan == (dep.Span{})
}

// Edits replaces the requirement's version: an exact one (`2.241.2`, `= 2.241.2`) with the new
// version, a pessimistic or lower bound (`~> 7.1`, `>= 1.2`) with the new version at its
// precision (versioning.FitRuby: `~> 7.1` → `~> 8.0` for 8.0.1).
func (bundlerManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.RangeFixed || d.RefSpan == (dep.Span{}) {
		return nil, fmt.Errorf("requirement %q is not rewritten", d.Range)
	}
	c, err := versioning.ParseRubyRequirement(d.Range)
	if err != nil {
		return nil, err
	}
	v := u.NewVersion
	if c.Op != "" && c.Op != "=" {
		v = versioning.FitRuby(d.Version, v)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: v}}, nil
}
