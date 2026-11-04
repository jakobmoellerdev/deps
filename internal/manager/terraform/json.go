package terraform

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// labelCounts are the label counts of the top-level block types of the JSON syntax; other
// types have none.
var labelCounts = map[string]int{
	"resource": 2, "data": 2,
	"module": 1, "provider": 1, "variable": 1, "output": 1, "check": 1,
}

// jsonNode is a decoded JSON value with the offsets deps needs.
type jsonNode struct {
	kind    jsontext.Kind // '{', '[', '"' or another value kind
	start   int           // offset of the value
	end     int           // offset after the value
	str     string        // '"': the decoded string
	raw     string        // '"': the raw bytes between the quotes
	members []jsonMember  // '{'
	elems   []jsonNode    // '['
}

type jsonMember struct {
	name string
	off  int // offset of the name
	val  jsonNode
}

// parseJSON parses src as a Terraform JSON configuration into the body the HCL parser would
// produce: top-level members are blocks nested one object level per label, and inside a
// body an object member is both an object attribute and a nested block, an array of objects
// repeated nested blocks. "//" members are comments.
func parseJSON(src []byte) (body, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))
	root, err := readJSON(dec)
	if err == nil {
		_, err = dec.ReadToken()
		if err == nil {
			err = errors.New("unexpected data after the top-level value")
		} else if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	if err != nil {
		return body{}, fmt.Errorf("line %d: %w", lineAt(src, min(int(dec.InputOffset()), len(src))), err)
	}
	// The root is an object or an array of objects whose members are merged.
	roots := []jsonNode{root}
	if root.kind == '[' {
		roots = root.elems
	}
	var b body
	for _, r := range roots {
		if r.kind != '{' {
			return body{}, fmt.Errorf("line %d: top level is not an object", lineAt(src, r.start))
		}
		for _, m := range r.members {
			if m.name != "//" {
				b.blocks = jsonBlocks(b.blocks, m.name, nil, labelCounts[m.name], m.off, m.val)
			}
		}
	}
	return b, nil
}

func readJSON(dec *jsontext.Decoder) (jsonNode, error) {
	switch dec.PeekKind() {
	case '{', '[':
		tok, err := dec.ReadToken()
		if err != nil {
			return jsonNode{}, err
		}
		n := jsonNode{kind: tok.Kind(), start: int(dec.InputOffset()) - 1}
		end := byte('}')
		if n.kind == '[' {
			end = ']'
		}
		for dec.PeekKind() != jsontext.Kind(end) {
			var name string
			off := 0
			if n.kind == '{' {
				v, err := dec.ReadValue()
				if err != nil {
					return jsonNode{}, err
				}
				off = int(dec.InputOffset()) - len(v)
				if name, err = decodeString(v); err != nil {
					return jsonNode{}, err
				}
			}
			v, err := readJSON(dec)
			if err != nil {
				return jsonNode{}, err
			}
			if n.kind == '{' {
				n.members = append(n.members, jsonMember{name: name, off: off, val: v})
			} else {
				n.elems = append(n.elems, v)
			}
		}
		if _, err := dec.ReadToken(); err != nil {
			return jsonNode{}, err
		}
		n.end = int(dec.InputOffset())
		return n, nil
	default:
		v, err := dec.ReadValue()
		if err != nil {
			return jsonNode{}, err
		}
		n := jsonNode{kind: v.Kind(), end: int(dec.InputOffset())}
		n.start = n.end - len(v)
		if n.kind == '"' {
			if n.str, err = decodeString(v); err != nil {
				return jsonNode{}, err
			}
			n.raw = string(v[1 : len(v)-1]) // v is only valid until the next read
		}
		return n, nil
	}
}

func decodeString(v jsontext.Value) (string, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	tok, err := dec.ReadToken()
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// jsonBlocks appends the blocks of type typ that v (after the key at off) defines, given the
// labels read so far and the number of labels the type takes.
func jsonBlocks(blocks []block, typ string, labels []string, want, off int, v jsonNode) []block {
	switch {
	case v.kind == '[':
		for _, e := range v.elems {
			blocks = jsonBlocks(blocks, typ, labels, want, off, e)
		}
	case v.kind != '{':
	case len(labels) < want:
		for _, m := range v.members {
			if m.name != "//" {
				blocks = jsonBlocks(blocks, typ, append(slices.Clip(labels), m.name), want, off, m.val)
			}
		}
	default:
		blocks = append(blocks, block{typ: typ, labels: labels, span: dep.Span{Start: off, End: v.end}, body: jsonBody(v)})
	}
	return blocks
}

// jsonBody converts the members of the object v into a body.
func jsonBody(v jsonNode) body {
	var b body
	for _, m := range v.members {
		if m.name == "//" {
			continue
		}
		b.attrs = append(b.attrs, attr{name: m.name, off: m.off, val: jsonValue(m.val)})
		if m.val.kind == '{' || m.val.kind == '[' && len(m.val.elems) > 0 && allObjects(m.val.elems) {
			b.blocks = jsonBlocks(b.blocks, m.name, nil, 0, m.off, m.val)
		}
	}
	return b
}

func allObjects(ns []jsonNode) bool {
	for _, n := range ns {
		if n.kind != '{' {
			return false
		}
	}
	return true
}

// jsonValue converts a JSON value into an attribute value; strings are templates.
func jsonValue(n jsonNode) value {
	switch n.kind {
	case '{':
		v := value{kind: kindObject}
		for _, m := range n.members {
			v.obj = append(v.obj, attr{name: m.name, off: m.off, val: jsonValue(m.val)})
		}
		return v
	case '"':
		s, literal, escaped := templateLiteral(n.str)
		if !literal {
			return value{}
		}
		v := value{kind: kindString, str: s}
		if !escaped && n.raw == n.str {
			v.span = dep.Span{Start: n.start + 1, End: n.end - 1}
		}
		return v
	}
	return value{}
}

// templateLiteral decodes the template escapes $${ and %%{ of s; literal is false when s
// contains an interpolation or directive.
func templateLiteral(s string) (out string, literal, escaped bool) {
	if !strings.Contains(s, "{") {
		return s, true, false
	}
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c == '$' || c == '%') && i+2 < len(s) && s[i+1] == c && s[i+2] == '{':
			buf.WriteByte(c)
			buf.WriteByte('{')
			escaped = true
			i += 2
		case (c == '$' || c == '%') && i+1 < len(s) && s[i+1] == '{':
			return "", false, false
		default:
			buf.WriteByte(c)
		}
	}
	return buf.String(), true, escaped
}
