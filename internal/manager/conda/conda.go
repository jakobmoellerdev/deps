// Package conda extracts the packages of conda environment files (`environment.yml`): the
// match specs of `dependencies` (`numpy=1.26`, `scipy>=1.11,<2`, `conda-forge::pytorch==2.3.0`),
// looked up on the file's `channels`, and the PEP 508 requirements of its nested `pip:` list,
// looked up on PyPI.
package conda

import (
	"cmp"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	condads "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/conda"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Dependabot's package ecosystem).
const Name = "conda"

// Versioning is the scheme of conda packages.
const Versioning = "conda"

// DepTypes and skip reasons.
const (
	DepTypeDependencies = "dependencies"
	DepTypePip          = "pip"
	SkipBuild           = "build string"
	SkipChannel         = "unsupported channel"
	SkipMatchSpec       = "unsupported match spec"
)

var matchRe = regexp.MustCompile(`(^|/)environment\.ya?ml$`)

var (
	scheme, _   = versioning.Get(Versioning)
	condaRanges = scheme.(versioning.RangeMatcher)
)

type condaManager struct{}

// New returns the conda manager.
func New() manager.Manager { return condaManager{} }

func (condaManager) Name() string { return Name }

// Match reports whether the slash path is an environment.yml or environment.yaml. The
// substring check skips the regexp for the paths that cannot match.
func (condaManager) Match(p string) bool {
	return strings.Contains(p, "environment.") && matchRe.MatchString(p)
}

// Extract returns one dep per `dependencies` entry and per requirement of a `pip:` list.
func (condaManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	items := yamlspan.Seq(yamlspan.Get(root, "dependencies"))
	if len(items) == 0 {
		return nil, nil
	}
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	x.channels, x.badChannel = channels(yamlspan.Seq(yamlspan.Get(root, "channels")))
	for _, it := range items {
		if v := yamlspan.Str(it); v != nil && v.ShortTag() == "!!str" {
			x.conda(v)
		}
	}
	for _, it := range items {
		if pip := yamlspan.Seq(yamlspan.Get(it, "pip")); pip != nil {
			x.pip(pip)
		}
	}
	return x.deps, nil
}

// channels returns the anaconda.org channels of the `channels` list in order, and the first
// entry the API does not serve when no entry is usable.
func channels(list []*yaml.Node) (out []string, bad string) {
	for _, n := range list {
		v := yamlspan.Value(n)
		if ch, ok := condads.Channel(v); ok {
			out = append(out, ch)
		} else if bad == "" && v != "nodefaults" {
			bad = v
		}
	}
	if len(out) > 0 {
		bad = ""
	}
	return out, bad
}

type extractor struct {
	file       string
	src        []byte
	lines      []int
	channels   []string
	badChannel string // set when the file lists channels but none is usable
	python     string // the PEP 440 form of the `python` spec, for the pip requirements
	deps       []dep.Dep
}

// conda adds the dep of a match spec.
func (x *extractor) conda(n *yaml.Node) {
	ms, ok := parseMatchSpec(n.Value)
	if !ok {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: condads.Name, Versioning: Versioning, DepType: DepTypeDependencies, File: x.file, Line: n.Line, Name: ms.name}
	d.Source = strings.Join(x.channels, " ")
	switch {
	case ms.channel != "":
		ch, ok := condads.Channel(ms.channel)
		if !ok {
			d.Skip = SkipChannel + " " + ms.channel
		}
		d.Source = ch
	case x.badChannel != "":
		d.Skip = SkipChannel + " " + x.badChannel
	}
	switch {
	case d.Skip != "":
	case ms.skip != "":
		d.Skip = ms.skip
	default:
		d.Version, d.Skip = anchor(ms.spec)
	}
	if d.Skip == "" {
		off := yamlspan.Locate(x.src, x.lines, n)
		if off < 0 {
			d.Version, d.Skip = "", pyreq.SkipLocate
		} else {
			d.Range = ms.spec
			d.RefSpan = dep.Span{Start: off + ms.specStart, End: off + ms.specStart + len(ms.spec)}
		}
		if d.Name == "python" && x.python == "" {
			x.python = pep440(ms.spec)
		}
	}
	x.deps = append(x.deps, d)
}

// pip adds the deps of a `pip:` list: PEP 508 requirements, looked up on the `--index-url`
// and `--extra-index-url` options of the list. Other options (`-r`, `-e`, `-c`) are no deps.
func (x *extractor) pip(items []*yaml.Node) {
	index, extras := "", []string(nil)
	for _, it := range items {
		f := strings.Fields(yamlspan.Value(it))
		if len(f) == 0 {
			continue
		}
		opt, val, hasVal := strings.Cut(f[0], "=")
		if !hasVal && len(f) > 1 {
			val = f[1]
		}
		switch opt {
		case "--index-url", "-i":
			index = val
		case "--extra-index-url":
			extras = append(extras, val)
		}
	}
	for _, it := range items {
		n := yamlspan.Str(it)
		if n == nil || n.Value == "" || n.Value[0] == '-' {
			continue
		}
		req := n.Value
		var d dep.Dep
		r, ok := pyreq.Parse(req)
		switch {
		case ok && r.URL != "":
			d = pyreq.NewDep(Name, DepTypePip, x.file, r.Name, n.Line)
			off := yamlspan.Locate(x.src, x.lines, n)
			pyreq.SetGitURL(&d, r.URL, off+r.URLStart, off >= 0)
		case strings.HasPrefix(req, "git+"):
			d = pyreq.NewDep(Name, DepTypePip, x.file, pyreq.GitName(req), n.Line)
			off := yamlspan.Locate(x.src, x.lines, n)
			pyreq.SetGitURL(&d, req, off, off >= 0)
		case strings.Contains(req, "://") || pyreq.HasAnyPrefix(req, ".", "/", "~/", "hg+", "svn+", "bzr+"):
			d = pyreq.NewDep(Name, DepTypePip, x.file, req, n.Line)
			d.Skip = pyreq.URLSkip(req)
		case !ok:
			d = pyreq.NewDep(Name, DepTypePip, x.file, req, n.Line)
			d.Skip = pyreq.SkipRange
		default:
			d = pyreq.NewDep(Name, DepTypePip, x.file, r.Name, n.Line)
			off := yamlspan.Locate(x.src, x.lines, n)
			at := off + r.SpecStart
			pyreq.SetVersion(&d, r.Spec, at, off >= 0, pyreq.PEP440)
		}
		if d.Datasource != pyreq.GitTags {
			d.Source, d.ExtraSources, d.PythonConstraint = index, extras, x.python
		}
		x.deps = append(x.deps, d)
	}
}

// matchSpec is a parsed conda match spec string `[<channel>::]<name>[ ]<version spec>`.
type matchSpec struct {
	channel, name string
	spec          string // the version spec as written, "" = none
	specStart     int    // offset of spec in the string
	skip          string
}

// parseMatchSpec parses a match spec as conda's MatchSpec reads its string form: `numpy`,
// `numpy=1.26` (fuzzy), `numpy==1.26.4`, `numpy>=1.26,<2`, `numpy 1.26.*`, `numpy >=1.26`,
// `conda-forge::numpy=1.26`. A build string (`numpy=1.26.4=py312h8753938_0`, `numpy 1.26.4
// py312_0`) and the bracket form (`numpy[version='>=1.26']`) are skipped; ok is false for
// strings without a package name.
func parseMatchSpec(s string) (ms matchSpec, ok bool) {
	i := 0
	if c := strings.Index(s, "::"); c >= 0 {
		ms.channel, i = s[:c], c+2
	}
	start := i
	for i < len(s) && nameChar(s[i]) {
		i++
	}
	if ms.name = s[start:i]; ms.name == "" {
		return ms, false
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	rest := strings.TrimRight(s[i:], " \t")
	switch {
	case rest == "" || rest == "*":
		ms.skip = pyreq.SkipUnconstrained
		return ms, true
	case strings.ContainsAny(rest, "[]()|"):
		ms.skip = SkipMatchSpec
		return ms, true
	case hasBuild(rest):
		ms.skip = SkipBuild
		return ms, true
	}
	ms.spec, ms.specStart = rest, i
	return ms, true
}

// hasBuild reports whether the version-and-build part of a match spec names a build: a second
// `=` after a version (`=1.26.4=py312_0`, `==1.26.4=py312_0`), or a blank-separated word
// after the version spec that does not continue it (`1.26.4 py312_0`; `>=1.2, <2` continues).
func hasBuild(rest string) bool {
	v := strings.TrimPrefix(strings.TrimPrefix(rest, "="), "=")
	if j := strings.IndexByte(v, '='); j > 0 && !strings.ContainsAny(v[:j], "<>!~,") && !strings.ContainsAny(v[j-1:j], "<>!~,") {
		return true
	}
	f := strings.Fields(rest)
	for k := 1; k < len(f); k++ {
		if !strings.ContainsAny(f[k-1][len(f[k-1])-1:], "=<>!~,") && !strings.ContainsAny(f[k][:1], "=<>!~,") {
			return true
		}
	}
	return false
}

// nameChar reports whether c can be part of a conda package name.
func nameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// anchor returns the version that stands for a conda version spec (dep.Dep.Version): the
// first clause that is not an upper bound or exclusion, else the first upper bound; or the
// skip reason of a spec that is not a conda version spec or has neither.
func anchor(spec string) (string, string) {
	cls, err := versioning.ParseCondaSpec(spec)
	if err != nil {
		return "", pyreq.SkipRange
	}
	upper := ""
	for _, c := range cls {
		switch c.Op {
		case "<", "<=":
			upper = cmp.Or(upper, c.Version)
		case "!=":
		default:
			return c.Version, ""
		}
	}
	if upper == "" {
		return "", pyreq.SkipUnconstrained
	}
	return upper, ""
}

// pep440 returns the PEP 440 form of a conda version spec (`=3.11` → `==3.11.*`, `3.11.4` →
// `==3.11.4`), or "" when a clause has no PEP 440 equivalent.
func pep440(spec string) string {
	cls, err := versioning.ParseCondaSpec(spec)
	if err != nil {
		return ""
	}
	pep, _ := versioning.Get(pyreq.Versioning)
	parts := make([]string, len(cls))
	for i, c := range cls {
		if !pep.Valid(c.Version) {
			return ""
		}
		switch {
		case c.Fuzzy():
			parts[i] = "==" + c.Version + ".*"
		case c.Op == "!=" && c.Glob:
			parts[i] = "!=" + c.Version + ".*"
		case c.Op == "":
			parts[i] = "==" + c.Version
		default:
			parts[i] = c.Op + c.Version
		}
	}
	return strings.Join(parts, ",")
}

// matchers caches the compiled spec of each range InRange checks (nil when invalid): the
// engine checks every candidate release of a dep against its range.
var matchers sync.Map // string -> func(string) bool

// InRange implements manager.RangeUpdater: conda's VersionSpec semantics for conda packages,
// PEP 440 for pip requirements.
func (condaManager) InRange(d dep.Dep, v string) bool {
	if d.Datasource == pyreq.Datasource {
		return pyreq.InRange(d, v)
	}
	return admits(d.Range, v)
}

func admits(spec, v string) bool {
	m, ok := matchers.Load(spec)
	if !ok {
		f, err := condaRanges.MatchRange(spec)
		if err != nil {
			f = nil
		}
		m, _ = matchers.LoadOrStore(spec, f)
	}
	f := m.(func(string) bool)
	return f != nil && f(v)
}

// NewRange implements manager.RangeUpdater (see newRange; pip requirements: pyreq.NewRange).
func (condaManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	if d.Datasource == pyreq.Datasource {
		return pyreq.NewRange(d, v, strategy)
	}
	return newRange(d.Range, v, strategy)
}

// Edits rewrites the version spec (newRange) or the pip requirement's specifier (pyreq.Edits).
func (m condaManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == pyreq.Datasource || d.Datasource == pyreq.GitTags {
		return pyreq.Edits(u)
	}
	out, ok := newRange(d.Range, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("range %q cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}

// newRange returns the conda version spec spec rewritten for v under strategy, or false when
// that leaves it unchanged or does not admit v:
//   - pin: an exact spec takes v as its version, anything else becomes `==v` (Renovate's conda
//     versioning);
//   - an exact (`==1.26.4`, `1.26.4`) or fuzzy (`=1.26`, `1.26.*`) spec takes v as its version,
//     a fuzzy one at its own precision (`=1.26` → `=2.3` for 2.3.4), keeping its operator;
//   - any other spec whose clauses are PEP 440 clauses (`>=1.26,<2`, `~=1.26`, `>=3.1,!=3.2.*`)
//     is rewritten with the pep440 rules of the Python managers (pyreq.NewRange), as Renovate's
//     conda versioning delegates to its pep440 versioning.
func newRange(spec, v, strategy string) (string, bool) {
	cls, err := versioning.ParseCondaSpec(spec)
	if err != nil || !scheme.Valid(v) {
		return "", false
	}
	var out string
	switch c := cls[0]; {
	case len(cls) == 1 && !c.Fuzzy() && (c.Op == "" || c.Op == "=="):
		out = spec[:c.VerStart] + v + spec[c.VerStart+len(c.Version):]
	case strategy == dep.RangePin:
		out = "==" + v
	case len(cls) == 1 && c.Fuzzy():
		out = spec[:c.VerStart] + versioning.FitConda(c.Version, v) + spec[c.VerStart+len(c.Version):]
	default:
		for _, c := range cls {
			if c.Op == "" || c.Op == "=" || c.Glob && !strings.HasSuffix(spec[c.Start:c.End], ".*") {
				return "", false // no PEP 440 equivalent
			}
		}
		var ok bool
		if out, ok = pyreq.NewRange(dep.Dep{Manager: Name, Range: spec}, v, strategy); !ok {
			return "", false
		}
	}
	if out == spec || !admits(out, v) {
		return "", false
	}
	return out, true
}
