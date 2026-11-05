// Package yamlspan holds the yaml.Node helpers the YAML managers share: mapping and scalar
// lookups, and the byte offset of a scalar's value in its source, so managers can replace
// single tokens instead of re-serializing the file.
package yamlspan

import (
	"bytes"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

// LineStarts returns the byte offset of every line start of src.
func LineStarts(src []byte) []int {
	ls := make([]int, 1, bytes.Count(src, []byte{'\n'})+1)
	for off := 0; ; {
		i := bytes.IndexByte(src[off:], '\n')
		if i < 0 {
			return ls
		}
		off += i + 1
		ls = append(ls, off)
	}
}

// Locate returns the byte offset of scalar v's value in src, or -1 when the source bytes differ
// from the value (escapes, multi-line or block scalars). lines are src's LineStarts. yaml
// columns count runes, not bytes, and point at the anchor of an anchored value.
func Locate(src []byte, lines []int, v *yaml.Node) int {
	if v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || v.Line < 1 || v.Line > len(lines) {
		return -1
	}
	off := lines[v.Line-1]
	for range v.Column - 1 {
		if off >= len(src) || src[off] == '\n' {
			return -1
		}
		_, n := utf8.DecodeRune(src[off:])
		off += n
	}
	if a := "&" + v.Anchor; v.Anchor != "" && bytes.HasPrefix(src[off:], []byte(a)) {
		off += len(a)
		for off < len(src) && (src[off] == ' ' || src[off] == '\t') {
			off++
		}
	}
	if v.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
		off++
	}
	if off > len(src) || !bytes.HasPrefix(src[off:], []byte(v.Value)) {
		return -1
	}
	return off
}

// Get returns the value of key in mapping m, or nil.
func Get(m *yaml.Node, key string) *yaml.Node {
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

// Str returns n if it is a scalar, else nil.
func Str(n *yaml.Node) *yaml.Node {
	if n == nil || n.Kind != yaml.ScalarNode {
		return nil
	}
	return n
}

// Seq returns the items of n if it is a sequence, else nil.
func Seq(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// Value returns the value of scalar n, or "" when n is not a scalar.
func Value(n *yaml.Node) string {
	if n = Str(n); n == nil {
		return ""
	}
	return n.Value
}
