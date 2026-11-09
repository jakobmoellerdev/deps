// Package jenkins extracts the plugins of Jenkins plugin lists in the input formats of the
// plugin installation manager tool (jenkins-plugin-cli): `plugins.txt` lines
// `<artifactId>:<version>` and the `plugins` list of `plugins.yaml`.
package jenkins

import (
	"bytes"
	"errors"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "jenkins"

// DepTypePlugin is the dep type of plugins.
const DepTypePlugin = "plugin"

// Datasource and Versioning of plugins (Renovate's).
const (
	Datasource = "jenkins-plugins"
	Versioning = "maven"
)

// Skip reasons.
const (
	skipNoVersion   = "no version"
	skipUnsupported = "unsupported version"
	skipURL         = "download url"
	skipInvalid     = "invalid version"
	skipIgnored     = "ignored"
	skipLocate      = "cannot locate value"
)

// lineRe is a plugins.txt line: the artifact ID, `:` and the version up to a comment or blank
// (Renovate's pattern), which may carry a download URL after another `:`.
var lineRe = regexp.MustCompile(`^\s*([\w-]+):([^#\s]+)[#\s]*(.*)$`)

// ignoreRe is a `# renovate:ignore` (or `pyup:`) comment, Renovate's skip comment.
var ignoreRe = regexp.MustCompile(`^(?:renovate|pyup):\s*ignore\s*(?:#|$)`)

type jenkinsManager struct{}

// New returns the jenkins manager.
func New() manager.Manager { return jenkinsManager{} }

func (jenkinsManager) Name() string { return Name }

// Match reports whether path is a `plugins.txt`, `plugins.yml` or `plugins.yaml` in any
// directory (Renovate's file pattern).
func (jenkinsManager) Match(p string) bool {
	switch path.Base(p) {
	case "plugins.txt", "plugins.yml", "plugins.yaml":
		return true
	}
	return false
}

// Extract returns the plugins of a plugins.txt or plugins.y(a)ml file.
func (jenkinsManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	if strings.HasSuffix(file, ".txt") {
		return extractText(file, src), nil
	}
	return extractYAML(file, src, env)
}

// Edits replaces the version. A plain YAML scalar is quoted when the new version would read
// as a number (`2.10`).
func (jenkinsManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if u.NewDigest != "" {
		return nil, errors.New("jenkins plugin versions cannot be pinned to a digest")
	}
	v := u.NewVersion
	if d.InsertAt > 0 && !plainString(v) {
		v = "'" + v + "'"
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: v}}, nil
}

// plainString reports whether v written as a plain YAML scalar reads as the string v.
func plainString(v string) bool {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(v), &n); err != nil || len(n.Content) != 1 {
		return false
	}
	s := n.Content[0]
	return s.Kind == yaml.ScalarNode && s.ShortTag() == "!!str" && s.Value == v
}

func newDep(file string, line int, name string) dep.Dep {
	return dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepTypePlugin, File: file, Line: line, Name: name}
}

// extractText reads the `<artifactId>:<version>` lines of a plugins.txt. Lines without a
// version (`git`, the latest) are not deps, as in Renovate.
func extractText(file string, src []byte) []dep.Dep {
	var deps []dep.Dep
	off := 0
	for i := 1; off < len(src); i++ {
		line := src[off:]
		if j := bytes.IndexByte(line, '\n'); j >= 0 {
			line = line[:j+1]
		}
		start := off
		off += len(line)
		m := lineRe.FindSubmatchIndex(bytes.TrimRight(line, "\r\n"))
		if m == nil {
			continue
		}
		d := newDep(file, i, string(line[m[2]:m[3]]))
		ver := string(line[m[4]:m[5]])
		url := ""
		// `<version>:<url>`, and `incrementals;<groupId>;<version>`.
		if v, u, ok := strings.Cut(ver, ":"); ok && v != "" && !strings.HasPrefix(u, "//") {
			ver, url = v, u
		} else if ok {
			ver, url = "", ver
		}
		setVersion(&d, ver, url != "", strings.Contains(ver, ";"), false, ignoreRe.MatchString(string(line[m[6]:m[7]])))
		if d.Skip == "" {
			d.RefSpan = dep.Span{Start: start + m[4], End: start + m[4] + len(ver)}
		}
		deps = append(deps, d)
	}
	return deps
}

// setVersion sets the skip reason of plugin d with version ver, in Renovate's precedence: a
// download URL, then `latest`, `experimental` or a group ID (incrementals), then no version
// or one that is no string, then an ignore comment.
func setVersion(d *dep.Dep, ver string, url, group, invalid, ignored bool) {
	d.Version = ver
	switch {
	case url:
		d.Skip = skipURL
	case ver == "latest" || ver == "experimental" || group:
		d.Skip = skipUnsupported
	case ver == "":
		d.Skip = skipNoVersion
	case invalid:
		d.Skip = skipInvalid
	case ignored:
		d.Skip = skipIgnored
	}
}

// extractYAML reads the `plugins` list of a plugins.y(a)ml: entries with an `artifactId`,
// their `source.version` (or `source.url`), `groupId` and `renovate.ignore`.
func extractYAML(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	if !bytes.Contains(src, []byte("artifactId")) {
		return nil, nil
	}
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	var deps []dep.Dep
	lines := yamlspan.LineStarts(src)
	for _, n := range yamlspan.Seq(yamlspan.Get(doc.Content[0], "plugins")) {
		id := yamlspan.Str(yamlspan.Get(n, "artifactId"))
		if id == nil || id.Value == "" {
			continue
		}
		d := newDep(file, id.Line, id.Value)
		source := yamlspan.Get(n, "source")
		v := yamlspan.Str(yamlspan.Get(source, "version"))
		ver := ""
		if v != nil && v.ShortTag() != "!!null" {
			ver, d.Line = v.Value, v.Line
		}
		// A YAML number is not the version written (`2.10` reads as 2.1; Renovate's invalid-version).
		invalid := ver != "" && v.ShortTag() != "!!str"
		ignored := strings.EqualFold(yamlspan.Value(yamlspan.Get(yamlspan.Get(n, "renovate"), "ignore")), "true")
		setVersion(&d, ver, yamlspan.Get(source, "url") != nil, yamlspan.Get(n, "groupId") != nil, invalid, ignored)
		if d.Skip == "" {
			locate(&d, src, lines, v)
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// locate sets the span of the version scalar v, or skips d: escapes and block scalars cannot be
// edited in place.
func locate(d *dep.Dep, src []byte, lines []int, v *yaml.Node) {
	off := yamlspan.Locate(src, lines, v)
	if off < 0 {
		d.Skip = skipLocate
		return
	}
	d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
	if v.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0 {
		d.InsertAt = off // plain: Edits quotes a new version that would read as a number
	}
}
