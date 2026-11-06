// Package elm extracts the package dependencies and the compiler version of Elm projects
// (`elm.json`): the exact versions of an application and the constraints of a package.
package elm

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Dependabot's package ecosystem).
const Name = "elm"

// Dep types. An application lists its dependencies by `direct` and `indirect`, in
// `dependencies` and `test-dependencies`; a package lists constraints in `dependencies` and
// `test-dependencies`. `elm-version` is the compiler version (application) or constraint
// (package).
const (
	DepTypeDirect           = "direct"
	DepTypeIndirect         = "indirect"
	DepTypeTestDirect       = "test-direct"
	DepTypeTestIndirect     = "test-indirect"
	DepTypeDependencies     = "dependencies"
	DepTypeTestDependencies = "test-dependencies"
	DepTypeElmVersion       = "elm-version"
)

// Datasources and versioning.
const (
	Datasource         = "elm-package"
	CompilerDatasource = "github-releases"
	Versioning         = "semver"
	compilerName       = "elm/compiler"
	compilerSource     = "https://github.com/elm/compiler"
)

// Skip reasons.
const (
	skipVersion    = "unsupported version"
	skipConstraint = "unsupported constraint"
	skipName       = "invalid package name"
)

// Project types (the `type` field).
const (
	typeApplication = "application"
	typePackage     = "package"
)

type elmManager struct{}

// New returns the elm manager.
func New() manager.Manager { return elmManager{} }

func (elmManager) Name() string { return Name }

// Match reports whether the slash path is an elm.json file.
func (elmManager) Match(p string) bool {
	return p == "elm.json" || strings.HasSuffix(p, "/elm.json")
}

// Extract returns the deps of an application or package elm.json in source order: the
// `elm-version` and every entry of `dependencies` and `test-dependencies`. An application's
// versions are exact (RefSpan on the version); a package's constraints
// (`1.0.0 <= v < 2.0.0`) are the dep's Range with RefSpan on all of it and Version its lower
// bound. A file whose `type` is neither has no deps.
func (elmManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	x := extractor{file: file, src: src, dec: jsontext.NewDecoder(bytes.NewReader(src)), line: 1}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return x.finish(), nil
}

// Edits replaces an exact version, or rewrites a constraint for the new version under the
// update's rangeStrategy (constraint.rewrite).
func (elmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	c, ok := parseConstraint(d.Range)
	if !ok {
		return nil, fmt.Errorf("unsupported constraint %q", d.Range)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: c.rewrite(u.NewVersion, u.RangeStrategy)}}, nil
}

// NewRange implements manager.RangeRewriter (constraint.rewrite).
func (elmManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	c := cachedConstraint(d.Range)
	if c == nil {
		return "", false
	}
	out := c.rewrite(v, strategy)
	if out == d.Range {
		return "", false
	}
	nc, ok := parseConstraint(out)
	return out, ok && nc.admits(v)
}

// constraints caches cachedConstraint by constraint string (nil when it does not parse): the
// engine rewrites a dep's constraint for every candidate release.
var constraints sync.Map // string -> *constraint

// cachedConstraint returns the parsed constraint s, or nil.
func cachedConstraint(s string) *constraint {
	if p, ok := constraints.Load(s); ok {
		return p.(*constraint)
	}
	var p *constraint
	if c, ok := parseConstraint(s); ok {
		p = &c
	}
	got, _ := constraints.LoadOrStore(s, p)
	return got.(*constraint)
}

// constraintRe is Elm's constraint grammar (`1.0.0 <= v < 2.0.0`): a lower bound, `<` or
// `<=`, `v`, `<` or `<=`, an upper bound; the compiler writes single spaces.
var constraintRe = regexp.MustCompile(`^\s*(\d+\.\d+\.\d+)\s*(<=?)\s*v\s*(<=?)\s*(\d+\.\d+\.\d+)\s*$`)

// constraint is a parsed Elm constraint; loInc and hiInc are whether the bounds are `<=`.
type constraint struct {
	lo, hi       *semver.Version
	loInc, hiInc bool
}

// parseConstraint parses an Elm constraint; bounds must be MAJOR.MINOR.PATCH.
func parseConstraint(s string) (constraint, bool) {
	m := constraintRe.FindStringSubmatch(s)
	if m == nil {
		return constraint{}, false
	}
	lo, err1 := semver.StrictNewVersion(m[1])
	hi, err2 := semver.StrictNewVersion(m[4])
	if err1 != nil || err2 != nil {
		return constraint{}, false
	}
	return constraint{lo: lo, hi: hi, loInc: m[2] == "<=", hiInc: m[3] == "<="}, true
}

// masterminds returns the constraint as a Masterminds semver constraint (dep.Dep.Constraint).
func (c constraint) masterminds() string {
	lo, hi := ">", "<"
	if c.loInc {
		lo = ">="
	}
	if c.hiInc {
		hi = "<="
	}
	return lo + c.lo.String() + " " + hi + c.hi.String()
}

// admits reports whether v satisfies the constraint.
func (c constraint) admits(v string) bool {
	nv, err := semver.StrictNewVersion(v)
	if err != nil {
		return false
	}
	l, h := nv.Compare(c.lo), nv.Compare(c.hi)
	return (l > 0 || l == 0 && c.loInc) && (h < 0 || h == 0 && c.hiInc)
}

// String writes the constraint as elm writes it.
func (c constraint) String() string {
	lo, hi := "<", "<"
	if c.loInc {
		lo = "<="
	}
	if c.hiInc {
		hi = "<="
	}
	return c.lo.String() + " " + lo + " v " + hi + " " + c.hi.String()
}

// rewrite returns the constraint for v under strategy (a config.RangeStrategies value; "" is
// the default rewrite). pin writes `v <= v <= v`. Otherwise an upper bound v reaches moves
// past it: an inclusive one to v, an exclusive one to the next boundary of its precision above
// v (`< 2.0.0` → `< 3.0.0` for 2.1.0, `< 1.5.0` → `< 1.8.0` for 1.7.2). widen keeps the lower
// bound (Dependabot's update), every other strategy sets it to `v <=`. A v that is not a
// MAJOR.MINOR.PATCH version keeps the constraint.
func (c constraint) rewrite(v, strategy string) string {
	nv, err := semver.StrictNewVersion(v)
	if err != nil {
		return c.String()
	}
	if strategy == dep.RangePin {
		return constraint{lo: nv, hi: nv, loInc: true, hiInc: true}.String()
	}
	out := c
	if strategy != dep.RangeWiden {
		out.lo, out.loInc = nv, true
	}
	switch h := nv.Compare(c.hi); {
	case c.hiInc && h > 0:
		out.hi = nv
	case !c.hiInc && h >= 0:
		out.hi = boundary(c.hi, nv)
	}
	return out.String()
}

// boundary returns the exclusive upper bound above v at the precision of the bound old it
// replaces: the next major for `X.0.0`, the next minor for `X.Y.0`, else the next patch.
func boundary(old, v *semver.Version) *semver.Version {
	switch {
	case old.Minor() == 0 && old.Patch() == 0:
		return semver.New(v.Major()+1, 0, 0, "", "")
	case old.Patch() == 0:
		return semver.New(v.Major(), v.Minor()+1, 0, "", "")
	}
	return semver.New(v.Major(), v.Minor(), v.Patch()+1, "", "")
}

// nameRe is an Elm package name: a GitHub user and a lower-case project name.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[a-z0-9][a-z0-9-]*$`)

// entry is a string member read before the project type is known.
type entry struct {
	depType, name, value string
	span                 dep.Span // the value's content; zero when it has escapes
	line                 int
}

type extractor struct {
	file string
	src  []byte
	dec  *jsontext.Decoder

	typ     string
	entries []entry

	pos, line int // line is the 1-based line of offset pos
}

func (x *extractor) run() error {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return errors.New("top level is not an object")
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		switch key {
		case "type":
			var e entry
			if e, err = x.value(""); err == nil {
				x.typ = e.value
			}
		case "elm-version":
			var e entry
			if e, err = x.value(DepTypeElmVersion); err == nil && e.depType != "" {
				e.name = compilerName
				x.entries = append(x.entries, e)
			}
		case "dependencies", "test-dependencies":
			err = x.section(key, 0)
		default:
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err = x.dec.ReadToken()
	return err
}

// section reads a dependency object: `{name: version}` (package), or `{direct: {…},
// indirect: {…}}` (application) at depth 0. Other members are skipped.
func (x *extractor) section(key string, depth int) error {
	if x.dec.PeekKind() != '{' {
		return x.dec.SkipValue()
	}
	if _, err := x.dec.ReadToken(); err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		name, err := x.key()
		if err != nil {
			return err
		}
		switch {
		case x.dec.PeekKind() == '{' && depth == 0 && (name == "direct" || name == "indirect"):
			err = x.section(key+"."+name, 1)
		case x.dec.PeekKind() == '"':
			var e entry
			if e, err = x.value(key); err == nil {
				e.name = name
				x.entries = append(x.entries, e)
			}
		default:
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err := x.dec.ReadToken()
	return err
}

// key reads an object member name.
func (x *extractor) key() (string, error) {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// value reads the next value as an entry of depType; a value that is not a string is skipped
// and returns an entry without dep type.
func (x *extractor) value(depType string) (entry, error) {
	if x.dec.PeekKind() != '"' {
		return entry{}, x.dec.SkipValue()
	}
	tok, err := x.dec.ReadToken()
	if err != nil {
		return entry{}, err
	}
	s := tok.String()
	end := int(x.dec.InputOffset())
	start := end - len(s) - 2
	e := entry{depType: depType, value: s}
	off := end
	if start >= 0 && x.src[start] == '"' && string(x.src[start+1:end-1]) == s {
		e.span = dep.Span{Start: start + 1, End: end - 1}
		off = e.span.Start
	}
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos, e.line = off, x.line
	return e, nil
}

// depTypes maps a section path to the dep type of its entries per project type.
var depTypes = map[string]map[string]string{
	typeApplication: {
		"dependencies.direct": DepTypeDirect, "dependencies.indirect": DepTypeIndirect,
		"test-dependencies.direct": DepTypeTestDirect, "test-dependencies.indirect": DepTypeTestIndirect,
		DepTypeElmVersion: DepTypeElmVersion,
	},
	typePackage: {
		"dependencies": DepTypeDependencies, "test-dependencies": DepTypeTestDependencies,
		DepTypeElmVersion: DepTypeElmVersion,
	},
}

// finish turns the entries into deps once the project type is known; entries in sections
// the type does not have (a package's `direct`) are dropped.
func (x *extractor) finish() []dep.Dep {
	types := depTypes[x.typ]
	if types == nil {
		return nil
	}
	deps := make([]dep.Dep, 0, len(x.entries))
	for _, e := range x.entries {
		depType := types[e.depType]
		if depType == "" {
			continue
		}
		d := dep.Dep{
			Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: depType,
			File: x.file, Line: e.line, Name: e.name, Version: e.value,
		}
		if depType == DepTypeElmVersion {
			d.Datasource, d.Source = CompilerDatasource, compilerSource
		}
		switch {
		case d.Datasource == Datasource && !nameRe.MatchString(e.name):
			d.Skip = skipName
		case e.span == (dep.Span{}):
			d.Skip = skipVersion
		case x.typ == typeApplication:
			if _, err := semver.StrictNewVersion(e.value); err != nil {
				d.Skip = skipVersion
			}
			d.RefSpan = e.span
		default:
			c, ok := parseConstraint(e.value)
			if !ok {
				d.Skip = skipConstraint
				break
			}
			d.Range, d.Constraint, d.Version, d.RefSpan = e.value, c.masterminds(), c.lo.String(), e.span
		}
		deps = append(deps, d)
	}
	return deps
}
