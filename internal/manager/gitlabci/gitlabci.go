// Package gitlabci extracts container images (`image`, `services`), project includes
// (`include: {project, ref}`) and CI/CD components (`include: {component}`) from GitLab CI
// configuration files. One manager covers what Renovate splits into gitlabci and
// gitlabci-include.
package gitlabci

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "gitlabci"

// Dep types.
const (
	TypeImage     = "image"
	TypeService   = "service"
	TypeInclude   = "include"
	TypeComponent = "component"
)

// serverVars are the GitLab CI predefined variables that name the instance host
// (CI_SERVER_FQDN with port, CI_SERVER_HOST without) when leading a component path.
var serverVars = []string{"$CI_SERVER_FQDN", "${CI_SERVER_FQDN}", "$CI_SERVER_HOST", "${CI_SERVER_HOST}"}

var (
	ciFileRe = regexp.MustCompile(`(^|/)([^/]*\.)?\.?gitlab-ci\.ya?ml$`)
	ciDirRe  = regexp.MustCompile(`(^|/)\.gitlab/ci/.+\.ya?ml$`)
)

// reserved are top-level keys that are not jobs.
var reserved = map[string]bool{
	"include": true, "variables": true, "stages": true, "workflow": true, "spec": true,
	"image": true, "services": true,
	"before_script": true, "after_script": true, "cache": true,
}

type gitlabManager struct{}

// New returns the gitlabci manager.
func New() manager.Manager { return gitlabManager{} }

func (gitlabManager) Name() string { return Name }

// Match reports whether path is `.gitlab-ci.y(a)ml`, `<x>.gitlab-ci.y(a)ml` or a YAML file
// under `.gitlab/ci/`.
func (gitlabManager) Match(path string) bool {
	return ciFileRe.MatchString(path) || ciDirRe.MatchString(path)
}

// Extract returns the deps of every YAML document in src (component templates carry a
// `spec:` header document). Project includes and components on the instance resolve against
// env.GitLabURL.
func (gitlabManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	x := extractor{file: file, src: src, lines: lineStarts(src), base: env.GitLabURL}
	if x.base == "" {
		x.base = platform.DefaultGitLabURL
	}
	if u, err := url.Parse(x.base); err == nil {
		x.host = u.Host
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			x.root(doc.Content[0])
		}
	}
	return x.deps, nil
}

// Edits rewrites image references like the docker manager, and include/component refs in
// place.
func (gitlabManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	switch d.DepType {
	case TypeImage, TypeService:
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("digest pinning of GitLab includes is not supported")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

type extractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
	base  string // GitLab instance base URL without trailing slash
	host  string // host[:port] of base
}

func lineStarts(src []byte) []int {
	ls := make([]int, 1, bytes.Count(src, []byte{'\n'})+1)
	for i, b := range src {
		if b == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
}

func (x *extractor) root(m *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i].Value, m.Content[i+1]
		switch {
		case k == "include":
			x.include(v)
		case k == "image":
			x.image(v)
		case k == "services":
			x.services(v)
		case !reserved[k]: // jobs, hidden templates and `default`
			x.job(v)
		}
	}
}

// job extracts image and services of a job (or of `default`, or a hidden `.template`).
func (x *extractor) job(m *yaml.Node) {
	x.image(get(m, "image"))
	x.services(get(m, "services"))
}

// image handles `image: ref` and `image: {name: ref}`.
func (x *extractor) image(v *yaml.Node) {
	if v != nil && v.Kind == yaml.MappingNode {
		v = get(v, "name")
	}
	x.ref(TypeImage, v)
}

func (x *extractor) services(v *yaml.Node) {
	if v == nil || v.Kind != yaml.SequenceNode {
		return
	}
	for _, s := range v.Content {
		if s.Kind == yaml.MappingNode {
			s = get(s, "name")
		}
		x.ref(TypeService, s)
	}
}

func (x *extractor) ref(depType string, v *yaml.Node) {
	if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: depType, File: x.file, Line: v.Line, Name: v.Value}
	switch off := x.locate(v); {
	case strings.Contains(v.Value, "$"):
		d.Skip = "variable reference"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		docker.ParseRef(&d, off, v.Value)
	}
	x.deps = append(x.deps, d)
}

// include handles a single include (mapping) or a list; string (local/remote) includes and
// `local`, `remote`, `template` mappings are not deps.
func (x *extractor) include(v *yaml.Node) {
	switch v.Kind {
	case yaml.SequenceNode:
		for _, c := range v.Content {
			if c.Kind == yaml.MappingNode {
				x.includeEntry(c)
			}
		}
	case yaml.MappingNode:
		x.includeEntry(v)
	}
}

func (x *extractor) includeEntry(m *yaml.Node) {
	if c := get(m, "component"); c != nil && c.Kind == yaml.ScalarNode {
		x.component(c)
		return
	}
	p := get(m, "project")
	if p == nil || p.Kind != yaml.ScalarNode || p.Value == "" {
		return
	}
	project := strings.Trim(p.Value, "/")
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: TypeInclude, File: x.file, Line: p.Line, Name: project}
	r := get(m, "ref")
	switch {
	case strings.Contains(project, "$"):
		d.Skip = "variable reference"
	case r == nil || r.Kind != yaml.ScalarNode || r.Value == "":
		d.Skip = "no ref"
	default:
		d.Source = x.base + "/" + project
		d.Line = r.Line
		x.version(&d, r, r.Value, 0)
	}
	x.deps = append(x.deps, d)
}

// component handles `component: <host>/<project path>/<name>@<version>`. A leading
// `$CI_SERVER_FQDN` or `$CI_SERVER_HOST` (also `${...}`) is the instance host; components on
// the instance resolve against its base URL (scheme and path prefix included), others
// against `https://<host>`.
func (x *extractor) component(v *yaml.Node) {
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: TypeComponent, File: x.file, Line: v.Line, Name: v.Value}
	at := strings.LastIndexByte(v.Value, '@')
	path := v.Value
	if at >= 0 {
		path = v.Value[:at]
	}
	d.Name = path
	if first, rest, ok := strings.Cut(path, "/"); ok && x.host != "" && slices.Contains(serverVars, first) {
		path = x.host + "/" + rest
	}
	slash := strings.LastIndexByte(path, '/')
	switch {
	case strings.Contains(path, "$"):
		d.Skip = "variable reference"
	case at < 0:
		d.Skip = "no version"
	case slash < 0 || strings.Count(path, "/") < 2:
		d.Skip = "invalid component"
	default:
		d.Source = x.componentSource(path[:slash])
		x.version(&d, v, v.Value[at+1:], at+1)
	}
	x.deps = append(x.deps, d)
}

// componentSource returns the repository URL of project, given as `<host>/<project path>`.
func (x *extractor) componentSource(project string) string {
	if host, rest, _ := strings.Cut(project, "/"); strings.EqualFold(host, x.host) {
		return x.base + "/" + rest
	}
	return "https://" + project
}

// version sets d's version to ver, found at byte rel of scalar v's value.
func (x *extractor) version(d *dep.Dep, v *yaml.Node, ver string, rel int) {
	off := x.locate(v)
	_, isVersion := versioning.ParseTag(ver)
	switch {
	case strings.Contains(ver, "$"):
		d.Skip = "variable reference"
	case !isVersion:
		d.Skip = "non-version ref"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		d.Version = ver
		d.RefSpan = dep.Span{Start: off + rel, End: off + rel + len(ver)}
	}
}

// locate returns the byte offset of v's value in src, or -1 when the source bytes differ from
// the value (escapes, block scalars). yaml columns count runes and point at anchors.
func (x *extractor) locate(v *yaml.Node) int {
	if v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || v.Line < 1 || v.Line > len(x.lines) {
		return -1
	}
	off := x.lines[v.Line-1]
	for range v.Column - 1 {
		if off >= len(x.src) || x.src[off] == '\n' {
			return -1
		}
		_, n := utf8.DecodeRune(x.src[off:])
		off += n
	}
	if a := "&" + v.Anchor; v.Anchor != "" && bytes.HasPrefix(x.src[off:], []byte(a)) {
		off += len(a)
		for off < len(x.src) && (x.src[off] == ' ' || x.src[off] == '\t') {
			off++
		}
	}
	if v.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
		off++
	}
	if off > len(x.src) || !bytes.HasPrefix(x.src[off:], []byte(v.Value)) {
		return -1
	}
	return off
}

// get returns the value of key in mapping m, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
