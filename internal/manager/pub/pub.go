// Package pub extracts the dependencies of Dart and Flutter packages from pubspec.yaml (hosted
// packages, git packages pinned to a tag, and the Dart and Flutter SDK constraints), rewrites
// their constraints per rangeStrategy, reads the versions pubspec.lock locks, and updates
// pubspec.lock after an update with `dart pub upgrade` (see Locker).
package pub

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/dart"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/dartversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/flutterversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "pub"

// Versioning is the versioning scheme of packages and SDK constraints.
const Versioning = "pub"

// GitDatasource looks up git packages pinned to a tag; their versioning is TagVersioning.
const (
	GitDatasource = "git-tags"
	TagVersioning = "tag"
)

// DepTypes.
const (
	DepTypeDependencies    = "dependencies"
	DepTypeDevDependencies = "dev_dependencies"
	DepTypeEnvironment     = "environment"
)

// SDK dep names: the `environment` constraints of the Dart and Flutter SDKs.
const (
	sdkDart    = "dart"
	sdkFlutter = "flutter"
)

// Skip reasons.
const (
	skipSDK         = "sdk package"
	skipPath        = "path dependency"
	skipGitNoRef    = "git dependency without ref"
	skipGitCommit   = "git commit"
	skipGitURL      = "unsupported git URL"
	skipUnversioned = "no version constraint"
	skipUnsupported = "unsupported constraint"
	skipEscaped     = "cannot locate value"
	skipFlutterPin  = "pinned by the Flutter SDK"
	skipNoLock      = "no locked version"
	skipNoFloor     = "no lower bound"
)

// flutterPinned are packages the Flutter SDK pins exactly (through flutter and flutter_test),
// so a Flutter package cannot move them on its own; Renovate skips them too.
var flutterPinned = map[string]bool{"meta": true}

type pubManager struct{}

// New returns the pub manager.
func New() manager.Manager { return pubManager{} }

func (pubManager) Name() string { return Name }

// Match reports whether the slash path is a pubspec.yaml or pubspec.yml.
func (pubManager) Match(p string) bool {
	b := path.Base(p)
	return b == "pubspec.yaml" || b == "pubspec.yml"
}

// Extract returns the deps of `dependencies` and `dev_dependencies` in file order, then the
// `environment` constraints `sdk` (dep dart) and `flutter`. Range is the constraint as
// written (RefSpan its bytes), Version its lower bound ("" without one: ReadLocks takes the
// locked version). A plain (unquoted) constraint has InsertAt set to its offset, so Edits can
// quote a rewrite YAML would not read as a plain scalar (`>=1.2.3 <3.0.0`).
func (pubManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("top level is not a mapping")
	}
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), flutter: usesFlutter(root)}
	x.section(root, DepTypeDependencies)
	x.section(root, DepTypeDevDependencies)
	envNode := yamlspan.Get(root, "environment")
	if n := yamlspan.Str(yamlspan.Get(envNode, "sdk")); n != nil {
		x.sdk(n, sdkDart, dartversion.Name)
	}
	if n := yamlspan.Str(yamlspan.Get(envNode, "flutter")); n != nil {
		x.sdk(n, sdkFlutter, flutterversion.Name)
	}
	return x.deps, nil
}

// usesFlutter reports whether a pubspec is a Flutter package: it depends on the Flutter SDK
// (`flutter: {sdk: flutter}`) or constrains its version.
func usesFlutter(root *yaml.Node) bool {
	if yamlspan.Get(yamlspan.Get(root, "environment"), "flutter") != nil {
		return true
	}
	for _, sec := range []string{DepTypeDependencies, DepTypeDevDependencies} {
		m := yamlspan.Get(root, sec)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}
		for i := 1; i < len(m.Content); i += 2 {
			if yamlspan.Value(yamlspan.Get(m.Content[i], "sdk")) == "flutter" {
				return true
			}
		}
	}
	return false
}

type extractor struct {
	file    string
	src     []byte
	lines   []int
	flutter bool
	deps    []dep.Dep
}

// section adds the deps of the mapping under key.
func (x *extractor) section(root *yaml.Node, depType string) {
	m := yamlspan.Get(root, depType)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		d := dep.Dep{Manager: Name, Datasource: dart.Name, Versioning: Versioning, DepType: depType, File: x.file, Line: k.Line, Name: k.Value}
		switch v.Kind {
		case yaml.ScalarNode:
			x.constraint(&d, v)
		case yaml.MappingNode:
			x.detailed(&d, v)
		default:
			d.Skip = skipUnsupported
		}
		if d.Skip == "" && x.flutter && flutterPinned[d.Name] {
			d.Skip = skipFlutterPin
		}
		x.deps = append(x.deps, d)
	}
}

// detailed fills d from a detailed dependency: `sdk`, `path`, `git` (url and ref) or
// `hosted` with `version`.
func (x *extractor) detailed(d *dep.Dep, v *yaml.Node) {
	switch {
	case yamlspan.Get(v, "sdk") != nil:
		d.Skip = skipSDK
	case yamlspan.Get(v, "path") != nil:
		d.Skip = skipPath
	case yamlspan.Get(v, "git") != nil:
		x.git(d, yamlspan.Get(v, "git"))
	default:
		switch h := yamlspan.Get(v, "hosted"); {
		case h == nil:
		case h.Kind == yaml.ScalarNode:
			d.Source = h.Value
		default:
			d.Source = yamlspan.Value(yamlspan.Get(h, "url"))
			if n := yamlspan.Value(yamlspan.Get(h, "name")); n != "" {
				d.Name = n
			}
		}
		n := yamlspan.Str(yamlspan.Get(v, "version"))
		if n == nil {
			d.Skip = skipUnversioned
			return
		}
		x.constraint(d, n)
	}
}

// git makes d a git-tags dep of the repository at `url` (or the string form) pinned to `ref`.
func (x *extractor) git(d *dep.Dep, g *yaml.Node) {
	u, ref := g, (*yaml.Node)(nil)
	if g.Kind == yaml.MappingNode {
		u, ref = yamlspan.Get(g, "url"), yamlspan.Str(yamlspan.Get(g, "ref"))
	}
	d.Datasource, d.Versioning = GitDatasource, TagVersioning
	if _, d.Source = manager.GitRepo(yamlspan.Value(u)); d.Source == "" {
		d.Skip = skipGitURL
		return
	}
	if ref == nil || ref.Value == "" {
		d.Skip = skipGitNoRef
		return
	}
	d.Version = ref.Value
	if manager.IsHex(ref.Value, 40) {
		d.Skip = skipGitCommit
		return
	}
	off := yamlspan.Locate(x.src, x.lines, ref)
	if off < 0 {
		d.Skip = skipEscaped
		return
	}
	d.Line, d.RefSpan = ref.Line, dep.Span{Start: off, End: off + len(ref.Value)}
}

// constraint fills d from the version constraint n.
func (x *extractor) constraint(d *dep.Dep, n *yaml.Node) {
	if n.Tag == "!!null" || n.Value == "" {
		d.Skip = skipUnversioned
		return
	}
	d.Version = n.Value
	c, err := versioning.ParsePubConstraint(n.Value)
	switch {
	case err != nil:
		d.Skip = skipUnsupported
		return
	case c.Any:
		d.Skip = skipUnversioned
		return
	}
	off := yamlspan.Locate(x.src, x.lines, n)
	if off < 0 {
		d.Skip = skipEscaped
		return
	}
	d.Line, d.Range, d.RefSpan = n.Line, n.Value, dep.Span{Start: off, End: off + len(n.Value)}
	if manager.QuoteChar(n.Style) == 0 {
		d.InsertAt = off
	}
	d.Version = ""
	if f, ok := c.Floor(); ok {
		d.Version = f.V
	}
}

// sdk adds the environment constraint n of an SDK.
func (x *extractor) sdk(n *yaml.Node, name, datasource string) {
	d := dep.Dep{Manager: Name, Datasource: datasource, Versioning: Versioning, DepType: DepTypeEnvironment, File: x.file, Line: n.Line, Name: name}
	x.constraint(&d, n)
	if d.Skip == "" && d.Version == "" {
		d.Skip = skipNoFloor // nothing locks an SDK: `<4.0.0` has no version to update from
	}
	x.deps = append(x.deps, d)
}

// Edits rewrites the constraint for the new version under u.RangeStrategy (newRange), or the
// ref of a git dependency. A plain constraint whose rewrite YAML would not read as a plain
// scalar is quoted.
func (pubManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	out, ok := newRange(d.Range, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("constraint %s cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	if d.InsertAt > 0 && needsQuote(out) {
		out = "'" + out + "'"
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}

// NewRange implements manager.RangeRewriter (newRange).
func (pubManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return newRange(d.Range, v, strategy)
}

// needsQuote reports whether s cannot be written as a plain YAML scalar in a block mapping:
// it starts with an indicator or holds a comment or mapping separator.
func needsQuote(s string) bool {
	return s == "" || strings.IndexByte(">|!&*'\"%@`#{}[],?:", s[0]) >= 0 || strings.Contains(s, ": ") || strings.Contains(s, " #")
}
