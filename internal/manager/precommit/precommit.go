// Package precommit extracts the hook repositories of pre-commit configurations
// (`.pre-commit-config.yaml`): each `repos[]` entry with a `repo` URL and a `rev`, and the
// `additional_dependencies` of hooks that declare their `language` (node, python, golang).
package precommit

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"
	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name.
const Name = "pre-commit"

// Dep types (Renovate's): the hook repository and the additional_dependencies of a hook by its
// language.
const (
	DepTypeRepository = "repository"
	DepTypeNode       = "pre-commit-node"
	DepTypePython     = "pre-commit-python"
	DepTypeGolang     = "pre-commit-golang"
)

// Skip reasons of additional_dependencies.
const (
	SkipInvalid     = "invalid dependency specification"
	SkipNoVersion   = "no version"
	SkipModulePath  = "invalid module path"
	skipLocateValue = "cannot locate value"
)

// hookDepTypes maps a hook's `language` to the dep type of its additional_dependencies. Other
// languages (and hooks without one, whose language the hook repository's
// .pre-commit-hooks.yaml defines) are not read, as in Renovate.
var hookDepTypes = map[string]string{"node": DepTypeNode, "python": DepTypePython, "golang": DepTypeGolang}

var fileRe = regexp.MustCompile(`(^|/)\.pre-commit-config\.ya?ml$`)

// npmManager edits and rewrites the ranges of node additional_dependencies.
var npmManager = npm.New()

type precommitManager struct{}

// New returns the pre-commit manager.
func New() manager.Manager { return precommitManager{} }

func (precommitManager) Name() string { return Name }

// Match reports whether the slash path is a pre-commit configuration. The name check skips the
// regexp for the paths that cannot match.
func (precommitManager) Match(path string) bool {
	return strings.Contains(path, ".pre-commit-config.") && fileRe.MatchString(path)
}

// Extract returns one dep per `repos[]` entry, followed by its hooks' additional_dependencies,
// with byte spans into src.
func (precommitManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, nil
	}
	x := extractor{file: file, src: src}
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "repos" || top.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, e := range top.Content[i+1].Content {
			x.entry(e)
		}
	}
	return x.deps, nil
}

// InRange implements manager.RangeUpdater for additional_dependencies: PEP 440 specifiers for
// python hooks, npm ranges for node hooks.
func (precommitManager) InRange(d dep.Dep, v string) bool {
	if d.DepType == DepTypePython {
		return pyreq.InRange(d, v)
	}
	return npm.InRange(d, v)
}

// NewRange implements manager.RangeUpdater (pyreq.NewRange, the npm manager's NewRange).
func (precommitManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	switch d.DepType {
	case DepTypePython:
		return pyreq.NewRange(d, v, strategy)
	case DepTypeNode:
		return npmManager.(manager.RangeRewriter).NewRange(d, v, strategy)
	}
	return "", false
}

// Edits rewrites the rev: tag → tag, frozen SHA → new SHA plus comment version, tag → SHA with
// " # frozen: <version>" inserted after the value, or a bare SHA → new SHA (with " # frozen:
// <version>" when the engine resolved the commit to a tag). additional_dependencies get their
// range (node, python) or version (golang) rewritten.
func (precommitManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	switch d.DepType {
	case DepTypeNode:
		return npmManager.Edits(u)
	case DepTypePython:
		return pyreq.Edits(u)
	case DepTypeGolang:
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	switch {
	case u.NewDigest == "":
		if d.Digest != "" {
			return nil, errors.New("cannot unpin a frozen pre-commit rev")
		}
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	case d.Digest != "":
		edits := []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Digest, New: u.NewDigest}}
		switch {
		case d.VersionSpan == (dep.Span{}) && u.NewVersion != "":
			edits = append(edits, patch.Edit{Start: d.InsertAt, End: d.InsertAt, New: " # frozen: " + u.NewVersion})
		case u.NewVersion != d.Version:
			edits = append(edits, patch.Edit{Start: d.VersionSpan.Start, End: d.VersionSpan.End, Old: d.Version, New: u.NewVersion})
		}
		return edits, nil
	}
	return []patch.Edit{
		{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewDigest},
		{Start: d.InsertAt, End: d.InsertAt, New: " # frozen: " + u.NewVersion},
	}, nil
}

type extractor struct {
	file  string
	src   []byte
	lines []int // yamlspan.LineStarts(src), computed for the first additional dependency
	deps  []dep.Dep
}

// entry extracts one `repos[]` entry: the repository and, unless it is `meta` (whose hooks
// pre-commit defines), its hooks' additional_dependencies.
func (x *extractor) entry(e *yaml.Node) {
	if e.Kind != yaml.MappingNode {
		return
	}
	repo := yamlspan.Str(yamlspan.Get(e, "repo"))
	if repo != nil {
		x.deps = append(x.deps, repository(x.file, x.src, repo, yamlspan.Get(e, "rev")))
	}
	if repo != nil && repo.Value == "meta" {
		return
	}
	for _, h := range yamlspan.Seq(yamlspan.Get(e, "hooks")) {
		depType := hookDepTypes[yamlspan.Value(yamlspan.Get(h, "language"))]
		if depType == "" {
			continue
		}
		for _, v := range yamlspan.Seq(yamlspan.Get(h, "additional_dependencies")) {
			if v.Kind == yaml.ScalarNode {
				x.deps = append(x.deps, x.additional(depType, v))
			}
		}
	}
}

// additional returns the dep of one additional_dependencies item: `name@range` (node, npm
// ranges), a PEP 508 requirement (python) or `path@version` (golang, a package or module path
// as `go install` takes it).
func (x *extractor) additional(depType string, v *yaml.Node) dep.Dep {
	if x.lines == nil {
		x.lines = yamlspan.LineStarts(x.src)
	}
	off := yamlspan.Locate(x.src, x.lines, v)
	s := v.Value
	if depType == DepTypePython {
		r, ok := pyreq.Parse(s)
		if !ok {
			d := pyreq.NewDep(Name, depType, x.file, strings.TrimSpace(s), v.Line)
			d.Skip = SkipInvalid
			return d
		}
		return r.Dep(Name, depType, x.file, v.Line, off, off >= 0)
	}
	d := dep.Dep{Manager: Name, DepType: depType, File: x.file, Line: v.Line, Name: s}
	var at int // the offset of the version in s; 0 = none
	if depType == DepTypeNode {
		d.Datasource, d.Versioning = "npm", "semver"
		// The name may be scoped (`@scope/name@^1`): the version follows the next `@`.
		if i := strings.IndexByte(s[min(1, len(s)):], '@'); i >= 0 {
			at = i + 2
		}
	} else {
		d.Datasource, d.Versioning = "go", "go"
		if i := strings.IndexByte(s, '@'); i >= 0 {
			at = i + 1
		}
	}
	if at == 0 || at == len(s) {
		d.Name = strings.TrimSuffix(s, "@")
		d.Skip = SkipNoVersion
		return d
	}
	d.Name = s[:at-1]
	var span dep.Span
	if off >= 0 {
		span = dep.Span{Start: off + at, End: off + len(s)}
	}
	if depType == DepTypeNode {
		npm.SetRange(&d, s[at:], span)
		return d
	}
	switch {
	case module.CheckImportPath(d.Name) != nil:
		d.Skip = SkipModulePath
	case span == (dep.Span{}):
		d.Skip = skipLocateValue
	default:
		d.Version, d.RefSpan = s[at:], span
	}
	return d
}

// repository returns the dep of a `repos[]` entry's repo and rev.
func repository(file string, src []byte, repo, rev *yaml.Node) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: DepTypeRepository, File: file, Line: repo.Line}
	switch repo.Value {
	case "local":
		d.Name, d.Skip = repo.Value, "local repository"
		return d
	case "meta":
		d.Name, d.Skip = repo.Value, "meta repository"
		return d
	}
	d.Name, d.Source = gitRepo(repo.Value)
	if d.Source == "" {
		d.Name, d.Skip = repo.Value, "unrecognized repo"
		return d
	}
	if rev == nil || rev.Kind != yaml.ScalarNode || rev.Value == "" {
		d.Skip = "no rev"
		return d
	}
	d.Line = rev.Line
	if rev.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		d.Skip = skipLocateValue
		return d
	}
	ls, le := line(src, rev.Line)
	start := -1
	if ls >= 0 {
		l := src[ls:le]
		if k := bytes.Index(l, []byte("rev")); k >= 0 {
			if i := bytes.Index(l[k+3:], []byte(rev.Value)); i >= 0 {
				start = ls + k + 3 + i
			}
		}
	}
	if start < 0 {
		d.Skip = skipLocateValue
		return d
	}
	end := start + len(rev.Value)
	d.RefSpan = dep.Span{Start: start, End: end}
	d.InsertAt = end
	if q := manager.QuoteChar(rev.Style); q != 0 && end < le && src[end] == q {
		d.InsertAt++
	}
	if !manager.IsHex(rev.Value, 40) {
		// A suffixed version (mirrors-prettier's `v4.0.0-alpha.8`) is no tag version either:
		// semver sorts that repository's `v4.0.0-alpha.3-1` above it.
		if _, ok := versioning.ParseTag(rev.Value); ok {
			d.Version = rev.Value
		} else {
			d.Skip = "non-version ref"
		}
		return d
	}
	d.Digest = rev.Value
	sp, tok, frozen, ok := comment(src, d.InsertAt, le)
	_, version := versioning.ParseTag(tok)
	switch {
	case !ok:
		// A bare SHA keeps Version "": the engine resolves the commit to the tag pointing at
		// it, or tracks the default branch (config.UnversionedPin).
	case version:
		d.Version, d.VersionSpan = tok, sp
	case frozen && tok != "":
		// `# frozen: nightly`: the commit of a tag that is no version, whose digest is kept
		// current (the engine's tag-only deps).
		d.Version, d.VersionSpan = tok, sp
	default:
		// Another comment (`# fix for #651`) may say why this commit is pinned.
		d.Skip = "pinned without version comment"
	}
	return d
}

// gitRepo is manager.GitRepo, which also accepts a repository at the top level of its host
// (`https://pagure.io/<project>`) as Renovate does; name is then its one path element.
func gitRepo(s string) (name, source string) {
	if name, source = manager.GitRepo(s); source != "" {
		return name, source
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return "", ""
	}
	p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if p == "" || strings.Contains(p, "/") {
		return "", ""
	}
	return p, u.Scheme + "://" + u.Host + "/" + p
}

// line returns [start,end) of 1-based line n, excluding "\r\n".
func line(src []byte, n int) (int, int) {
	s := 0
	for range n - 1 {
		i := bytes.IndexByte(src[s:], '\n')
		if i < 0 {
			return -1, -1
		}
		s += i + 1
	}
	e := len(src)
	if i := bytes.IndexByte(src[s:], '\n'); i >= 0 {
		e = s + i
	}
	if e > s && src[e-1] == '\r' {
		e--
	}
	return s, e
}

// comment finds the first comment in src[from:le] (ok false: none) and its first token, after
// `frozen:` (pre-commit autoupdate --freeze) when it starts with one: `# frozen: v1.2.3` or
// `# v1.2.3`.
func comment(src []byte, from, le int) (sp dep.Span, tok string, frozen, ok bool) {
	h := bytes.IndexByte(src[from:le], '#')
	if h < 0 {
		return dep.Span{}, "", false, false
	}
	s := skipBlank(src, from+h+1, le)
	if bytes.HasPrefix(src[s:le], []byte("frozen:")) {
		s, frozen = skipBlank(src, s+len("frozen:"), le), true
	}
	e := s
	for e < le && src[e] != ' ' && src[e] != '\t' {
		e++
	}
	return dep.Span{Start: s, End: e}, string(src[s:e]), frozen, true
}

func skipBlank(src []byte, i, le int) int {
	for i < le && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return i
}
