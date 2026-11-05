package nuget

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// value is a string as written in a file: its decoded text, the byte span of exactly that
// text (zero when the raw bytes differ: entities, CDATA, comments inside), and its line.
type value struct {
	text string
	span dep.Span
	line int
}

// item is a package or SDK reference of an MSBuild file: the dep type, the package id, and
// its Version and VersionOverride (attribute or child element; nil = absent).
type item struct {
	depType           string
	name              value
	version, override *value
}

// versionValue returns the version that applies: VersionOverride over Version.
func (it item) versionValue() (value, bool) {
	switch {
	case it.override != nil:
		return *it.override, true
	case it.version != nil:
		return *it.version, true
	}
	return value{}, false
}

// project is what an MSBuild file defines: its references and its unconditional properties
// (lower-case name → the last definition, as MSBuild evaluates them).
type project struct {
	items []item
	props map[string]value
}

// packageItems are the item types whose Include (or Update) is a NuGet package id with a
// Version (or VersionOverride) attribute or child element.
var packageItems = map[string]bool{
	"PackageReference": true, "PackageVersion": true, "GlobalPackageReference": true,
	"PackageDownload": true, "DotNetCliToolReference": true,
}

// element is an open element during the scan.
type element struct {
	name   string
	cond   bool // it or an ancestor has a Condition, or it is inside a Target
	target bool // it is inside a Target (run-time items, not restored)
	it     int  // index into project.items of the package item it declares; -1 = none
	// text is the element's character data; textOK is false once anything but one run of
	// plain character data was seen (entities, CDATA, comments, child elements).
	text   value
	textOK bool
	kids   bool
}

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

// parseProject scans an MSBuild file (a project, .props or .targets) with encoding/xml,
// locating attribute values and element texts by byte offset. It collects the package items,
// the `<Project Sdk="Name/Version">` SDKs, `<Sdk Name Version>` and `<Import Sdk Version>`
// elements, and the properties of top-level `<PropertyGroup>`s without conditions. A file
// whose root is not `<Project>` yields nothing.
func parseProject(src []byte) (*project, error) {
	bom := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	dec := xml.NewDecoder(bytes.NewReader(src[bom:]))
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
			return r, nil
		}
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	p := &project{props: map[string]value{}}
	ln := newLines(src)
	var stack []*element
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
		var top *element
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if top == nil && t.Name.Local != "Project" {
				return p, nil
			}
			if top != nil {
				top.kids, top.textOK = true, false
			}
			e := &element{name: t.Name.Local, it: -1, textOK: true}
			attrs := attrSpans(src, start, end, t.Attr, ln)
			_, cond := attrs["condition"]
			e.cond = cond || e.name == "Target" || top != nil && top.cond
			e.target = e.name == "Target" || top != nil && top.target
			p.start(e, top, attrs, len(stack))
			stack = append(stack, e)
		case xml.EndElement:
			if top == nil || t.Name.Local != top.name {
				return nil, fmt.Errorf("unexpected end element </%s>", t.Name.Local)
			}
			stack = stack[:len(stack)-1]
			p.end(top, stack)
		case xml.CharData:
			if top == nil || len(bytes.TrimSpace(t)) == 0 {
				continue
			}
			s := strings.TrimSpace(string(t))
			if top.text.line != 0 || string(src[start:end]) != string(t) {
				top.textOK = false
			}
			if top.text.line == 0 {
				off := start + max(0, bytes.Index(src[start:end], []byte(s)))
				top.text = value{text: s, span: dep.Span{Start: off, End: off + len(s)}, line: ln.at(off)}
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			if top != nil && top.text.line != 0 {
				top.textOK = false
			}
		}
	}
	if len(stack) > 0 {
		return nil, errors.New("unclosed element <" + stack[len(stack)-1].name + ">")
	}
	return p, nil
}

// start records what the element e (child of parent, at depth) declares.
func (p *project) start(e, parent *element, attrs map[string]value, depth int) {
	switch {
	case depth == 0:
		if sdk, ok := attrs["sdk"]; ok {
			p.sdks(sdk)
		}
	case packageItems[e.name] && parent.name == "ItemGroup" && !parent.target:
		name, ok := attrs["include"]
		if !ok {
			name, ok = attrs["update"]
		}
		if !ok {
			return
		}
		it := item{depType: DepTypeNuGet, name: name}
		if v, ok := attrs["version"]; ok {
			it.version = &v
		}
		if v, ok := attrs["versionoverride"]; ok {
			it.override = &v
		}
		e.it = len(p.items)
		p.items = append(p.items, it)
	case e.name == "Sdk" && depth == 1 || e.name == "Import":
		key := "name"
		if e.name == "Import" {
			key = "sdk"
		}
		name, okN := attrs[key]
		if v, okV := attrs["version"]; okN && okV {
			p.items = append(p.items, item{depType: DepTypeMSBuildSDK, name: name, version: &v})
		}
	}
}

// end records the text of a closed leaf element: a property value, or the Version or
// VersionOverride child of a package item.
func (p *project) end(e *element, stack []*element) {
	if e.kids || len(stack) == 0 {
		return
	}
	text := e.text
	if !e.textOK {
		text.span = dep.Span{}
	}
	parent := stack[len(stack)-1]
	switch {
	case parent.name == "PropertyGroup" && len(stack) == 2 && !e.cond:
		p.props[strings.ToLower(e.name)] = text // an empty definition overrides too
	case parent.it >= 0 && e.name == "Version":
		p.items[parent.it].version = &text
	case parent.it >= 0 && e.name == "VersionOverride":
		p.items[parent.it].override = &text
	}
}

// sdks adds the `Name/Version` entries of a Project Sdk attribute (`;`-separated; entries
// without a version are SDKs the .NET SDK ships).
func (p *project) sdks(attr value) {
	off := 0
	for entry := range strings.SplitSeq(attr.text, ";") {
		start := off
		off += len(entry) + 1
		name, ver, ok := strings.Cut(entry, "/")
		if !ok {
			continue
		}
		n, v := strings.TrimSpace(name), strings.TrimSpace(ver)
		it := item{depType: DepTypeMSBuildSDK, name: value{text: n, line: attr.line}, version: &value{text: v, line: attr.line}}
		if attr.span != (dep.Span{}) {
			vs := attr.span.Start + start + len(name) + 1 + strings.Index(ver, v)
			it.version.span = dep.Span{Start: vs, End: vs + len(v)}
		}
		p.items = append(p.items, it)
	}
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
		out[strings.ToLower(a.Name.Local)] = v
		i = j + k + 2
	}
	return out
}

func isXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
