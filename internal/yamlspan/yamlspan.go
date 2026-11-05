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

// KeepDocuments returns src with every YAML document for which keep is false blanked, so a
// parser skips it cheaply: only its line breaks remain, plus its "---" start marker and any
// "..." end markers (the scanner treats those as markers in any context). The blanked
// document parses as an empty one, and every node of a kept document keeps its line and
// column, so Locate against the original src still works. keep sees a document's bytes
// including its marker line; it returns src itself when every document is kept.
//
// Documents split at lines starting with "---". Where the parser may read the text
// differently, nothing is blanked: a document with a line starting with "%" (a directive of
// the next document, or scalar content), everything from a document whose root node is a
// block scalar on ("|" or ">"; its indentation may be 0, so later "---" lines can be its
// content), and all of src when it is not split into lines by LF and CRLF alone (see
// unsplittable).
func KeepDocuments(src []byte, keep func(doc []byte) bool) []byte {
	if unsplittable(src) {
		return src
	}
	var out []byte
	flush := func(start, end int) {
		doc := src[start:end]
		if keep(doc) || bytes.HasPrefix(doc, []byte("%")) || bytes.Contains(doc, []byte("\n%")) {
			if out != nil {
				out = append(out, doc...)
			}
			return
		}
		if out == nil {
			out = make([]byte, 0, len(src))
			out = append(out, src[:start]...)
		}
		for first := true; len(doc) > 0; first = false {
			line, rest, found := bytes.Cut(doc, []byte{'\n'})
			switch {
			case first && isMarker(line, "---"):
				out = append(out, "---"...)
			case isMarker(line, "..."):
				out = append(out, "..."...)
			}
			if found {
				out = append(out, '\n')
			}
			doc = rest
		}
	}
	start, split, atRoot := 0, true, true
	for i := 0; i < len(src) && split; {
		end := len(src)
		if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
			end = i + j + 1
		}
		line := src[i:end]
		marker := isMarker(line, "---")
		if i > start && marker {
			flush(start, i)
			start, atRoot = i, true
		}
		switch {
		case isMarker(line, "..."):
			atRoot = true // an implicit document may follow
		case atRoot:
			if root, ok := rootToken(line, marker); ok {
				atRoot, split = false, root != '|' && root != '>'
			}
		}
		i = end
	}
	flush(start, len(src))
	if out == nil {
		return src
	}
	return out
}

// isMarker reports whether line starts with the document marker m ("---" or "...")
// followed by a blank or the line end.
func isMarker(line []byte, m string) bool {
	if !bytes.HasPrefix(line, []byte(m)) {
		return false
	}
	return len(line) == 3 || line[3] == ' ' || line[3] == '\t' || line[3] == '\n' || line[3] == '\r'
}

// rootToken returns the first byte of the node line starts, if any: after the "---" marker,
// blanks and node properties (tags and anchors). ok is false when the line holds no node
// (blank, comment, or properties only).
func rootToken(line []byte, marker bool) (b byte, ok bool) {
	if marker {
		line = line[3:]
	}
	for {
		line = bytes.TrimLeft(line, " \t")
		if len(line) == 0 || line[0] == '#' || line[0] == '\n' || line[0] == '\r' {
			return 0, false
		}
		if line[0] != '!' && line[0] != '&' {
			return line[0], true
		}
		j := bytes.IndexAny(line, " \t\r\n")
		if j < 0 {
			return 0, false
		}
		line = line[j:]
	}
}

// unsplittable reports whether src's YAML lines are not its LF and CRLF lines: it is UTF-16
// (a byte order mark), or it has a CR not followed by LF, a NEL, LS or PS, which the scanner
// also counts as line breaks.
func unsplittable(src []byte) bool {
	if bytes.HasPrefix(src, []byte{0xFE, 0xFF}) || bytes.HasPrefix(src, []byte{0xFF, 0xFE}) {
		return true
	}
	for i := bytes.IndexByte(src, '\r'); i >= 0; {
		if i+1 == len(src) || src[i+1] != '\n' {
			return true
		}
		j := bytes.IndexByte(src[i+1:], '\r')
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return bytes.Contains(src, []byte("\u0085")) || bytes.Contains(src, []byte("\u2028")) || bytes.Contains(src, []byte("\u2029"))
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
