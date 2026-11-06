package sbt

import "bytes"

// tokKind is the kind of a Scala token.
type tokKind uint8

const (
	tIdent  tokKind = iota // identifier or keyword (backquoted identifiers without the quotes)
	tString                // string literal; text is its contents
	tOp                    // operator characters (`%%`, `:=`, `+=`, `/`)
	tPunct                 // one of ( ) [ ] { } , ; .
	tOther                 // number, character literal or anything else
)

// token is one Scala token. For a string, start/end are the span of its contents.
type token struct {
	kind       tokKind
	text       string
	start, end int
	line       int
	// plain: a string literal whose contents are what Scala reads (not interpolated, no
	// escapes), so it can be rewritten in place.
	plain bool
}

func (t token) is(kind tokKind, text string) bool { return t.kind == kind && t.text == text }

func isOpChar(c byte) bool {
	switch c {
	case '!', '#', '%', '&', '*', '+', '-', '/', ':', '<', '=', '>', '?', '@', '\\', '^', '|', '~':
		return true
	}
	return false
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || '0' <= c && c <= '9' }

// lex splits Scala source into tokens, dropping whitespace and comments (nested block
// comments included). An identifier directly followed by a string literal (`s"…"`) is an
// interpolated string. It never fails: an unterminated literal or comment ends at EOF.
func lex(src []byte) []token {
	var (
		toks []token
		line = 1
	)
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			depth := 0
			for i < n {
				switch {
				case src[i] == '/' && i+1 < n && src[i+1] == '*':
					depth++
					i += 2
				case src[i] == '*' && i+1 < n && src[i+1] == '/':
					depth--
					i += 2
				default:
					if src[i] == '\n' {
						line++
					}
					i++
				}
				if depth == 0 {
					break
				}
			}
		case c == '"':
			t, next, lines := lexString(src, i)
			t.line = line
			toks = append(toks, t)
			line += lines
			i = next
		case c == '`':
			j := i + 1
			for j < n && src[j] != '`' && src[j] != '\n' {
				j++
			}
			toks = append(toks, token{kind: tIdent, text: string(src[i+1 : j]), start: i + 1, end: j, line: line})
			i = min(j+1, n)
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentPart(src[j]) {
				j++
			}
			if j < n && src[j] == '"' { // interpolated string
				t, next, lines := lexString(src, j)
				t.line, t.plain = line, false
				toks = append(toks, t)
				line += lines
				i = next
				continue
			}
			toks = append(toks, token{kind: tIdent, text: string(src[i:j]), start: i, end: j, line: line})
			i = j
		case c == '\'':
			// Character literal 'a' or '\n'; a symbol literal 'sym is left as an identifier.
			j := i + 1
			if j < n && src[j] == '\\' {
				j++
			}
			if j+1 < n && src[j+1] == '\'' {
				toks = append(toks, token{kind: tOther, text: string(src[i : j+2]), start: i, end: j + 2, line: line})
				i = j + 2
				continue
			}
			i++
		case '0' <= c && c <= '9':
			j := i + 1
			for j < n && (isIdentPart(src[j]) || src[j] == '.' && j+1 < n && '0' <= src[j+1] && src[j+1] <= '9') {
				j++
			}
			toks = append(toks, token{kind: tOther, text: string(src[i:j]), start: i, end: j, line: line})
			i = j
		case bytes.IndexByte([]byte("()[]{},;."), c) >= 0:
			toks = append(toks, token{kind: tPunct, text: string(c), start: i, end: i + 1, line: line})
			i++
		case isOpChar(c):
			j := i + 1
			for j < n && isOpChar(src[j]) && !(src[j] == '/' && j+1 < n && (src[j+1] == '/' || src[j+1] == '*')) {
				j++
			}
			toks = append(toks, token{kind: tOp, text: string(src[i:j]), start: i, end: j, line: line})
			i = j
		default:
			toks = append(toks, token{kind: tOther, text: string(c), start: i, end: i + 1, line: line})
			i++
		}
	}
	return toks
}

// lexString reads the string literal (`"…"` or `"""…"""`) starting at the quote at i. It
// returns the token (plain unless it holds an escape), the offset after it and the newlines
// it spans.
func lexString(src []byte, i int) (token, int, int) {
	n := len(src)
	if i+2 < n && src[i+1] == '"' && src[i+2] == '"' {
		end := bytes.Index(src[i+3:], []byte(`"""`))
		if end < 0 {
			end = n - i - 3
		}
		s, e := i+3, i+3+end
		for e+3 < n && src[e+3] == '"' { // """a"""" ends at the last three quotes
			e++
		}
		return token{kind: tString, text: string(src[s:e]), start: s, end: e, plain: true},
			min(e+3, n), bytes.Count(src[s:e], []byte("\n"))
	}
	plain := true
	j := i + 1
	for j < n && src[j] != '"' && src[j] != '\n' {
		if src[j] == '\\' {
			plain = false
			j++
		}
		j++
	}
	j = min(j, n)
	t := token{kind: tString, text: string(src[i+1 : j]), start: i + 1, end: j, plain: plain}
	if j < n && src[j] == '"' {
		j++
	}
	return t, j, 0
}
