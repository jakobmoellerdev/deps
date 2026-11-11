// Package gitlabci extracts container images (`image`, `services`), project includes
// (`include: {project, ref}`), versioned remote includes (`include: {remote}` or a URL string)
// and CI/CD components (`include: {component}`) from GitLab CI configuration files. One
// manager covers what Renovate splits into gitlabci and gitlabci-include.
package gitlabci

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name.
const Name = "gitlabci"

// Dep types.
const (
	TypeImage     = "image"
	TypeService   = "service"
	TypeInclude   = "include"
	TypeRemote    = "remote"
	TypeComponent = "component"
)

// Datasources of includes and components: git-tags for a version tag, git-refs for a branch.
const (
	dsTags = "git-tags"
	dsRefs = "git-refs"
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
// under `.gitlab/ci/`. Each regexp only runs on paths containing its literal part.
func (gitlabManager) Match(path string) bool {
	return strings.Contains(path, "gitlab-ci.") && ciFileRe.MatchString(path) ||
		strings.Contains(path, ".gitlab/ci/") && ciDirRe.MatchString(path)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (gitlabManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yaml", ".yml"}}
}

// Extract returns the deps of every YAML document in src (component templates carry a
// `spec:` header document). Project includes and components on the instance resolve against
// env.GitLabURL.
func (gitlabManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), base: env.GitLabURL, aliases: aliases(env.RegistryAliases)}
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

// Edits rewrites image references like the docker manager. Include, remote and component
// refs: tag → tag in place; pinned commit → new commit plus the comment version; tag or
// branch → commit with " # <version>" inserted after the value (pin: digest).
func (gitlabManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	switch d.DepType {
	case TypeImage, TypeService:
		return docker.New().Edits(u)
	}
	switch {
	case u.NewDigest == "":
		if d.Digest != "" {
			return nil, errors.New("cannot unpin a commit-pinned GitLab include")
		}
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	case d.Digest != "":
		edits := []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Digest, New: u.NewDigest}}
		if u.NewVersion != d.Version {
			edits = append(edits, patch.Edit{Start: d.VersionSpan.Start, End: d.VersionSpan.End, Old: d.Version, New: u.NewVersion})
		}
		return edits, nil
	case d.InsertAt == 0:
		return nil, errors.New("no place for a version comment after the GitLab include ref")
	}
	return []patch.Edit{
		{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewDigest},
		{Start: d.InsertAt, End: d.InsertAt, New: " # " + u.NewVersion},
	}, nil
}

// Rename implements manager.Renamer for image references, like the docker manager; includes
// and components cannot be renamed.
func (gitlabManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.DepType != TypeImage && d.DepType != TypeService {
		return d, errors.New("only images can be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	file    string
	src     []byte
	lines   []int
	deps    []dep.Dep
	base    string            // GitLab instance base URL without trailing slash
	host    string            // host[:port] of base
	aliases map[string]string // registry aliases of images (see aliases)
}

// dependencyProxy are the prefixes of GitLab's Dependency Proxy for Docker Hub images, which
// Renovate resolves to Docker Hub.
var dependencyProxy = map[string]string{
	"$CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX": "", "${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}": "",
	"$CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX": "", "${CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX}": "",
}

// aliases returns the registry aliases of images: the dependency proxy prefixes and the
// config's registryAliases, which override them.
func aliases(config map[string]string) map[string]string {
	if len(config) == 0 {
		return dependencyProxy
	}
	m := maps.Clone(dependencyProxy)
	maps.Copy(m, config)
	return m
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
	x.image(yamlspan.Get(m, "image"))
	x.services(yamlspan.Get(m, "services"))
}

// image handles `image: ref` and `image: {name: ref}`.
func (x *extractor) image(v *yaml.Node) {
	if v != nil && v.Kind == yaml.MappingNode {
		v = yamlspan.Get(v, "name")
	}
	x.ref(TypeImage, v)
}

func (x *extractor) services(v *yaml.Node) {
	for _, s := range yamlspan.Seq(v) {
		if s.Kind == yaml.MappingNode {
			s = yamlspan.Get(s, "name")
		}
		x.ref(TypeService, s)
	}
}

func (x *extractor) ref(depType string, v *yaml.Node) {
	if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: depType, File: x.file, Line: v.Line, Name: v.Value}
	docker.ParseImage(&d, yamlspan.Locate(x.src, x.lines, v), v.Value, x.aliases)
	x.deps = append(x.deps, d)
}

// include handles a single include (mapping or string) or a list. Local and template
// includes are not deps: they have no version.
func (x *extractor) include(v *yaml.Node) {
	switch v.Kind {
	case yaml.SequenceNode:
		for _, c := range v.Content {
			x.includeItem(c)
		}
	default:
		x.includeItem(v)
	}
}

func (x *extractor) includeItem(v *yaml.Node) {
	switch v.Kind {
	case yaml.MappingNode:
		x.includeEntry(v)
	case yaml.ScalarNode:
		// A string include is remote when it is a URL, else local.
		if strings.HasPrefix(v.Value, "https://") || strings.HasPrefix(v.Value, "http://") {
			x.remote(v)
		}
	}
}

func (x *extractor) includeEntry(m *yaml.Node) {
	if c := yamlspan.Get(m, "component"); c != nil && c.Kind == yaml.ScalarNode {
		x.component(c)
		return
	}
	if r := yamlspan.Get(m, "remote"); r != nil && r.Kind == yaml.ScalarNode {
		x.remote(r)
		return
	}
	p := yamlspan.Get(m, "project")
	if p == nil || p.Kind != yaml.ScalarNode || p.Value == "" {
		return
	}
	project := strings.Trim(p.Value, "/")
	d := dep.Dep{Manager: Name, Datasource: dsTags, Versioning: "tag", DepType: TypeInclude, File: x.file, Line: p.Line, Name: project}
	r := yamlspan.Get(m, "ref")
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

// remote handles a remote include URL that names a repository ref: a GitLab raw file
// (`https://<host>/<project>/-/raw/<ref>/<file>`) or a GitHub one
// (`https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<file>`). Other URLs are not deps.
func (x *extractor) remote(v *yaml.Node) {
	s := v.Value
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || strings.ContainsAny(rest, "?#") {
		return
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok || host == "" {
		return
	}
	pathAt := len(scheme) + 3 + len(host) + 1 // offset of path in s
	var repo string
	var refAt int
	if host == "raw.githubusercontent.com" {
		parts := strings.SplitN(path, "/", 4)
		if len(parts) < 4 || parts[0] == "" || parts[1] == "" {
			return
		}
		repo = "https://github.com/" + parts[0] + "/" + parts[1]
		refAt = pathAt + len(parts[0]) + 1 + len(parts[1]) + 1
	} else {
		i := strings.Index(path, "/-/raw/")
		if i <= 0 {
			return
		}
		repo = scheme + "://" + host + "/" + path[:i]
		refAt = pathAt + i + len("/-/raw/")
	}
	ref, _, ok := strings.Cut(s[refAt:], "/")
	if !ok || ref == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: dsTags, Versioning: "tag", DepType: TypeRemote, File: x.file, Line: v.Line, Name: repo[len(scheme)+3:]}
	if strings.Contains(repo, "$") {
		d.Skip = "variable reference"
	} else {
		d.Source = repo
		x.version(&d, v, ref, refAt)
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

// version sets d's ref to ver, found at byte rel of scalar v's value: a version tag
// (git-tags), a branch (git-refs: digest updates once pinned) or a 40-hex commit followed by
// a `# <version or branch>` comment. InsertAt, where pinning inserts the version comment, is
// after the value (and its closing quote) when only blanks or a comment follow it on the
// line, else 0 (a flow mapping: no place for a comment).
func (x *extractor) version(d *dep.Dep, v *yaml.Node, ver string, rel int) {
	off := yamlspan.Locate(x.src, x.lines, v)
	switch {
	case strings.Contains(ver, "$"):
		d.Skip = "variable reference"
		return
	case ver == "~latest":
		d.Skip = "latest version"
		return
	case off < 0:
		d.Skip = "cannot locate value"
		return
	}
	d.RefSpan = dep.Span{Start: off + rel, End: off + rel + len(ver)}
	end := off + len(v.Value)
	if q := manager.QuoteChar(v.Style); q != 0 && end < len(x.src) && x.src[end] == q {
		end++
	}
	le := len(x.src)
	if i := bytes.IndexByte(x.src[end:], '\n'); i >= 0 {
		le = end + i
	}
	if rest := bytes.TrimLeft(x.src[end:le], " \t\r"); len(rest) == 0 || rest[0] == '#' {
		d.InsertAt = end
	}
	if !manager.IsHex(ver, 40) {
		d.Version = ver
		if _, ok := versioning.ParseTag(ver); !ok {
			d.Datasource = dsRefs
		}
		return
	}
	sp, tok := commentToken(x.src, end, le)
	if tok == "" {
		d.Skip = "pinned without version comment"
		return
	}
	d.Digest, d.Version, d.VersionSpan = ver, tok, sp
	if _, ok := versioning.ParseTag(tok); !ok {
		d.Datasource = dsRefs
	}
}

// commentToken returns the first word of a `#` comment in src[from:le] and its span.
func commentToken(src []byte, from, le int) (dep.Span, string) {
	h := bytes.IndexByte(src[from:le], '#')
	if h < 0 {
		return dep.Span{}, ""
	}
	s := from + h + 1
	for s < le && (src[s] == ' ' || src[s] == '\t') {
		s++
	}
	e := s
	for e < le && src[e] != ' ' && src[e] != '\t' && src[e] != '\r' {
		e++
	}
	return dep.Span{Start: s, End: e}, string(src[s:e])
}
