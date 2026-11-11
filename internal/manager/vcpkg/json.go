package vcpkg

import (
	"bytes"
	"encoding/json/jsontext"
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// node is a JSON value with the location of strings: span is the content between the quotes,
// zero when the raw bytes differ from the value (escapes).
type node struct {
	kind  jsontext.Kind
	str   string
	span  dep.Span
	start int // offset of the value
	keys  []string
	vals  []*node // object member values, in keys order; array elements
}

// get returns the value of the last member key of object n, or nil.
func (n *node) get(key string) *node {
	if n == nil || n.kind != '{' {
		return nil
	}
	for i, k := range slices.Backward(n.keys) {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

// items returns the elements of array n, or nil when n is no array.
func (n *node) items() []*node {
	if n == nil || n.kind != '[' {
		return nil
	}
	return n.vals
}

// has reports whether object n has member key.
func (n *node) has(key string) bool { return n.get(key) != nil }

// text returns the string value of n, or "" when n is no string.
func (n *node) text() string {
	if n == nil || n.kind != '"' {
		return ""
	}
	return n.str
}

// bom is the UTF-8 byte order mark, which editors on Windows write before a manifest's first
// byte; vcpkg's own parser skips it.
var bom = []byte("\ufeff")

// parseJSON reads src into a node tree. A leading byte order mark is blanked rather than
// trimmed, so the decoder reads it as whitespace and every span stays a span of src.
func parseJSON(src []byte) (*node, error) {
	in := src
	if bytes.HasPrefix(in, bom) {
		in = slices.Clone(src)
		for i := range bom {
			in[i] = ' '
		}
	}
	dec := jsontext.NewDecoder(bytes.NewReader(in), jsontext.AllowDuplicateNames(true))
	return parseValue(dec, src)
}

func parseValue(dec *jsontext.Decoder, src []byte) (*node, error) {
	kind := dec.PeekKind()
	start := int(dec.InputOffset())
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	// InputOffset before the token may precede whitespace and separators; locate the value.
	for start < len(src) && (src[start] == ' ' || src[start] == '\t' || src[start] == '\n' || src[start] == '\r' || src[start] == ',' || src[start] == ':') {
		start++
	}
	n := &node{kind: kind, start: start}
	switch kind {
	case '"':
		n.str = tok.String()
		end := int(dec.InputOffset())
		if s := end - len(n.str) - 1; s >= 1 && src[s-1] == '"' && src[end-1] == '"' && string(src[s:end-1]) == n.str {
			n.span = dep.Span{Start: s, End: end - 1}
		}
	case '{':
		for dec.PeekKind() != '}' {
			k, err := dec.ReadToken()
			if err != nil {
				return nil, err
			}
			key := k.String()
			v, err := parseValue(dec, src)
			if err != nil {
				return nil, err
			}
			n.keys, n.vals = append(n.keys, key), append(n.vals, v)
		}
		if _, err := dec.ReadToken(); err != nil {
			return nil, err
		}
	case '[':
		for dec.PeekKind() != ']' {
			v, err := parseValue(dec, src)
			if err != nil {
				return nil, err
			}
			n.vals = append(n.vals, v)
		}
		if _, err := dec.ReadToken(); err != nil {
			return nil, err
		}
	}
	return n, nil
}
