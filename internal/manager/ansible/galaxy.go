package ansible

import (
	"errors"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// GalaxyName is the ansible-galaxy manager name (Renovate's).
const GalaxyName = "ansible-galaxy"

// Dep types of the ansible-galaxy manager (Renovate's).
const (
	DepTypeRole       = "role"
	DepTypeCollection = "galaxy-collection"
)

// Datasources and versionings of the ansible-galaxy deps.
const (
	RoleDatasource       = "galaxy"
	CollectionDatasource = "galaxy-collection"
	GitDatasource        = "git-tags"
	// RoleVersioning is the versioning of roles and git sources: their versions are git tags.
	RoleVersioning = "tag"
	// CollectionVersioning is the versioning of Galaxy collections: Galaxy versions are semver
	// (`1.0.0-dev` is a prerelease ordered by semver rules). Their specifiers
	// (`>=1.0.0,<2.0.0`, a bare version meaning `==`) are parsed and rewritten as PEP 440
	// specifier sets, the syntax ansible-galaxy accepts.
	CollectionVersioning = "semver"
)

// Skip reasons.
const (
	skipNoVersion   = "no version"
	skipNonVersion  = "non-version ref"
	skipNoName      = "no name"
	skipUnsupported = "unsupported source"
	skipLocal       = "local source"
	skipLocate      = pyreq.SkipLocate
)

var galaxyFileRe = regexp.MustCompile(`(^|/)(galaxy|requirements)(\.ansible)?\.ya?ml$`)

// galaxyNameRe is a Galaxy `namespace.name` (roles may have dashes).
var galaxyNameRe = regexp.MustCompile(`^[\w-]+\.[\w-]+$`)

type galaxyManager struct{}

// NewGalaxy returns the ansible-galaxy manager.
func NewGalaxy() manager.Manager { return galaxyManager{} }

func (galaxyManager) Name() string { return GalaxyName }

// Match reports whether path is a requirements file (`requirements.yml`, `galaxy.yml`, with
// optional `.ansible`, `.yml` or `.yaml`) in any directory.
func (galaxyManager) Match(p string) bool {
	return (strings.Contains(p, "requirements") || strings.Contains(p, "galaxy")) && galaxyFileRe.MatchString(p)
}

// Extract returns the roles and collections of a requirements file, or the `dependencies` of
// a collection's galaxy.yml.
func (galaxyManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	x := galaxyExtractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	top := doc.Content[0]
	if strings.HasPrefix(path.Base(file), "galaxy.") {
		x.metadata(yamlspan.Get(top, "dependencies"))
		return x.deps, nil
	}
	switch top.Kind {
	case yaml.SequenceNode: // the old format: a list of roles
		x.roles(top)
	case yaml.MappingNode:
		x.roles(yamlspan.Get(top, "roles"))
		x.collections(yamlspan.Get(top, "collections"))
	}
	return x.deps, nil
}

// Edits rewrites a collection's specifier (pyreq.Edits), else the version.
func (galaxyManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if u.NewDigest != "" {
		return nil, errors.New("ansible-galaxy versions cannot be pinned to a digest")
	}
	if d.Range != "" {
		return pyreq.Edits(u)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// InRange implements manager.RangeUpdater for collection specifiers.
func (galaxyManager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater for collection specifiers.
func (galaxyManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return pyreq.NewRange(d, v, strategy)
}

type galaxyExtractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
}

func (x *galaxyExtractor) newDep(depType string, line int) dep.Dep {
	return dep.Dep{Manager: GalaxyName, DepType: depType, File: x.file, Line: line}
}

// metadata extracts the `dependencies` mapping of galaxy.yml: `namespace.name: <specifier>`,
// or an SCM URL key with a git version (`'git+https://github.com/o/r': 'v1.0.0'`).
func (x *galaxyExtractor) metadata(deps *yaml.Node) {
	if deps == nil || deps.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(deps.Content); i += 2 {
		k, v := deps.Content[i], deps.Content[i+1]
		d := x.newDep(DepTypeCollection, k.Line)
		d.Name, d.Datasource, d.Versioning = k.Value, CollectionDatasource, CollectionVersioning
		switch name := strings.TrimSpace(k.Value); {
		case v.Kind != yaml.ScalarNode:
			d.Skip = skipNoVersion
		case isSCM(name):
			x.git(&d, k, name, "", false, entry{fields: map[string]*yaml.Node{"version": v}})
		case !galaxyNameRe.MatchString(name):
			d.Skip = skipNoName
		default:
			x.setSpec(&d, v)
		}
		x.deps = append(x.deps, d)
	}
}

// setSpec sets the collection version specifier v (a bare version means `==`, `*` or none any
// version).
func (x *galaxyExtractor) setSpec(d *dep.Dep, v *yaml.Node) {
	d.Line = v.Line
	off := yamlspan.Locate(x.src, x.lines, v)
	if strings.TrimSpace(v.Value) == "" {
		d.Skip = skipNoVersion
		return
	}
	pyreq.SetVersion(d, v.Value, off, off >= 0, pyreq.Pipenv)
}

// entry is a role or collection entry: a mapping, or a scalar (`name` or `name,version`).
type entry struct {
	fields map[string]*yaml.Node // scalar values by key
	keys   []string              // every key
	scalar *yaml.Node
	line   int
}

func readEntry(n *yaml.Node) (entry, bool) {
	e := entry{line: n.Line}
	switch n.Kind {
	case yaml.ScalarNode:
		e.scalar = n
	case yaml.MappingNode:
		e.fields = map[string]*yaml.Node{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			e.keys = append(e.keys, n.Content[i].Value)
			if v := yamlspan.Str(n.Content[i+1]); v != nil {
				e.fields[n.Content[i].Value] = v
			}
		}
	default:
		return e, false
	}
	return e, true
}

// roleKeys are the keys of a role entry.
var roleKeys = map[string]bool{"src": true, "name": true, "version": true, "scm": true, "role": true}

// role reports whether the mapping entry is a role: it has `src`, `name` or `role`, and only
// role keys.
func (e entry) role() bool {
	if e.fields["src"] == nil && e.fields["name"] == nil && e.fields["role"] == nil {
		return false
	}
	for _, k := range e.keys {
		if !roleKeys[k] {
			return false
		}
	}
	return true
}

func (e entry) value(key string) string {
	if v := e.fields[key]; v != nil {
		return strings.TrimSpace(v.Value)
	}
	return ""
}

// roles extracts the role entries of a requirements file. A role is `src` (or `name`) and
// `version`: a Galaxy role `namespace.name`, or an SCM URL (`git+https://…`, `https://…`,
// `git@host:owner/repo`), optionally with `,version` (and `,name`).
func (x *galaxyExtractor) roles(seq *yaml.Node) {
	for _, n := range yamlspan.Seq(seq) {
		e, ok := readEntry(n)
		if !ok || e.scalar == nil && !e.role() {
			continue // an `include:` of another file, a task (a tasks file named requirements.yml)
		}
		d := x.newDep(DepTypeRole, e.line)
		d.Datasource, d.Versioning = RoleDatasource, RoleVersioning
		src := e.scalar
		if src == nil {
			src = e.fields["src"]
		}
		if src == nil {
			src = e.fields["name"]
		}
		if src == nil {
			src = e.fields["role"]
		}
		if scm := e.value("scm"); scm != "" && scm != "git" {
			d.Name, d.Skip = src.Value, skipUnsupported
			x.deps = append(x.deps, d)
			continue
		}
		x.source(&d, src, e, false)
		x.deps = append(x.deps, d)
	}
}

// collections extracts the collection entries of a requirements file.
func (x *galaxyExtractor) collections(seq *yaml.Node) {
	for _, n := range yamlspan.Seq(seq) {
		e, ok := readEntry(n)
		if !ok {
			continue
		}
		d := x.newDep(DepTypeCollection, e.line)
		d.Datasource, d.Versioning = CollectionDatasource, CollectionVersioning
		name := e.scalar
		if name == nil {
			name = e.fields["name"]
		}
		if name == nil {
			d.Skip = skipNoName
			x.deps = append(x.deps, d)
			continue
		}
		switch e.value("type") {
		case "", "galaxy", "git":
		case "file", "dir", "subdirs":
			d.Name, d.Skip = name.Value, skipLocal
			x.deps = append(x.deps, d)
			continue
		default: // url: a tarball
			d.Name, d.Skip = name.Value, skipUnsupported
			x.deps = append(x.deps, d)
			continue
		}
		x.source(&d, name, e, true)
		x.deps = append(x.deps, d)
	}
}

// source sets d from the source value src of entry e: a Galaxy name with e's `version`
// (a collection: a specifier on e's `source` server), or an SCM URL looked up with git-tags.
func (x *galaxyExtractor) source(d *dep.Dep, src *yaml.Node, e entry, collection bool) {
	raw := strings.TrimSpace(src.Value)
	d.Line = src.Line
	// `src,version[,name]`; the version only when the value is a plain scalar we can locate.
	val, ver, hasVer := strings.Cut(raw, ",")
	ver, _, _ = strings.Cut(ver, ",")
	val, ver = strings.TrimSpace(val), strings.TrimSpace(ver)
	switch {
	case val == "":
		d.Skip = skipNoName
		return
	case e.value("type") == "git" || isSCM(val):
		x.git(d, src, val, ver, hasVer, e)
		return
	case strings.HasSuffix(val, ".tar.gz") || strings.HasSuffix(val, ".tgz") || strings.Contains(val, "://"):
		d.Name, d.Skip = val, skipUnsupported
		return
	case strings.HasPrefix(val, "/") || strings.HasPrefix(val, "."):
		d.Name, d.Skip = val, skipLocal
		return
	}
	d.Name = val
	if !galaxyNameRe.MatchString(val) {
		if !collection { // a role `name` with an `src` elsewhere was handled; this one is bare
			d.Skip = skipUnsupported
		} else {
			d.Skip = skipNoName
		}
		return
	}
	if collection {
		d.Source = e.value("source")
		if strings.HasPrefix(d.Source, "git@") || strings.HasSuffix(d.Source, ".git") {
			d.Skip = skipUnsupported
			return
		}
		if v := e.fields["version"]; v != nil {
			x.setSpec(d, v)
		} else {
			d.Skip = skipNoVersion
		}
		return
	}
	switch v := e.fields["version"]; {
	case v != nil:
		x.tagVersion(d, v, v.Value, yamlspan.Locate(x.src, x.lines, v))
	case hasVer:
		x.inlineVersion(d, src, ver)
	default:
		d.Skip = skipNoVersion
	}
}

// git sets d from the SCM URL val: git-tags of its https repository.
func (x *galaxyExtractor) git(d *dep.Dep, src *yaml.Node, val, ver string, hasVer bool, e entry) {
	d.Datasource, d.Versioning = GitDatasource, RoleVersioning
	u := strings.TrimPrefix(val, "git+")
	d.Name, d.Source = manager.GitRepo(u)
	if d.Source == "" {
		d.Name, d.Skip = val, skipUnsupported
		return
	}
	switch v := e.fields["version"]; {
	case hasVer && ver != "":
		x.inlineVersion(d, src, ver)
	case v != nil:
		x.tagVersion(d, v, v.Value, yamlspan.Locate(x.src, x.lines, v))
	default:
		d.Skip = skipNoVersion
	}
}

// inlineVersion sets the version ver written after the first comma of the scalar src.
func (x *galaxyExtractor) inlineVersion(d *dep.Dep, src *yaml.Node, ver string) {
	off := yamlspan.Locate(x.src, x.lines, src)
	if off >= 0 {
		c := strings.IndexByte(src.Value, ',')
		off += c + 1 + strings.Index(src.Value[c+1:], ver)
	}
	x.tagVersion(d, src, ver, off)
}

// tagVersion sets the exact version ver at off (-1 = not located) of a role or git source.
func (*galaxyExtractor) tagVersion(d *dep.Dep, n *yaml.Node, ver string, off int) {
	d.Line = n.Line
	ver = strings.TrimSpace(ver)
	switch {
	case ver == "":
		d.Skip = skipNoVersion
	case off < 0:
		d.Version, d.Skip = ver, skipLocate
	default:
		if _, ok := versioning.ParseTag(ver); !ok {
			d.Version, d.Skip = ver, skipNonVersion
			return
		}
		d.Version, d.RefSpan = ver, dep.Span{Start: off, End: off + len(ver)}
	}
}

// isSCM reports whether s is an SCM URL ansible-galaxy clones: `git+…`, `git@host:…`, or an
// http(s)/ssh/git URL of a repository (not an archive).
func isSCM(s string) bool {
	switch {
	case strings.HasPrefix(s, "git+"), strings.HasPrefix(s, "git@"):
		return true
	case strings.HasPrefix(s, "ssh://"), strings.HasPrefix(s, "git://"):
		return true
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		return !strings.HasSuffix(s, ".tar.gz") && !strings.HasSuffix(s, ".tgz") && !strings.HasSuffix(s, ".zip")
	}
	return false
}
