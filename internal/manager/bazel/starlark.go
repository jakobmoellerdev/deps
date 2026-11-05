package bazel

import (
	"bytes"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Token kinds of the Starlark subset MODULE.bazel files are written in.
const (
	tokIdent  = iota + 1 // identifier or keyword
	tokString            // string literal
	tokPunct             // operator or bracket
	tokOther             // number or any other byte run
)

// token is a lexical token; for a string, text is its value as written between the quotes.
type token struct {
	kind       int
	start, end int    // byte offsets of the whole token
	text       string // identifier, punctuation or string value
	// plain marks a string without escapes: text is src[valStart:valEnd].
	plain            bool
	valStart, valEnd int
}

// tokenize splits src into tokens, dropping blanks, newlines and `#` comments. It never
// fails: an unterminated string runs to the end of its line (triple-quoted: of src).
func tokenize(src []byte) []token {
	var toks []token
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\\':
			i++
		case c == '#':
			if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = n
			}
		case c == '"' || c == '\'':
			t := lexString(src, i, i)
			toks = append(toks, t)
			i = t.end
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdent(src[j]) {
				j++
			}
			if j < n && (src[j] == '"' || src[j] == '\'') && isStringPrefix(src[i:j]) {
				t := lexString(src, i, j)
				toks = append(toks, t)
				i = t.end
				continue
			}
			toks = append(toks, token{kind: tokIdent, start: i, end: j, text: string(src[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && (isIdent(src[j]) || src[j] == '.') {
				j++
			}
			toks = append(toks, token{kind: tokOther, start: i, end: j, text: string(src[i:j])})
			i = j
		case c == '=' && i+1 < n && src[i+1] == '=', (c == '!' || c == '<' || c == '>') && i+1 < n && src[i+1] == '=':
			toks = append(toks, token{kind: tokPunct, start: i, end: i + 2, text: string(src[i : i+2])})
			i += 2
		case c < 0x80:
			toks = append(toks, token{kind: tokPunct, start: i, end: i + 1, text: string(c)})
			i++
		default:
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

func isIdentStart(c byte) bool { return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isIdent(c byte) bool      { return isIdentStart(c) || '0' <= c && c <= '9' }

// isStringPrefix reports whether p is a Starlark string prefix (raw and bytes literals).
func isStringPrefix(p []byte) bool {
	switch strings.ToLower(string(p)) {
	case "r", "b", "rb", "br":
		return true
	}
	return false
}

// lexString lexes the string literal starting at start whose quote is at q (after any
// prefix). A string is plain when it has no backslash: its value is then the bytes between
// the quotes.
func lexString(src []byte, start, q int) token {
	quote := src[q]
	open := 1
	if q+2 < len(src) && src[q+1] == quote && src[q+2] == quote {
		open = 3
	}
	t := token{kind: tokString, start: start, valStart: q + open, plain: true}
	i := t.valStart
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\\':
			t.plain = false
			i += 2
			continue
		case c == '\n' && open == 1:
			t.valEnd, t.end = i, i
			t.plain = false
			return t
		case c == quote && (open == 1 || i+2 < len(src) && src[i+1] == quote && src[i+2] == quote):
			t.valEnd, t.end = i, i+open
			t.text = string(src[t.valStart:t.valEnd])
			return t
		}
		i++
	}
	t.valEnd, t.end, t.plain = len(src), len(src), false
	return t
}

// Value kinds of a call argument.
const (
	valOther  = iota // any other expression (a variable, a call, True, a concatenation)
	valString        // one string literal
	valList          // a list of string literals
)

// value is a call argument's value.
type value struct {
	kind  int
	str   token   // valString
	items []token // valList
}

// span is the byte span of a plain string's value; ok is false for any other value.
func (v value) span() (dep.Span, bool) {
	if v.kind != valString || !v.str.plain {
		return dep.Span{}, false
	}
	return dep.Span{Start: v.str.valStart, End: v.str.valEnd}, true
}

// call is a call statement or expression: `fn(…)`, or `recv.fn(…)` (an extension tag).
type call struct {
	recv, fn string
	start    int              // offset of the callee
	assign   string           // the variable a `x = fn(…)` assigns the result to
	kwargs   map[string]value // keyword arguments by name; the last one wins
	args     []value          // positional arguments
}

// str returns the plain string keyword argument name; ok is false when it is absent, set is
// false only when it is absent (present but not a plain string: set, !ok).
func (c call) str(name string) (s string, sp dep.Span, ok, set bool) {
	v, set := c.kwargs[name]
	if !set {
		return "", dep.Span{}, false, false
	}
	sp, ok = v.span()
	return v.str.text, sp, ok, true
}

// calls returns every call of toks in source order, nested ones (inside the arguments of
// another call, a list comprehension) included.
func calls(toks []token) []call {
	var out []call
	for i := range toks {
		if toks[i].kind != tokIdent || i > 0 && toks[i-1].text == "." && toks[i-1].kind == tokPunct {
			continue
		}
		c := call{fn: toks[i].text, start: toks[i].start}
		j := i + 1
		if j+2 < len(toks) && isPunct(toks[j], ".") && toks[j+1].kind == tokIdent && isPunct(toks[j+2], "(") {
			c.recv, c.fn = c.fn, toks[j+1].text
			j += 2
		}
		if j >= len(toks) || !isPunct(toks[j], "(") {
			continue
		}
		if i >= 2 && isPunct(toks[i-1], "=") && toks[i-2].kind == tokIdent && (i < 3 || !isPunct(toks[i-3], ".")) {
			c.assign = toks[i-2].text
		}
		c.kwargs, c.args = parseArgs(toks, j)
		out = append(out, c)
	}
	return out
}

func isPunct(t token, s string) bool { return t.kind == tokPunct && t.text == s }

// parseArgs splits the argument list opened by toks[open] ("(") at its top-level commas into
// keyword and positional arguments.
func parseArgs(toks []token, open int) (map[string]value, []value) {
	kw := map[string]value{}
	var pos []value
	depth := 0
	start := open + 1
	flush := func(end int) {
		a := toks[start:end]
		if len(a) == 0 {
			return
		}
		if len(a) >= 2 && a[0].kind == tokIdent && isPunct(a[1], "=") {
			kw[a[0].text] = valueOf(a[2:])
			return
		}
		pos = append(pos, valueOf(a))
	}
	for i := open; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokPunct {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				flush(i)
				return kw, pos
			}
		case ",":
			if depth == 1 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(toks))
	return kw, pos
}

// valueOf classifies the tokens of one argument value.
func valueOf(a []token) value {
	if len(a) == 1 && a[0].kind == tokString {
		return value{kind: valString, str: a[0]}
	}
	if len(a) < 2 || !isPunct(a[0], "[") || !isPunct(a[len(a)-1], "]") {
		return value{}
	}
	var items []token
	for i := 1; i < len(a)-1; i++ {
		if a[i].kind != tokString {
			return value{}
		}
		items = append(items, a[i])
		if i+1 < len(a)-1 {
			if !isPunct(a[i+1], ",") {
				return value{}
			}
			i++
		}
	}
	return value{kind: valList, items: items}
}
