// Package bundler extracts the gems and the `ruby` directive of Ruby Gemfiles, reads the
// versions Gemfile.lock locks them to, and updates Gemfile.lock after an update with `bundle
// lock` (see Locker). It also holds the cocoapods manager: Podfiles are Ruby too, and pod
// requirements are Gem::Requirements (see NewCocoaPods and PodLocker).
package bundler

import (
	"fmt"
	"regexp"
	"slices"
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

// RubyDatasource is the datasource of the `ruby` directive (Ruby releases).
const RubyDatasource = "ruby-version"

// RubyName is the dep name of the `ruby` directive, shared with `.ruby-version` and the asdf
// and mise `ruby` tool.
const RubyName = "ruby"

// Versioning is the versioning scheme of gems and of the `ruby` directive.
const Versioning = "ruby"

// DepTypes.
const (
	DepTypeDependencies = "dependencies" // outside groups
	DepTypeGroup        = "group"        // in a `group … do` block or with a `group:`/`groups:` option
	DepTypeRuby         = "ruby"         // the `ruby` directive, a gemspec's required_ruby_version
	DepTypeRuntime      = "runtime"      // gemspec add_dependency, add_runtime_dependency
	DepTypeDevelopment  = "development"  // gemspec add_development_dependency
)

// Skip reasons.
const (
	skipGit              = "git dependency"
	skipPath             = "path dependency"
	skipNonLiteralSource = "non-literal source"
	skipNonLiteral       = "non-literal requirement"
	skipUnsupported      = "unsupported requirement"
	skipNoLock           = "no Gemfile.lock version for a requirement without a floor"
	skipRubyFile         = "version read from a file"
	skipRubyEngine       = "non-MRI engine"
	skipRubyPatchlevel   = "pinned patchlevel"
	skipRubyNoFloor      = "ruby requirement without a lower bound"
	skipRequiredRuby     = "required_ruby_version is the gem's supported range"
)

// anyVersion is the range of a gem without requirements (Gem::Requirement.default).
const anyVersion = ">= 0"

var matchRe = regexp.MustCompile(`(^|/)(Gemfile|gems\.rb|[^/]+\.gemspec)$`)

type bundlerManager struct{}

// New returns the bundler manager.
func New() manager.Manager { return bundlerManager{} }

func (bundlerManager) Name() string { return Name }

// Match reports whether the slash path is a Gemfile, its alternative name gems.rb, or a
// gemspec. The suffix checks skip the regexp for the paths that cannot match.
func (bundlerManager) Match(p string) bool {
	return (strings.HasSuffix(p, "Gemfile") || strings.HasSuffix(p, "gems.rb") || strings.HasSuffix(p, ".gemspec")) && matchRe.MatchString(p)
}

// isGemspec reports whether the slash path is a gemspec.
func isGemspec(p string) bool { return strings.HasSuffix(p, ".gemspec") }

// Extract returns one Dep per `gem` statement and one for the `ruby` directive, in file order.
// Range is the gem's requirement strings as written, from the first quote to the last
// (`"~> 7.1", ">= 7.1.2"`, as Renovate's currentValue; RefSpan its bytes), Constraint the
// requirements joined with ", " (`~> 7.1, >= 7.1.2`), which the ruby versioning matches; a gem
// without any has Range `>= 0` and is never rewritten (RangeFixed), only moved in Gemfile.lock.
// Version is the version of its first lower bound, "" without one (ReadLocks takes the locked
// version then). Edits and NewRange rewrite the requirements per rangeStrategy (reqList.rewrite).
// The `ruby` directive is the dep RubyName of RubyDatasource with its requirements read the
// same way. A gemspec gets extractGemspec's deps.
func (bundlerManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if isGemspec(file) {
		return extractGemspec(file, src), nil
	}
	f := parse(src)
	global := strings.Join(f.sources, " ")
	deps := make([]dep.Dep, 0, len(f.gems)+1)
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
			setRequirements(&d, src, g.reqs)
		}
		deps = append(deps, d)
	}
	if r := f.ruby; r != nil {
		d := rubyDep(file, src, r)
		i, _ := slices.BinarySearchFunc(deps, d.Line, func(g dep.Dep, line int) int { return g.Line - line })
		deps = slices.Insert(deps, i, d)
	}
	return deps, nil
}

// extractGemspec returns one Dep per dependency of a gemspec (DepType runtime or development,
// looked up on rubygems.org, requirements as for a Gemfile gem) and a skipped one for its
// required_ruby_version, in file order.
func extractGemspec(file string, src []byte) []dep.Dep {
	f := parseGemspec(src)
	deps := make([]dep.Dep, 0, len(f.deps)+1)
	for _, g := range f.deps {
		d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepTypeRuntime, File: file, Line: g.line, Name: g.name}
		if g.group {
			d.DepType = DepTypeDevelopment
		}
		if g.nonLiteral {
			d.Skip = skipNonLiteral
		} else {
			setRequirements(&d, src, g.reqs)
		}
		deps = append(deps, d)
	}
	if f.rubyLine > 0 {
		parts := make([]string, len(f.rubyReqs))
		for i, r := range f.rubyReqs {
			parts[i] = strings.TrimSpace(r.text)
		}
		d := dep.Dep{Manager: Name, Datasource: RubyDatasource, Versioning: Versioning, DepType: DepTypeRuby, File: file, Line: f.rubyLine, Name: RubyName, Range: strings.Join(parts, ", "), Skip: skipRequiredRuby}
		i, _ := slices.BinarySearchFunc(deps, d.Line, func(g dep.Dep, line int) int { return g.Line - line })
		deps = slices.Insert(deps, i, d)
	}
	return deps
}

// rubyDep is the dep of the `ruby` directive r of file (src).
func rubyDep(file string, src []byte, r *rubyDecl) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: RubyDatasource, Versioning: Versioning, DepType: DepTypeRuby, File: file, Line: r.line, Name: RubyName}
	switch {
	case r.skip != "":
		d.Skip = r.skip
	case r.nonLiteral:
		d.Skip = skipNonLiteral
	case len(r.reqs) == 0:
		d.Skip = skipRubyNoFloor
	default:
		setRequirements(&d, src, r.reqs)
		if d.Skip == "" && d.Version == "" {
			d.Skip = skipRubyNoFloor
		}
	}
	return d
}

// floorOps are the operators of requirements with a lower bound, whose version is the dep's.
var floorOps = map[string]bool{"": true, "=": true, "~>": true, ">=": true, ">": true}

// setRequirements sets the Range, Constraint, Version and RefSpan of d from its requirement
// strings in src.
func setRequirements(d *dep.Dep, src []byte, reqs []token) {
	if len(reqs) == 0 {
		d.Range, d.RangeFixed = anyVersion, true
		return
	}
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		c, err := versioning.ParseRubyRequirement(r.text)
		if err != nil {
			d.Version, d.Skip = r.text, skipUnsupported
			return
		}
		parts[i] = strings.TrimSpace(r.text)
		if d.Version == "" && floorOps[c.Op] {
			d.Version = c.Version
		}
	}
	first, last := reqs[0], reqs[len(reqs)-1]
	d.Range, d.Constraint = string(src[first.start:last.end]), strings.Join(parts, ", ")
	d.RefSpan = dep.Span{Start: first.start, End: last.end}
}

// Edits rewrites the requirement strings for the new version under u.RangeStrategy
// (reqList.rewrite): only the strings that change, and of those only the version when the
// operator stays (`~> 7.1` → `~> 8.0` for 8.0.1).
func (bundlerManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	l := parseReqs(d.Range)
	if d.RangeFixed || l == nil {
		return nil, fmt.Errorf("requirement %s is not rewritten", d.Range)
	}
	edits, ok := l.rewrite(d.Range, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("requirement %s cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	for i := range edits {
		edits[i].Start += d.RefSpan.Start
		edits[i].End += d.RefSpan.Start
	}
	return edits, nil
}

// NewRange implements manager.RangeRewriter (reqList.rewrite).
func (bundlerManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	l := parseReqs(d.Range)
	if d.RangeFixed || l == nil {
		return "", false
	}
	edits, ok := l.rewrite(d.Range, v, strategy)
	if !ok {
		return "", false
	}
	out, err := patch.Apply([]byte(d.Range), edits)
	if err != nil {
		return "", false
	}
	return string(out), true
}
