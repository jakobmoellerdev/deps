package beam

import (
	"bytes"
	"slices"
	"strings"
)

// The scanner below reads just enough Elixir to find the dependency tuples of a mix.exs:
// comments, strings, charlists, heredocs, sigils and character literals are skipped as
// opaque, `do`/`fn` … `end` blocks are counted, and brackets are matched.

// isIdent reports whether c can continue an identifier.
func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// sigilClose maps a sigil's opening delimiter to its closing one.
var sigilClose = map[byte]byte{'/': '/', '|': '|', '"': '"', '\'': '\'', '(': ')', '[': ']', '{': '}', '<': '>'}

// skipOpaque returns the end of the comment, string, charlist, heredoc, sigil or character
// literal starting at i, or i when none does.
func skipOpaque(src []byte, i int) int {
	if i >= len(src) {
		return i
	}
	switch c := src[i]; {
	case c == '#':
		if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
			return i + j
		}
		return len(src)
	case c == '"' || c == '\'':
		return skipQuoted(src, i, c, true)
	case c == '?' && (i == 0 || !isIdent(src[i-1])) && i+1 < len(src):
		if src[i+1] == '\\' && i+2 < len(src) {
			return i + 3
		}
		return i + 2
	case c == '~' && i+1 < len(src) && (src[i+1] >= 'a' && src[i+1] <= 'z' || src[i+1] >= 'A' && src[i+1] <= 'Z'):
		j := i + 1
		upper := src[j] >= 'A' && src[j] <= 'Z'
		for j < len(src) && (src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z') {
			j++
		}
		if j == len(src) {
			return j
		}
		d := src[j]
		var end int
		switch closeCh, ok := sigilClose[d]; {
		case (d == '"' || d == '\'') && bytes.HasPrefix(src[j:], []byte{d, d, d}):
			end = skipQuoted(src, j, d, !upper)
		case ok:
			end = skipDelimited(src, j, d, closeCh, !upper)
		default:
			return j
		}
		for end < len(src) && (src[end] >= 'a' && src[end] <= 'z' || src[end] >= 'A' && src[end] <= 'Z') {
			end++ // modifiers
		}
		return end
	}
	return i
}

// skipQuoted returns the end of the string or charlist quoted with q at i (a heredoc when
// tripled), with escapes and, when interp, `#{…}` interpolations.
func skipQuoted(src []byte, i int, q byte, interp bool) int {
	if bytes.HasPrefix(src[i:], []byte{q, q, q}) {
		j := i + 3
		for j < len(src) {
			switch {
			case src[j] == '\\' && interp:
				j += 2
			case interp && src[j] == '#' && j+1 < len(src) && src[j+1] == '{':
				j = skipCode(src, j+2, '}')
			case bytes.HasPrefix(src[j:], []byte{q, q, q}) && heredocLineStart(src, j):
				return j + 3
			default:
				j++
			}
		}
		return len(src)
	}
	return skipDelimited(src, i, q, q, interp)
}

// heredocLineStart reports whether only blanks precede j on its line (a heredoc's closing
// delimiter).
func heredocLineStart(src []byte, j int) bool {
	for k := j - 1; k >= 0 && src[k] != '\n'; k-- {
		if src[k] != ' ' && src[k] != '\t' {
			return false
		}
	}
	return true
}

// skipDelimited returns the end of the text opened by open at i and closed by closeCh, with
// escapes and, when interp, `#{…}` interpolations; nested pairs of a bracket delimiter count.
func skipDelimited(src []byte, i int, open, closeCh byte, interp bool) int {
	depth := 0
	for j := i + 1; j < len(src); j++ {
		switch c := src[j]; {
		case c == '\\':
			j++
		case interp && c == '#' && j+1 < len(src) && src[j+1] == '{':
			j = skipCode(src, j+2, '}') - 1
		case c == closeCh && depth == 0:
			return j + 1
		case c == closeCh:
			depth--
		case c == open && open != closeCh:
			depth++
		}
	}
	return len(src)
}

// skipCode returns the offset after the closing bracket closeCh that ends the code starting at
// i, skipping opaque text and nested brackets.
func skipCode(src []byte, i int, closeCh byte) int {
	for i < len(src) {
		if j := skipOpaque(src, i); j > i {
			i = j
			continue
		}
		switch c := src[i]; c {
		case closeCh:
			return i + 1
		case '(':
			i = skipCode(src, i+1, ')')
		case '[':
			i = skipCode(src, i+1, ']')
		case '{':
			i = skipCode(src, i+1, '}')
		default:
			i++
		}
	}
	return len(src)
}

// word returns the identifier starting at i (with a trailing `?` or `!`), or "" when i does
// not start one: a letter or `_` not preceded by an identifier character, `.`, `:` or `@`.
func word(src []byte, i int) string {
	if i > 0 && (isIdent(src[i-1]) || src[i-1] == '.' || src[i-1] == ':' || src[i-1] == '@') {
		return ""
	}
	return ident(src, i)
}

// ident returns the identifier starting at i (with a trailing `?` or `!`), or "".
func ident(src []byte, i int) string {
	if i >= len(src) {
		return ""
	}
	if c := src[i]; !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		return ""
	}
	j := i + 1
	for j < len(src) && isIdent(src[j]) {
		j++
	}
	if j < len(src) && (src[j] == '?' || src[j] == '!') {
		j++
	}
	return string(src[i:j])
}

// keywordKey reports whether the identifier ending at j is a keyword key (`do:`, `only:`):
// followed by `:` and a blank.
func keywordKey(src []byte, j int) bool {
	return j+1 < len(src) && src[j] == ':' && (src[j+1] == ' ' || src[j+1] == '\t' || src[j+1] == '\n' || src[j+1] == '\r')
}

// span is a byte range of the source.
type span struct{ start, end int }

// depsBodies returns the bodies of the functions named `deps` or ending in `_deps` (`defp
// deps do … end`, `defp target_deps(:app), do: […]`): a `do` block up to its `end`, or the
// expression of a `do:` keyword (a bracketed list, else the rest of the line).
func depsBodies(src []byte) []span {
	var out []span
	type open struct{ start, depth int }
	var pending []open // deps functions whose do block is open, innermost last
	depth := 0
	want := false // a deps function head was read; its do/do: is next
	for i := 0; i < len(src); {
		if j := skipOpaque(src, i); j > i {
			i = j
			continue
		}
		w := word(src, i)
		if w == "" {
			i++
			continue
		}
		end := i + len(w)
		key := keywordKey(src, end)
		switch {
		case key && w == "do" && want:
			want = false
			j := end + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
				j++
			}
			e := j
			if j < len(src) && src[j] == '[' {
				e = skipCode(src, j+1, ']')
			} else {
				for e < len(src) && src[e] != '\n' {
					if k := skipOpaque(src, e); k > e {
						e = k
						continue
					}
					if src[e] == '(' || src[e] == '[' || src[e] == '{' {
						e = skipCode(src, e+1, map[byte]byte{'(': ')', '[': ']', '{': '}'}[src[e]])
						continue
					}
					e++
				}
			}
			out = append(out, span{j, e})
			end = e
		case key:
		case w == "do" || w == "fn":
			depth++
			if w == "do" && want {
				want = false
				pending = append(pending, open{end, depth})
			}
		case w == "end":
			if n := len(pending); n > 0 && pending[n-1].depth == depth {
				out = append(out, span{pending[n-1].start, i})
				pending = pending[:n-1]
			}
			depth--
		case w == "def" || w == "defp":
			j := end
			for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
				j++
			}
			if j < len(src) {
				if name := word(src, j); name == "deps" || strings.HasSuffix(name, "_deps") {
					want = true
					end = j + len(name)
				}
			}
		}
		i = end
	}
	return out
}

// str is a string literal: its content, the content's span, and whether the bytes are the
// content (no escapes or interpolation).
type str struct {
	s     string
	at    span
	exact bool
}

// literal returns the string literal that is all of the trimmed text src[s.start:s.end], or
// false.
func literal(src []byte, s span) (str, bool) {
	s = trim(src, s)
	if s.end-s.start < 2 || src[s.start] != '"' || bytes.HasPrefix(src[s.start:], []byte(`"""`)) || skipQuoted(src, s.start, '"', true) != s.end {
		return str{}, false
	}
	in := span{s.start + 1, s.end - 1}
	content := string(src[in.start:in.end])
	return str{s: content, at: in, exact: !strings.ContainsAny(content, "\\") && !strings.Contains(content, "#{")}, true
}

// trim returns s without leading and trailing blanks, newlines and comments.
func trim(src []byte, s span) span {
	for s.start < s.end {
		if c := src[s.start]; c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			s.start++
		} else if c == '#' {
			s.start = min(skipOpaque(src, s.start), s.end)
		} else {
			break
		}
	}
	// a trailing comment ends at a newline, which is trimmed below; trailing comments are
	// cut by scanning forward for the last code byte
	last := s.start
	for i := s.start; i < s.end; {
		if j := skipOpaque(src, i); j > i {
			if src[i] != '#' {
				last = min(j, s.end)
			}
			i = j
			continue
		}
		if c := src[i]; c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			last = i + 1
		}
		i++
	}
	s.end = last
	return s
}

// elements splits the code src[s.start:s.end] at its top-level commas.
func elements(src []byte, s span) []span {
	var out []span
	start := s.start
	for i := s.start; i < s.end; {
		if j := skipOpaque(src, i); j > i {
			i = j
			continue
		}
		switch src[i] {
		case '(':
			i = skipCode(src, i+1, ')')
		case '[':
			i = skipCode(src, i+1, ']')
		case '{':
			i = skipCode(src, i+1, '}')
		case ',':
			out = append(out, span{start, i})
			start = i + 1
			i++
		default:
			i++
		}
	}
	if t := trim(src, span{start, s.end}); t.end > t.start {
		out = append(out, span{start, s.end})
	}
	return out
}

// option is the value of a keyword option of a dependency tuple: its trimmed text, and the
// string literal it is, when it is one.
type option struct {
	raw string
	lit *str
}

// atom returns the name of the atom the option is (`:dev` → dev, `:"x"` is not one), or "".
func (o option) atom() string {
	if len(o.raw) > 1 && o.raw[0] == ':' && ident([]byte(o.raw), 1) == o.raw[1:] {
		return o.raw[1:]
	}
	return ""
}

// entry is a dependency tuple of mix.exs: `{:app, "requirement", opt: value, …}`.
type entry struct {
	app     string
	at      int  // offset of the app atom
	req     *str // the requirement literal; nil when the second element is not a string
	reqText string
	opts    map[string]option
}

// keywords parses the keyword pairs of the elements els (`only: :dev`), or false when one is
// not a keyword pair.
func keywords(src []byte, els []span, opts map[string]option) bool {
	for _, e := range els {
		t := trim(src, e)
		if t.end <= t.start {
			return false
		}
		k := word(src, t.start)
		if k == "" || !keywordKey(src, t.start+len(k)) {
			return false
		}
		v := trim(src, span{t.start + len(k) + 1, t.end})
		o := option{raw: string(src[v.start:v.end])}
		if l, ok := literal(src, v); ok {
			o.lit = &l
		}
		opts[k] = o
	}
	return true
}

// entries returns the dependency tuples in body: list elements (after a `[` or `,`) that are
// tuples whose first element is an atom and whose second is a string (the requirement),
// keyword options, or another expression (a module attribute, a function call); tuples nested
// in another one are not dependencies.
func entries(src []byte, body span) []entry {
	var out []entry
	prev := byte(0) // the last byte of code before i, comments and blanks aside
	for i := body.start; i < body.end; {
		if j := skipOpaque(src, i); j > i {
			if src[i] != '#' {
				prev = src[j-1]
			}
			i = j
			continue
		}
		c := src[i]
		if c != '{' || (prev != '[' && prev != ',') {
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prev = c
			}
			i++
			continue
		}
		end := skipCode(src, i+1, '}')
		prev = '}'
		els := elements(src, span{i + 1, end - 1})
		i = end
		if len(els) < 2 {
			continue
		}
		first := trim(src, els[0])
		if first.end-first.start < 2 || src[first.start] != ':' {
			continue
		}
		app := ident(src, first.start+1)
		if app == "" || first.start+1+len(app) != first.end {
			continue
		}
		e := entry{app: app, at: first.start + 1, opts: map[string]option{}}
		rest := els[1:]
		if second := trim(src, els[1]); second.end > second.start {
			if l, ok := literal(src, second); ok {
				e.req, rest = &l, els[2:]
			} else if !keywords(src, els[1:2], map[string]option{}) {
				e.reqText, rest = string(src[second.start:second.end]), els[2:]
			}
		}
		if len(rest) == 1 {
			if t := trim(src, rest[0]); t.end > t.start && src[t.start] == '[' {
				rest = elements(src, span{t.start + 1, t.end - 1}) // `[only: :dev]`
			}
		}
		if !keywords(src, rest, e.opts) && e.req == nil && e.reqText == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// mixEntries returns the dependency tuples of the deps functions of a mix.exs, in file order.
func mixEntries(src []byte) []entry {
	bodies := depsBodies(src)
	slices.SortFunc(bodies, func(a, b span) int { return a.start - b.start })
	parts := make([][]entry, len(bodies))
	for i, b := range bodies {
		parts[i] = entries(src, b)
	}
	return slices.Concat(parts...)
}
