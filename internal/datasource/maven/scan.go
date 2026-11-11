package mavends

import "bytes"

// metaDoc is what a maven-metadata.xml document says about an artifact.
type metaDoc struct {
	versions    []string
	lastUpdated string // `versioning>lastUpdated` text, "" when the document has none
}

// scanVersions reads the non-empty, trimmed `versioning>versions>version` texts and the
// trimmed `versioning>lastUpdated` text of a maven-metadata.xml document without
// encoding/xml. It handles only plain markup: an optional leading XML declaration,
// unprefixed element names, quoted attributes and ASCII text without entities. Anything
// else (comments, CDATA, processing instructions, entities, non-ASCII bytes, unbalanced or
// unterminated elements) returns ok == false and the caller decodes with encoding/xml
// instead, which keeps its results and errors.
func scanVersions(b []byte) (doc metaDoc, ok bool) {
	i := skipSpace(b, 0)
	if bytes.HasPrefix(b[i:], []byte("<?xml")) {
		end := bytes.Index(b[i:], []byte("?>"))
		if end < 0 {
			return metaDoc{}, false
		}
		decl := b[i+2 : i+end]
		if _, ok := startTag(bytes.TrimRight(decl, " \t\n\r")); !ok ||
			!bytes.HasPrefix(decl, []byte(`xml version="1.0"`)) && !bytes.HasPrefix(decl, []byte(`xml version='1.0'`)) {
			return metaDoc{}, false
		}
		i += end + 2
	}
	var (
		stack  [8][]byte // element names on the path; deeper levels are counted, not kept
		depth  int
		in     bool // inside versioning>versions>version
		inLast bool // inside versioning>lastUpdated
	)
	for {
		lt := bytes.IndexByte(b[i:], '<')
		if lt < 0 {
			return metaDoc{}, false // root never closed
		}
		text := b[i : i+lt]
		if !plainText(text) || bytes.Contains(text, []byte("]]>")) || depth == 0 && len(bytes.TrimSpace(text)) != 0 {
			return metaDoc{}, false
		}
		if in || inLast {
			if bytes.IndexByte(text, '\r') >= 0 {
				return metaDoc{}, false
			}
			v := bytes.TrimSpace(text)
			switch {
			case inLast:
				doc.lastUpdated = string(v) // the last one wins, as in encoding/xml
			case len(v) != 0:
				doc.versions = append(doc.versions, string(v))
			}
		}
		i += lt + 1
		gt := bytes.IndexByte(b[i:], '>')
		if gt < 0 {
			return metaDoc{}, false
		}
		tag := b[i : i+gt]
		i += gt + 1
		if len(tag) > 0 && tag[0] == '/' {
			name := bytes.TrimRight(tag[1:], " \t\n")
			if depth == 0 || depth <= len(stack) && !bytes.Equal(name, stack[depth-1]) || !validName(name) {
				return metaDoc{}, false
			}
			if depth > len(stack) {
				return metaDoc{}, false // cannot check the name; let encoding/xml do it
			}
			depth--
			in, inLast = false, false
			if depth == 0 {
				return doc, true
			}
			continue
		}
		selfClose := len(tag) > 0 && tag[len(tag)-1] == '/'
		if selfClose {
			tag = tag[:len(tag)-1]
		}
		name, ok := startTag(tag)
		if !ok || in || inLast {
			return metaDoc{}, false // `<version>` with child elements, or odd markup
		}
		last := depth == 2 && bytes.Equal(name, []byte("lastUpdated")) && bytes.Equal(stack[1], []byte("versioning"))
		if selfClose {
			if last {
				doc.lastUpdated = "" // an empty element, as encoding/xml reads it
			}
			if depth == 0 {
				return doc, true
			}
			continue
		}
		if depth < len(stack) {
			stack[depth] = name
		}
		depth++
		in = depth == 4 && bytes.Equal(name, []byte("version")) &&
			bytes.Equal(stack[2], []byte("versions")) && bytes.Equal(stack[1], []byte("versioning"))
		inLast = last
	}
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// plainText reports whether text is ASCII character data without entities.
func plainText(text []byte) bool {
	for _, c := range text {
		if c >= 0x80 || c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == '&' {
			return false
		}
	}
	return true
}

func validName(n []byte) bool {
	if len(n) == 0 || n[0] >= '0' && n[0] <= '9' || n[0] == '-' || n[0] == '.' {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// startTag returns the element name of a start tag body and checks its attributes
// are name="value" or name='value' pairs with plain values.
func startTag(tag []byte) ([]byte, bool) {
	n := 0
	for n < len(tag) && tag[n] != ' ' && tag[n] != '\t' && tag[n] != '\n' && tag[n] != '\r' {
		n++
	}
	name := tag[:n]
	if !validName(name) {
		return nil, false
	}
	rest := tag[n:]
	for {
		j := skipSpace(rest, 0)
		if j == len(rest) {
			return name, true
		}
		if j == 0 {
			return nil, false
		}
		rest = rest[j:]
		eq := bytes.IndexByte(rest, '=')
		if eq < 0 || !validName(rest[:eq]) || eq+1 >= len(rest) {
			return nil, false
		}
		q := rest[eq+1]
		if q != '"' && q != '\'' {
			return nil, false
		}
		end := bytes.IndexByte(rest[eq+2:], q)
		if end < 0 {
			return nil, false
		}
		v := rest[eq+2 : eq+2+end]
		if !plainText(v) || bytes.IndexByte(v, '<') >= 0 {
			return nil, false
		}
		rest = rest[eq+3+end:]
	}
}
