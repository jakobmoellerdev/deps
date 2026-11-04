package gradle

import (
	"bytes"
	"regexp"
	"sort"
)

// source is a scanned Gradle script or Kotlin/Groovy source file.
type source struct {
	src []byte
	// code is src with comments, string contents (template expressions included) and
	// multi-line strings blanked to spaces, newlines and offsets kept, so regular expressions
	// see only code; the quotes of single-line strings stay.
	code   []byte
	strs   []strLit // single-line string literal contents, by start
	blocks []block
	lines  lines
	groovy bool
}

// strLit is the content span of a string literal.
type strLit struct{ start, end int }

// block is a `label {` … `}` region of code.
type block struct {
	label     string // identifier path before the brace (call arguments skipped), "" = none
	object    bool   // `object X {`, `class X {`, `interface X {`
	open, end int    // offsets of '{' and '}' (end = len(src) when unterminated)
	parent    int    // index of the enclosing block, -1 = none
}

func scan(src []byte, groovy bool) *source {
	s := &source{src: src, code: bytes.Clone(src), lines: newLines(src), groovy: groovy}
	code := s.code
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if code[i] != '\n' {
				code[i] = ' '
			}
		}
	}
	n := len(src)
	for i := 0; i < n; i++ { // the body advances i
		switch c := src[i]; {
		case c == '"' || c == '\'':
			if i+2 < n && src[i+1] == c && src[i+2] == c {
				end := n
				if e := bytes.Index(src[i+3:], []byte{c, c, c}); e >= 0 {
					end = i + 3 + e + 3
				}
				blank(i, end)
				i = end - 1
				continue
			}
			end := stringEnd(src, i+1, c)
			if end < 0 { // unterminated: the line is not code
				e := bytes.IndexByte(src[i:], '\n')
				if e < 0 {
					e = n - i
				}
				blank(i+1, i+e)
				i += e - 1
				continue
			}
			s.strs = append(s.strs, strLit{i + 1, end})
			blank(i+1, end)
			i = end
		case c == '/' && i+1 < n && src[i+1] == '/':
			e := bytes.IndexByte(src[i:], '\n')
			if e < 0 {
				e = n - i
			}
			blank(i, i+e)
			i += e - 1
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := n
			if e := bytes.Index(src[i+2:], []byte("*/")); e >= 0 {
				end = i + 2 + e + 2
			}
			blank(i, end)
			i = end - 1
		}
	}
	s.blocks = blocks(code)
	return s
}

// stringEnd returns the offset of the quote q closing the single-line string whose content
// starts at i, skipping escapes and (in double-quoted strings) `${…}` templates; -1 when the
// line ends first.
func stringEnd(src []byte, i int, q byte) int {
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case '\n':
			return -1
		case q:
			return i
		case '$':
			if q == '"' && i+1 < len(src) && src[i+1] == '{' {
				if i = templateEnd(src, i+2); i < 0 {
					return -1
				}
				continue
			}
		}
		i++
	}
	return -1
}

// templateEnd returns the offset after the '}' closing a template expression starting at i.
func templateEnd(src []byte, i int) int {
	depth := 1
	for i < len(src) {
		switch c := src[i]; c {
		case '"', '\'':
			e := stringEnd(src, i+1, c)
			if e < 0 {
				return -1
			}
			i = e + 1
			continue
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		case '\n':
			return -1
		}
		i++
	}
	return -1
}

// blocks returns the brace blocks of code in order of their opening brace.
func blocks(code []byte) []block {
	var out []block
	var stack []int
	for i, c := range code {
		switch c {
		case '{':
			b := block{open: i, end: len(code), parent: -1}
			if len(stack) > 0 {
				b.parent = stack[len(stack)-1]
			}
			b.label, b.object = label(code, i)
			stack = append(stack, len(out))
			out = append(out, b)
		case '}':
			if len(stack) > 0 {
				out[stack[len(stack)-1]].end = i
				stack = stack[:len(stack)-1]
			}
		}
	}
	return out
}

// typeDeclRe is a type declaration on the line of a block's brace: `object Versions : Base() {`.
var typeDeclRe = regexp.MustCompile(`\b(?:object|class|interface)[ \t]+([A-Za-z_]\w*)`)

// label reads the identifier path before the '{' at i, skipping one argument list; a type
// declaration directly before the brace on its line names the block.
func label(code []byte, i int) (name string, object bool) {
	line := code[bytes.LastIndexByte(code[:i], '\n')+1 : i]
	if ms := typeDeclRe.FindAllSubmatchIndex(line, -1); ms != nil {
		if m := ms[len(ms)-1]; !bytes.ContainsAny(line[m[1]:], "{}") {
			return string(line[m[2]:m[3]]), true
		}
	}
	j := skipBack(code, i-1)
	if j >= 0 && code[j] == ')' {
		depth := 0
		for ; j >= 0; j-- {
			if code[j] == ')' {
				depth++
			} else if code[j] == '(' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if j < 0 {
			return "", false
		}
		j = skipBack(code, j-1)
	}
	end := j + 1
	for j >= 0 && (isIdent(code[j]) || code[j] == '.') {
		j--
	}
	return string(code[j+1 : end]), false
}

func skipBack(code []byte, j int) int {
	for j >= 0 && (code[j] == ' ' || code[j] == '\t' || code[j] == '\n' || code[j] == '\r') {
		j--
	}
	return j
}

func isIdent(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

// lit returns the string literal whose content starts at off.
func (s *source) lit(off int) (strLit, bool) {
	i := sort.Search(len(s.strs), func(i int) bool { return s.strs[i].start >= off })
	if i < len(s.strs) && s.strs[i].start == off {
		return s.strs[i], true
	}
	return strLit{}, false
}

// litAfter returns the string literal whose opening quote is at off (code[off] is a quote).
func (s *source) litAfter(off int) (strLit, bool) {
	if off >= len(s.code) || s.code[off] != '"' && s.code[off] != '\'' {
		return strLit{}, false
	}
	return s.lit(off + 1)
}

func (s *source) text(l strLit) string { return string(s.src[l.start:l.end]) }

// enclosing returns the innermost block containing off, -1 = none.
func (s *source) enclosing(off int) int {
	i := sort.Search(len(s.blocks), func(i int) bool { return s.blocks[i].open >= off }) - 1
	for i >= 0 && s.blocks[i].end < off {
		i = s.blocks[i].parent
	}
	return i
}

// within reports whether block b or one of its ancestors is labeled name.
func (s *source) within(b int, name string) bool {
	for ; b >= 0; b = s.blocks[b].parent {
		if s.blocks[b].label == name {
			return true
		}
	}
	return false
}

// skipSpace returns the first offset at or after i that is not a space or tab.
func (s *source) skipSpace(i int) int {
	for i < len(s.code) && (s.code[i] == ' ' || s.code[i] == '\t') {
		i++
	}
	return i
}

// skipBlank also skips newlines.
func (s *source) skipBlank(i int) int {
	for i < len(s.code) && (s.code[i] == ' ' || s.code[i] == '\t' || s.code[i] == '\n' || s.code[i] == '\r') {
		i++
	}
	return i
}
