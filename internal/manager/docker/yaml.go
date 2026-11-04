package docker

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const dockerPrefix = "docker://"

// yaml parses src and runs walk on the top-level mapping.
func (x *extractor) yaml(walk func(root *yaml.Node)) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(x.src, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		walk(doc.Content[0])
	}
	return nil
}

// manifestParses counts YAML parses of manifests mode (tests assert the byte prefilter).
var manifestParses atomic.Int64

// manifests extracts Kubernetes container images and Helm image values from every document
// of a multi-document YAML file; a document shaped like a Compose file (isCompose) is extracted
// as one. Unparsable files (Helm templates) yield no deps, no error.
func (x *extractor) manifests() {
	manifestParses.Add(1)
	dec := yaml.NewDecoder(bytes.NewReader(x.src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			slog.Debug("skipping unparsable yaml", "file", x.file, "err", err)
			x.deps = nil
			return
		}
		if len(doc.Content) == 1 && isCompose(doc.Content[0]) {
			x.compose(doc.Content[0])
		} else {
			x.walk(&doc)
		}
	}
}

var containerKeys = map[string]bool{"containers": true, "initContainers": true, "ephemeralContainers": true}

// walk visits every mapping below n.
func (x *extractor) walk(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i].Value, n.Content[i+1]
			switch {
			case containerKeys[k] && v.Kind == yaml.SequenceNode:
				for _, c := range v.Content {
					x.scalar(TypeKubernetes, get(c, "image"), "")
				}
			case v.Kind == yaml.MappingNode && strings.HasSuffix(strings.ToLower(k), "image"):
				x.helm(v)
			}
		}
	}
	for _, c := range n.Content {
		x.walk(c)
	}
}

// helm extracts `{registry?, repository, tag, digest?}` image values with RefSpan on the tag.
func (x *extractor) helm(m *yaml.Node) {
	repo, tag := str(get(m, "repository")), str(get(m, "tag"))
	if repo == nil || tag == nil || repo.Value == "" {
		return
	}
	name := repo.Value
	if r := str(get(m, "registry")); r != nil && r.Value != "" {
		name = r.Value + "/" + name
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeHelm, File: x.file, Line: tag.Line, Name: name}
	off := x.locate(tag)
	switch {
	case strings.Contains(name, "{{") || strings.Contains(tag.Value, "{{"):
		d.Skip = "template value"
	case strings.Contains(name, "$") || strings.Contains(tag.Value, "$"):
		d.Skip = "variable reference"
	case tag.Value == "":
		parseRef(&d, 0, name)
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		ref := name + ":" + tag.Value
		parseRef(&d, off+len(tag.Value)-len(ref), ref)
		if dg := str(get(m, "digest")); d.Skip == "" && d.Digest == "" && dg != nil && strings.HasPrefix(dg.Value, "sha256:") {
			if o := x.locate(dg); o >= 0 {
				d.Digest, d.DigestSpan = dg.Value, dep.Span{Start: o, End: o + len(dg.Value)}
			} else {
				d.Skip = "cannot locate value"
			}
		}
	}
	x.deps = append(x.deps, d)
}

// str returns n if it is a scalar, else nil.
func str(n *yaml.Node) *yaml.Node {
	if n == nil || n.Kind != yaml.ScalarNode {
		return nil
	}
	return n
}

// workflow extracts jobs.*.container[.image], jobs.*.services.*.image and
// `uses: docker://` steps of workflows, and runs.image / `uses: docker://` steps of actions.
func (x *extractor) workflow(root *yaml.Node) {
	for _, job := range mappingValues(get(root, "jobs")) {
		if c := get(job, "container"); c != nil && c.Kind == yaml.MappingNode {
			x.scalar(TypeWorkflow, get(c, "image"), "")
		} else {
			x.scalar(TypeWorkflow, c, "")
		}
		for _, svc := range mappingValues(get(job, "services")) {
			x.scalar(TypeWorkflow, get(svc, "image"), "")
		}
		x.steps(TypeWorkflow, get(job, "steps"))
	}
	runs := get(root, "runs")
	if u := get(runs, "using"); u != nil && u.Value == "docker" {
		x.scalar(TypeAction, get(runs, "image"), dockerPrefix)
	}
	x.steps(TypeAction, get(runs, "steps"))
}

func (x *extractor) steps(depType string, steps *yaml.Node) {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return
	}
	for _, s := range steps.Content {
		x.scalar(depType, get(s, "uses"), dockerPrefix)
	}
}

// scalar adds the image reference in v. With a non-empty prefix, only values starting with it
// are references, and the prefix is not part of the reference.
func (x *extractor) scalar(depType string, v *yaml.Node, prefix string) {
	if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" || !strings.HasPrefix(v.Value, prefix) || v.Value == prefix {
		return
	}
	off := x.locate(v)
	if off >= 0 {
		off += len(prefix)
	}
	x.add(depType, v.Line, off, v.Value[len(prefix):])
}

// locate returns the byte offset of v's value in src, or -1 when the source bytes differ from
// the value (escapes, multi-line or block scalars). yaml columns count runes, not bytes, and
// point at the anchor of an anchored value.
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
		off = skipBlank(x.src, off+len(a), len(x.src))
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

// mappingValues returns the values of mapping m (nil if m is not a mapping).
func mappingValues(m *yaml.Node) []*yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	vs := make([]*yaml.Node, 0, len(m.Content)/2)
	for i := 1; i < len(m.Content); i += 2 {
		vs = append(vs, m.Content[i])
	}
	return vs
}
