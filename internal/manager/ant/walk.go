package ant

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool/properties"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// value is a string as written in a file: its decoded text, the byte span of exactly that
// text (zero when the raw bytes differ: entities, properties escapes), and its line.
type value struct {
	text string
	span dep.Span
	line int
}

// def is a property definition: its value and the file defining it.
type def struct {
	value
	file string
}

// repo is a Maven repository: its id ("" = none) and URL.
type repo struct{ id, url string }

// decl is a `<dependency>` as written: group, artifact and version (substrings of `coords`, or
// attributes), the dep type, and the repositories of its enclosing element.
type decl struct {
	file                     string
	group, artifact, version value
	depType                  string
	repos                    []repo
}

// elem is an element of a build file: its local name (without namespace prefix), its
// attributes by lower-case name (`prefix:name` for prefixed ones) and its child elements.
type elem struct {
	name  string
	attrs map[string]value
	kids  []*elem
}

// rootMark starts an expanded path that is relative to the scanned root (`${basedir}`), not to
// the directory it is resolved against.
const rootMark = "\x00"

// maxExpandDepth bounds `${a}` → `${b}` chains when expanding paths and names.
const maxExpandDepth = 10

// walker collects the properties, repositories and dependencies of a build file and the
// files it imports, in document order, as Ant (and Renovate's ant manager) reads them.
type walker struct {
	env      manager.Env
	basedir  string          // the project's basedir, a slash path relative to the root
	props    map[string]def  // first definition wins, as in Ant
	visited  map[string]bool // build and properties files read
	repoDefs map[string]repo // `<remoteRepository id url>` definitions, for refid
	decls    []decl
}

// newWalker returns the walker of the build file doc: its basedir is the `basedir` attribute
// of the root `<project>` relative to the file's directory, else that directory.
func newWalker(file string, doc *elem, env manager.Env) *walker {
	w := &walker{env: env, basedir: path.Dir(file), props: map[string]def{}, visited: map[string]bool{file: true}, repoDefs: map[string]repo{}}
	if len(doc.kids) == 1 && doc.kids[0].name == "project" {
		if b, ok := doc.kids[0].attrs["basedir"]; ok {
			if p, ok := w.path(b.text, w.basedir); ok {
				w.basedir = p
			}
		}
	}
	return w
}

// walk visits the children of e (in file) in document order: `<property>` definitions,
// `<import>`ed build files and `<dependency>` elements. repos are the repositories of the
// enclosing element; an element with its own (reposOf) passes those to its descendants.
func (w *walker) walk(file string, e *elem, repos []repo) {
	for _, k := range e.kids {
		switch k.name {
		case "property":
			w.property(file, k)
		case "import":
			if f, ok := k.attrs["file"]; ok {
				w.importFile(file, f.text)
			}
		case "dependency":
			w.dependency(file, k, repos)
		default:
			if k.name == "remoteRepository" {
				if id, url := k.attrs["id"].text, k.attrs["url"]; id != "" && url.text != "" {
					if u, ok := w.expand(url.text); ok {
						w.repoDefs[id] = repo{id: id, url: u}
					}
				}
			}
			r := w.reposOf(k)
			if r == nil {
				r = repos
			}
			w.walk(file, k, r)
		}
	}
}

// property records `<property name value>` unless name is defined already, and loads the
// properties of `<property file>` (keys prefixed with `prefix`).
func (w *walker) property(file string, e *elem) {
	if name, val := e.attrs["name"].text, e.attrs["value"]; name != "" && val.text != "" {
		if _, ok := w.props[name]; !ok {
			w.props[name] = def{value: val, file: file}
		}
	}
	f, ok := e.attrs["file"]
	if !ok {
		return
	}
	p, ok := w.path(f.text, w.basedir)
	if !ok || w.visited[p] {
		return
	}
	w.visited[p] = true
	src, ok := w.read(p)
	if !ok {
		return
	}
	prefix := e.attrs["prefix"].text
	if prefix != "" && !strings.HasSuffix(prefix, ".") {
		prefix += "."
	}
	for k, pr := range properties.Parse(src) {
		key := prefix + properties.Unescape(k)
		if _, ok := w.props[key]; ok {
			continue
		}
		v := def{text: properties.Unescape(pr.Value), line: pr.Line, file: p}
		if v.text == pr.Value {
			v.span = dep.Span{Start: pr.Start, End: pr.End}
		}
		w.props[key] = v
	}
}

// importFile walks the build file f, resolved against the directory of the importing file.
// Files outside the root, unreadable or malformed ones are ignored.
func (w *walker) importFile(file, f string) {
	p, ok := w.path(f, path.Dir(file))
	if !ok || w.visited[p] {
		return
	}
	w.visited[p] = true
	src, ok := w.read(p)
	if !ok {
		return
	}
	doc, err := parse(src)
	if err != nil {
		return
	}
	w.walk(p, doc, nil)
}

// read returns the content of the slash path p; false when it cannot be read (or the
// environment reads no files).
func (w *walker) read(p string) ([]byte, bool) {
	if w.env.ReadFile == nil {
		return nil, false
	}
	src, err := w.env.ReadFile(p)
	return src, err == nil
}

// dependency records the `<dependency>` e: `coords="group:artifact:version[[:type[:classifier]]:scope]"`
// (the Maven Resolver Ant Tasks' grammar), else the groupId, artifactId, version and scope
// attributes. Elements without a version are not deps (a version managed elsewhere, a POM
// reference), nor coords the resolver rejects.
func (w *walker) dependency(file string, e *elem, repos []repo) {
	dc := decl{file: file, depType: depType(e.attrs["scope"].text), repos: repos}
	if c, ok := e.attrs["coords"]; ok && c.text != "" {
		parts := splitCoords(c.text)
		if len(parts) < 3 || len(parts) > 6 || slices.ContainsFunc(parts, func(p string) bool { return p == "" || strings.Contains(p, " ") }) {
			return
		}
		if len(parts) >= 4 {
			dc.depType = depType(parts[len(parts)-1])
		}
		part := func(i int) value {
			off := 0
			for _, p := range parts[:i] {
				off += len(p) + 1
			}
			v := value{text: parts[i], line: c.line}
			if c.span != (dep.Span{}) {
				v.span = dep.Span{Start: c.span.Start + off, End: c.span.Start + off + len(parts[i])}
			}
			return v
		}
		dc.group, dc.artifact, dc.version = part(0), part(1), part(2)
	} else {
		dc.group, dc.artifact, dc.version = e.attrs["groupid"], e.attrs["artifactid"], e.attrs["version"]
		if dc.group.text == "" || dc.artifact.text == "" {
			return
		}
	}
	if dc.version.text == "" {
		return
	}
	w.decls = append(w.decls, dc)
}

// splitCoords splits coords at the colons outside `${…}` references.
func splitCoords(s string) []string {
	var parts []string
	start, depth := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}' && depth > 0:
			depth--
		case s[i] == ':' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// depType returns the dep type of a Maven scope: the scope, else compile.
func depType(scope string) string {
	if scopes[scope] {
		return scope
	}
	return DepTypeCompile
}

// reposOf returns the repositories e declares for its descendants: those of its
// `settingsFile` (Maven settings mirrors and profile repositories) and of its
// `<remoteRepository url>` (or `refid`) children; nil when none.
func (w *walker) reposOf(e *elem) []repo {
	var out []repo
	if f, ok := e.attrs["settingsfile"]; ok {
		out = w.settings(f.text)
	}
	for _, k := range e.kids {
		if k.name != "remoteRepository" {
			continue
		}
		if ref := k.attrs["refid"].text; ref != "" {
			if r, ok := w.repoDefs[ref]; ok {
				out = append(out, r)
			}
			continue
		}
		if u, ok := w.expand(k.attrs["url"].text); ok && u != "" {
			out = append(out, repo{id: k.attrs["id"].text, url: u})
		}
	}
	return out
}

// settingsXML is what Renovate reads of a Maven settings.xml: the mirrors and the repositories
// of every profile.
type settingsXML struct {
	XMLName  xml.Name  `xml:"settings"`
	Mirrors  []repoXML `xml:"mirrors>mirror"`
	Profiles []struct {
		Repos []repoXML `xml:"repositories>repository"`
	} `xml:"profiles>profile"`
}

type repoXML struct {
	ID  string `xml:"id"`
	URL string `xml:"url"`
}

// settings returns the repositories of the Maven settings file f (resolved against the
// basedir): its mirrors, then the repositories of its profiles.
func (w *walker) settings(f string) []repo {
	p, ok := w.path(f, w.basedir)
	if !ok {
		return nil
	}
	src, ok := w.read(p)
	if !ok {
		return nil
	}
	var s settingsXML
	if xml.Unmarshal(src, &s) != nil {
		return nil
	}
	var out []repo
	add := func(rs []repoXML) {
		for _, r := range rs {
			if u := strings.TrimSpace(r.URL); u != "" {
				out = append(out, repo{id: strings.TrimSpace(r.ID), url: u})
			}
		}
	}
	add(s.Mirrors)
	for _, pr := range s.Profiles {
		add(pr.Repos)
	}
	return out
}

// expand replaces the `${name}` references of s with the property values (`${basedir}` with
// rootMark and the basedir), following chains; false when one is undefined or a chain is too
// long.
func (w *walker) expand(s string) (string, bool) {
	for range maxExpandDepth {
		if !strings.Contains(s, "${") {
			return s, true
		}
		ok := true
		s = propRe.ReplaceAllStringFunc(s, func(m string) string {
			name := m[2 : len(m)-1]
			if name == "basedir" {
				return rootMark + w.basedir
			}
			d, found := w.props[name]
			if !found {
				ok = false
				return m
			}
			return d.text
		})
		if !ok {
			return "", false
		}
	}
	return "", false
}

// path resolves the file reference f against the slash directory dir (both relative to the
// root): `${…}` expanded, backslashes as slashes. False for absolute paths, paths outside the
// root and undefined properties.
func (w *walker) path(f, dir string) (string, bool) {
	f, ok := w.expand(f)
	if !ok || f == "" {
		return "", false
	}
	f = strings.ReplaceAll(f, `\`, "/")
	switch {
	case strings.HasPrefix(f, rootMark):
		f = path.Clean(strings.TrimPrefix(f, rootMark))
	case strings.Contains(f, rootMark) || path.IsAbs(f):
		return "", false
	default:
		f = path.Join(dir, f)
	}
	if strings.Contains(f, rootMark) || f == ".." || strings.HasPrefix(f, "../") {
		return "", false
	}
	return f, true
}

// parse reads the element tree of a build file with encoding/xml, locating attribute values
// by byte offset. Entities Ant files declare in their DOCTYPE (`&common;`) are left as they
// are; the declared encoding is not converted (versions are ASCII).
func parse(src []byte) (*elem, error) {
	bom := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	dec := xml.NewDecoder(bytes.NewReader(src[bom:]))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	ln := newLines(src)
	doc := &elem{}
	stack := []*elem{doc}
	for {
		start := bom + int(dec.InputOffset())
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		end := bom + int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			e := &elem{name: t.Name.Local, attrs: attrSpans(src, start, end, t.Attr, ln)}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, e)
			stack = append(stack, e)
		case xml.EndElement:
			if len(stack) == 1 || t.Name.Local != stack[len(stack)-1].name {
				return nil, errors.New("unexpected end element </" + t.Name.Local + ">")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 1 {
		return nil, errors.New("unclosed element <" + stack[len(stack)-1].name + ">")
	}
	return doc, nil
}

// attrSpans returns the attributes of the start tag src[start:end] by lower-case name, each
// with the span of its raw value when that equals the decoded one.
func attrSpans(src []byte, start, end int, attrs []xml.Attr, ln lines) map[string]value {
	out := make(map[string]value, len(attrs))
	tag := src[start:end]
	i := 1
	for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '>' && tag[i] != '/' {
		i++ // the element name
	}
	for _, a := range attrs {
		// Attributes come in document order: the next quoted value after the next '='.
		j := i + bytes.IndexByte(tag[i:], '=')
		if j < i {
			break
		}
		for j < len(tag) && tag[j] != '"' && tag[j] != '\'' {
			j++
		}
		if j >= len(tag) {
			break
		}
		k := bytes.IndexByte(tag[j+1:], tag[j])
		if k < 0 {
			break
		}
		vs, ve := start+j+1, start+j+1+k
		v := value{text: a.Value, line: ln.at(vs)}
		if string(src[vs:ve]) == a.Value {
			v.span = dep.Span{Start: vs, End: ve}
		}
		name := a.Name.Local
		if a.Name.Space != "" {
			name = a.Name.Space + ":" + name
		}
		out[strings.ToLower(name)] = v
		i = j + k + 2
	}
	return out
}

func isXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// lines maps byte offsets to 1-based lines.
type lines []int // offsets of '\n'

func newLines(src []byte) lines {
	var l lines
	for i, c := range src {
		if c == '\n' {
			l = append(l, i)
		}
	}
	return l
}

func (l lines) at(off int) int {
	lo, hi := 0, len(l)
	for lo < hi {
		m := (lo + hi) / 2
		if l[m] < off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo + 1
}
