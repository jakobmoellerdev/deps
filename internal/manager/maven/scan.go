package maven

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// scan builds the element tree of src like decode, without decoding the document with
// encoding/xml: it handles elements, attributes, character data, CDATA sections, comments and
// processing instructions, and has encoding/xml decode only the character data it would
// change (references, carriage returns, CDATA, non-ASCII). ok is false where the tree could
// differ from decode's, which then parses src: references in attribute values, DOCTYPE
// declarations, non-ASCII names, prefixed names other than `prefix:local`, an unsupported
// XML version, and everything that is not well-formed.
func scan(src []byte) (doc *node, ok bool) {
	doc = &node{}
	cur := doc
	var open [][]byte // raw names of the open elements
	line, pos := 1, 0
	for p := 0; p < len(src); {
		if src[p] != '<' {
			end := p + bytes.IndexByte(src[p:], '<')
			if end < p {
				end = len(src)
			}
			if !scanText(cur, src, p, end) {
				return nil, false
			}
			p = end
			continue
		}
		start := p
		p++
		if p == len(src) {
			return nil, false
		}
		switch src[p] {
		case '/':
			name, q := scanName(src, p+1, true)
			q = skipSpace(src, q)
			if name == nil || q == len(src) || src[q] != '>' || len(open) == 0 || !bytes.Equal(open[len(open)-1], name) {
				return nil, false
			}
			open = open[:len(open)-1]
			cur = cur.parent
			p = q + 1
		case '?':
			if p = scanProcInst(src, p+1); p < 0 {
				return nil, false
			}
		case '!':
			end := -1
			switch {
			case bytes.HasPrefix(src[p:], []byte("!--")):
				// The first "--" ends the comment and must be followed by '>'.
				if i := bytes.Index(src[p+3:], []byte("--")); i >= 0 && p+3+i+2 < len(src) && src[p+3+i+2] == '>' {
					end = p + 3 + i + 3
				}
			case bytes.HasPrefix(src[p:], []byte("![CDATA[")):
				if i := bytes.Index(src[p+8:], []byte("]]>")); i >= 0 {
					end = p + 8 + i + 3
					if !decodeText(cur, src, start, end) {
						end = -1
					}
				}
			}
			if end < 0 {
				return nil, false
			}
			p = end
		default:
			name, q := scanName(src, p, true)
			if name == nil {
				return nil, false
			}
			q, empty := scanAttrs(src, q)
			if q < 0 {
				return nil, false
			}
			line += bytes.Count(src[pos:start], []byte{'\n'})
			pos = start
			local := name
			if _, after, found := bytes.Cut(name, []byte{':'}); found {
				local = after
			}
			n := &node{name: string(local), parent: cur, line: line}
			cur.kids = append(cur.kids, n)
			if !empty {
				open = append(open, name)
				cur = n
			}
			p = q
		}
	}
	return doc, len(open) == 0
}

// scanText adds the character data src[p:end] to n as decode does; it is false where
// encoding/xml rejects it.
func scanText(n *node, src []byte, p, end int) bool {
	raw := src[p:end]
	cr := false
	for i, c := range raw {
		switch {
		case c == '&' || c >= utf8.RuneSelf || c < 0x20 && c != '\t' && c != '\n' && c != '\r' ||
			c == '>' && i >= 2 && raw[i-1] == ']' && raw[i-2] == ']':
			return decodeText(n, src, p, end)
		case c == '\r':
			cr = true
		}
	}
	b := bytes.TrimSpace(raw)
	if len(b) == 0 {
		return true
	}
	if cr && bytes.IndexByte(b, '\r') >= 0 { // encoding/xml turns it into '\n'
		return decodeText(n, src, p, end)
	}
	n.pieces++
	n.value += string(b)
	if n.pieces == 1 {
		i := p + bytes.Index(raw, b)
		n.span = dep.Span{Start: i, End: i + len(b)}
	} else {
		n.span = dep.Span{}
	}
	return true
}

// decodeText adds the character data or CDATA section src[start:end] to n, decoded by
// encoding/xml like decode does, which depends on nothing before it. It is false where
// encoding/xml rejects it.
func decodeText(n *node, src []byte, start, end int) bool {
	raw := src[start:end]
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Entity = xml.HTMLEntity
	tok, err := dec.Token()
	t, ok := tok.(xml.CharData)
	if err != nil || !ok || dec.InputOffset() != int64(len(raw)) {
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false
	}
	s := strings.TrimSpace(string(t))
	if s == "" {
		return true
	}
	n.pieces++
	n.value += s
	i := bytes.Index(raw, []byte(s))
	if n.pieces == 1 && i >= 0 && string(bytes.TrimSpace(raw)) == s {
		n.span = dep.Span{Start: start + i, End: start + i + len(s)}
	} else {
		n.span = dep.Span{}
	}
	return true
}

// scanAttrs skips the attributes and the end of the start tag beginning at src[p]; it
// returns the offset after the tag and whether the element is empty (`/>`), q < 0 where
// scan falls back.
func scanAttrs(src []byte, p int) (q int, empty bool) {
	for {
		p = skipSpace(src, p)
		if p == len(src) {
			return -1, false
		}
		switch src[p] {
		case '>':
			return p + 1, false
		case '/':
			if p+1 == len(src) || src[p+1] != '>' {
				return -1, false
			}
			return p + 2, true
		}
		name, q := scanName(src, p, true)
		if name == nil {
			return -1, false
		}
		q = skipSpace(src, q)
		if q == len(src) || src[q] != '=' {
			return -1, false
		}
		q = skipSpace(src, q+1)
		if q == len(src) || src[q] != '"' && src[q] != '\'' {
			return -1, false
		}
		quote := src[q]
		end := q + 1 + bytes.IndexByte(src[q+1:], quote)
		if end <= q || !attrValue(src[q+1:end]) {
			return -1, false
		}
		p = end + 1
	}
}

// attrValue reports whether decode accepts the quoted attribute value v unchanged.
func attrValue(v []byte) bool {
	for i := 0; i < len(v); {
		switch c := v[i]; {
		case c == '<' || c == '&':
			return false
		case c < utf8.RuneSelf:
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				return false
			}
			i++
		default:
			r, size := utf8.DecodeRune(v[i:])
			if r == utf8.RuneError && size == 1 || !inCharacterRange(r) {
				return false
			}
			i += size
		}
	}
	return true
}

// scanProcInst skips the processing instruction whose target starts at src[p] (after
// "<?"); it returns the offset after "?>", or -1 where scan falls back. Like decode it
// rejects an XML declaration of a version other than 1.0; a declared encoding is read as
// UTF-8 (see decode's CharsetReader).
func scanProcInst(src []byte, p int) int {
	target, q := scanName(src, p, false)
	if target == nil {
		return -1
	}
	q = skipSpace(src, q)
	end := bytes.Index(src[q:], []byte("?>"))
	if end < 0 {
		return -1
	}
	if string(target) == "xml" {
		if v := procInst("version", string(src[q:q+end])); v != "" && v != "1.0" {
			return -1
		}
	}
	return q + end + 2
}

// scanName returns the ASCII XML name at src[p] and the offset after it, or nil. With ns,
// a name holding a colon must be `prefix:local` (decode splits it the same way).
func scanName(src []byte, p int, ns bool) ([]byte, int) {
	q := p
	for q < len(src) && nameByte(src[q]) {
		q++
	}
	if q == p || q < len(src) && src[q] >= utf8.RuneSelf {
		return nil, p
	}
	name := src[p:q]
	if c := name[0]; c >= '0' && c <= '9' || c == '.' || c == '-' {
		return nil, p
	}
	if ns {
		if i := bytes.IndexByte(name, ':'); i >= 0 && (i == 0 || i == len(name)-1 || bytes.IndexByte(name[i+1:], ':') >= 0) {
			return nil, p
		}
	}
	return name, q
}

// nameByte is encoding/xml's isNameByte: the ASCII bytes of a name.
func nameByte(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
		c == '_' || c == ':' || c == '.' || c == '-'
}

// skipSpace returns the offset of the first byte at or after p that is not XML white space.
func skipSpace(src []byte, p int) int {
	for p < len(src) && (src[p] == ' ' || src[p] == '\t' || src[p] == '\n' || src[p] == '\r') {
		p++
	}
	return p
}

// inCharacterRange is encoding/xml's isInCharacterRange: the characters XML allows.
func inCharacterRange(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D || r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF
}

// procInst is encoding/xml's procInst: the value of param in the content of an XML
// declaration, "" when absent.
func procInst(param, s string) string {
	param += "="
	lenp := len(param)
	i := 0
	var sep byte
	for i < len(s) {
		sub := s[i:]
		k := strings.Index(sub, param)
		if k < 0 || lenp+k >= len(sub) {
			return ""
		}
		i += lenp + k + 1
		if c := sub[lenp+k]; c == '\'' || c == '"' {
			sep = c
			break
		}
	}
	if sep == 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], sep)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}
