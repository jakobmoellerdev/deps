package docker

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
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

// compose extracts services.*.image.
func (x *extractor) compose(root *yaml.Node) {
	for _, svc := range mappingValues(get(root, "services")) {
		x.scalar(TypeCompose, get(svc, "image"), "")
	}
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
