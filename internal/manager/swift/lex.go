package swift

import "bytes"

// Token kinds.
const (
	tokIdent  = iota + 1 // identifier or keyword (backticks stripped)
	tokString            // string literal
	tokPunct             // punctuation: one byte, or "..." / "..<"
	tokOther             // number or any other byte run
)

// token is a lexical token of a Swift source.
type token struct {
	kind       int
	start, end int    // byte offsets of the whole token
	text       string // identifier or punctuation; for a plain string its value
	// plain marks a single-line string literal without escapes, interpolation or raw
	// delimiters: its value is src[start+1:end-1].
	plain bool
}

// tokenize splits src into tokens, dropping whitespace and comments (`//` lines and nested
// `/* */` blocks). It never fails: an unterminated string or comment runs to the end of its
// line or of src.
func tokenize(src []byte) []token {
	var toks []token
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '/' && i+1 < n && src[i+1] == '/':
			if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = n
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '"' || c == '#' && rawStringAt(src, i):
			t := lexString(src, i)
			toks = append(toks, t)
			i = t.end
		case c == '`':
			j := i + 1
			for j < n && isIdent(src[j]) {
				j++
			}
			if j < n && src[j] == '`' && j > i+1 {
				toks = append(toks, token{kind: tokIdent, start: i, end: j + 1, text: string(src[i+1 : j])})
				i = j + 1
			} else {
				toks = append(toks, token{kind: tokPunct, start: i, end: i + 1, text: "`"})
				i++
			}
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdent(src[j]) {
				j++
			}
			toks = append(toks, token{kind: tokIdent, start: i, end: j, text: string(src[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && isIdent(src[j]) {
				j++
			}
			toks = append(toks, token{kind: tokOther, start: i, end: j, text: string(src[i:j])})
			i = j
		case c == '.' && i+2 < n && src[i+1] == '.' && (src[i+2] == '.' || src[i+2] == '<'):
			toks = append(toks, token{kind: tokPunct, start: i, end: i + 3, text: string(src[i : i+3])})
			i += 3
		case c < 0x80:
			toks = append(toks, token{kind: tokPunct, start: i, end: i + 1, text: string(c)})
			i++
		default: // non-ASCII bytes outside strings and comments (operators, identifiers)
			j := i + 1
			for j < n && src[j] >= 0x80 {
				j++
			}
			toks = append(toks, token{kind: tokOther, start: i, end: j, text: string(src[i:j])})
			i = j
		}
	}
	return toks
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// skipBlockComment returns the offset after the (nested) block comment starting at i.
func skipBlockComment(src []byte, i int) int {
	depth := 0
	for n := len(src); i < n; {
		switch {
		case src[i] == '/' && i+1 < n && src[i+1] == '*':
			depth++
			i += 2
		case src[i] == '*' && i+1 < n && src[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(src)
}

// rawStringAt reports whether the '#' at i starts a raw string literal (`#"…"#`, `##"…"##`).
func rawStringAt(src []byte, i int) bool {
	for i < len(src) && src[i] == '#' {
		i++
	}
	return i < len(src) && src[i] == '"'
}

// lexString lexes the string literal starting at i (`"…"`, `"""…"""` or a raw string with
// `#` delimiters). Interpolations `\(…)` may nest parentheses and strings.
func lexString(src []byte, i int) token {
	start, n := i, len(src)
	hashes := 0
	for i < n && src[i] == '#' {
		hashes++
		i++
	}
	t := token{kind: tokString, start: start}
	closing := func(j int, quotes int) bool { // quotes followed by the delimiter hashes at j
		for k := range quotes {
			if j+k >= n || src[j+k] != '"' {
				return false
			}
		}
		for k := range hashes {
			if j+quotes+k >= n || src[j+quotes+k] != '#' {
				return false
			}
		}
		return true
	}
	if i+2 < n && src[i+1] == '"' && src[i+2] == '"' { // multi-line
		for j := i + 3; j < n; j++ {
			if src[j] == '\\' && hashes == 0 {
				j++
				continue
			}
			if closing(j, 3) {
				t.end = j + 3 + hashes
				return t
			}
		}
		t.end = n
		return t
	}
	plain := hashes == 0
	for j := i + 1; j < n; j++ {
		switch c := src[j]; {
		case c == '\n' || c == '\r':
			t.end = j
			return t
		case c == '\\' && hashes == 0:
			plain = false
			if j+1 < n && src[j+1] == '(' {
				j = skipInterpolation(src, j+1) - 1
			} else {
				j++
			}
		case c == '"' && closing(j, 1):
			t.end = j + 1 + hashes
			if plain {
				t.plain, t.text = true, string(src[i+1:j])
			}
			return t
		}
	}
	t.end = n
	return t
}

// skipInterpolation returns the offset after the parenthesized interpolation starting at the
// '(' at i, skipping nested parentheses and string literals.
func skipInterpolation(src []byte, i int) int {
	depth := 0
	for n := len(src); i < n; {
		switch src[i] {
		case '(':
			depth++
			i++
		case ')':
			depth--
			i++
			if depth == 0 {
				return i
			}
		case '"':
			i = lexString(src, i).end
		case '\n':
			return i
		default:
			i++
		}
	}
	return len(src)
}

// arg is one argument of a call: its label ("" when unlabeled) and value tokens.
type arg struct {
	label string
	toks  []token // the value, without the label and colon
	start int     // offset of the argument's first token (its label when labeled)
	end   int     // offset after its last token
}

// call is a `.package(…)` call.
type call struct {
	start int // offset of the leading '.'
	args  []arg
}

// packageCalls returns every `.package(…)` call in toks (also `Package.Dependency.package(…)`),
// in source order. Calls are not nested.
func packageCalls(toks []token) []call {
	var calls []call
	for i := 0; i+2 < len(toks); i++ {
		if toks[i].kind != tokPunct || toks[i].text != "." || toks[i+1].kind != tokIdent || toks[i+1].text != "package" ||
			toks[i+2].kind != tokPunct || toks[i+2].text != "(" {
			continue
		}
		c, next := parseArgs(toks, i+3)
		c.start = toks[i].start
		calls = append(calls, c)
		i = next - 1
	}
	return calls
}

// parseArgs splits the arguments from toks[i] up to the closing parenthesis of the call and
// returns them with the index after that parenthesis (len(toks) when unclosed).
func parseArgs(toks []token, i int) (call, int) {
	var c call
	depth := 0
	first := i
	flush := func(end int) {
		if end <= first {
			return
		}
		a := arg{toks: toks[first:end], start: toks[first].start, end: toks[end-1].end}
		if len(a.toks) >= 2 && a.toks[0].kind == tokIdent && a.toks[1].kind == tokPunct && a.toks[1].text == ":" {
			a.label, a.toks = a.toks[0].text, a.toks[2:]
		}
		c.args = append(c.args, a)
	}
	for ; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokPunct {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				flush(i)
				return c, i + 1
			}
			depth--
		case ",":
			if depth == 0 {
				flush(i)
				first = i + 1
			}
		}
	}
	flush(len(toks))
	return c, len(toks)
}
