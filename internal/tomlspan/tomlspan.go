// Package tomlspan parses TOML documents into a tree that keeps the byte span and line of
// every value, so managers can replace single tokens instead of re-serializing the file.
//
// It implements the TOML 1.0 syntax deps needs to read manifests (pyproject.toml, Pipfile):
// standard tables, arrays of tables, dotted and quoted keys, all four string forms, arrays,
// inline tables (also spanning lines, as TOML 1.1 allows) and other scalars kept as raw text.
// Integers, floats, booleans and datetimes are not interpreted.
package tomlspan

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind is the type of a Value.
type Kind uint8

// Value kinds. Scalar is any non-string atom (integer, float, boolean, datetime) as raw text.
const (
	String Kind = iota + 1
	Scalar
	Array
	Table
)

// Value is one TOML value.
type Value struct {
	Kind Kind
	// Str is the decoded string (String) or the raw text (Scalar).
	Str string
	// Start and End delimit the string's content between its quotes (String) or the raw text
	// (Scalar); for arrays and tables they delimit the whole value: from the opening bracket
	// of an inline one, from the header of a table or an array of tables to the end of the
	// last value below it.
	Start, End int
	// Exact reports whether src[Start:End] equals Str byte for byte (no escapes, no trimmed
	// first newline), so the bytes can be replaced in place.
	Exact bool
	Line  int      // 1-based line of Start
	Items []*Value // Array
	// Keys lists the keys of a Table in document order; Fields holds their values.
	Keys   []string
	Fields map[string]*Value

	inline  bool // inline table or static array: closed for later definitions
	defined bool // table header seen (a second [x] is an error)
	aot     bool // array of tables
}

// Get returns the value at the dotted path below v (a Table), or nil.
func (v *Value) Get(path ...string) *Value {
	for _, k := range path {
		if v == nil || v.Kind != Table {
			return nil
		}
		v = v.Fields[k]
	}
	return v
}

// Strings returns the String items of an Array value (nil for other kinds).
func (v *Value) Strings() []*Value {
	if v == nil || v.Kind != Array {
		return nil
	}
	out := make([]*Value, 0, len(v.Items))
	for _, it := range v.Items {
		if it.Kind == String {
			out = append(out, it)
		}
	}
	return out
}

// Error is a syntax error at a 1-based line.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("toml: line %d: %s", e.Line, e.Msg) }

// Parse parses src and returns the root table.
func Parse(src []byte) (*Value, error) {
	p := &parser{src: src, line: 1, names: map[string]string{}}
	root := newTable(0, 1)
	root.defined = true
	cur := root
	for {
		p.skipBlank()
		if p.eof() {
			break
		}
		if p.peek() == '[' {
			t, err := p.header(root)
			if err != nil {
				return nil, err
			}
			cur = t
		} else if err := p.keyValue(cur); err != nil {
			return nil, err
		}
		if err := p.endOfLine(); err != nil {
			return nil, err
		}
	}
	closeTables(root)
	return root, nil
}

func newTable(start, line int) *Value {
	return &Value{Kind: Table, Start: start, End: start, Line: line, Fields: map[string]*Value{}}
}

// closeTables sets the End of the values a table header opens and returns it: a standard
// table, an array of tables and the implicit tables above them end with the last value
// below them, which the header cannot know. Inline tables and arrays close with their
// bracket and already have it.
func closeTables(v *Value) int {
	switch {
	case v.Kind == Table && !v.inline:
		for _, k := range v.Keys {
			v.End = max(v.End, closeTables(v.Fields[k]))
		}
	case v.Kind == Array && v.aot:
		for _, it := range v.Items {
			v.End = max(v.End, closeTables(it))
		}
	}
	return v.End
}

type parser struct {
	src  []byte
	pos  int
	line int
	// names holds each bare key once: lockfiles repeat the same few keys in every entry.
	names map[string]string
}

func (p *parser) eof() bool  { return p.pos >= len(p.src) }
func (p *parser) peek() byte { return p.src[p.pos] }

func (p *parser) errf(format string, args ...any) error {
	return &Error{Line: p.line, Msg: fmt.Sprintf(format, args...)}
}

// skipSpace skips spaces and tabs.
func (p *parser) skipSpace() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.pos++
	}
}

// skipComment skips a comment up to (not including) the newline.
func (p *parser) skipComment() {
	if !p.eof() && p.peek() == '#' {
		for !p.eof() && p.peek() != '\n' {
			p.pos++
		}
	}
}

// newline consumes a LF or CRLF and reports whether it did.
func (p *parser) newline() bool {
	switch {
	case p.eof():
		return false
	case p.peek() == '\n':
		p.pos++
	case p.peek() == '\r' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n':
		p.pos += 2
	default:
		return false
	}
	p.line++
	return true
}

// skipBlank skips whitespace, comments and newlines.
func (p *parser) skipBlank() {
	for {
		p.skipSpace()
		p.skipComment()
		if !p.newline() {
			return
		}
	}
}

// endOfLine requires optional whitespace, an optional comment and a newline or EOF.
func (p *parser) endOfLine() error {
	p.skipSpace()
	p.skipComment()
	if p.eof() || p.newline() {
		return nil
	}
	return p.errf("expected end of line, found %q", p.peek())
}

// header parses `[a.b]` or `[[a.b]]` and returns the table it opens.
func (p *parser) header(root *Value) (*Value, error) {
	start, line := p.pos, p.line
	p.pos++
	aot := !p.eof() && p.peek() == '['
	if aot {
		p.pos++
	}
	p.skipSpace()
	var buf [4]string
	keys, err := p.key(buf[:0])
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	closing := "]"
	if aot {
		closing = "]]"
	}
	if !strings.HasPrefix(string(p.src[p.pos:min(p.pos+len(closing), len(p.src))]), closing) {
		return nil, p.errf("unterminated table header")
	}
	p.pos += len(closing)
	t := root
	for _, k := range keys[:len(keys)-1] {
		if t, err = p.descend(t, k, start, line); err != nil {
			return nil, err
		}
	}
	last := keys[len(keys)-1]
	v := t.Fields[last]
	if aot {
		if v == nil {
			v = &Value{Kind: Array, Start: start, Line: line, aot: true}
			t.add(last, v)
		} else if v.Kind != Array || !v.aot {
			return nil, p.errf("%s is not an array of tables", strings.Join(keys, "."))
		}
		el := newTable(start, line)
		el.defined, el.End = true, p.pos // an element without values spans its header
		v.Items = append(v.Items, el)
		return el, nil
	}
	switch {
	case v == nil:
		v = newTable(start, line)
		t.add(last, v)
	case v.Kind != Table || v.inline:
		return nil, p.errf("%s is not a table", strings.Join(keys, "."))
	case v.defined:
		return nil, p.errf("table %s defined twice", strings.Join(keys, "."))
	}
	v.defined = true
	v.Start, v.End, v.Line = start, p.pos, line
	return v, nil
}

// descend returns the sub-table k of t for a header, creating an implicit one; an array of
// tables yields its last element.
func (p *parser) descend(t *Value, k string, start, line int) (*Value, error) {
	v := t.Fields[k]
	switch {
	case v == nil:
		v = newTable(start, line)
		t.add(k, v)
		return v, nil
	case v.Kind == Array && v.aot && len(v.Items) > 0:
		return v.Items[len(v.Items)-1], nil
	case v.Kind != Table || v.inline:
		return nil, p.errf("key %s is not a table", k)
	}
	return v, nil
}

func (t *Value) add(k string, v *Value) {
	t.Keys = append(t.Keys, k)
	t.Fields[k] = v
}

// keyValue parses `key = value` into t; dotted keys create implicit sub-tables.
func (p *parser) keyValue(t *Value) error {
	start, line := p.pos, p.line
	var buf [4]string
	keys, err := p.key(buf[:0])
	if err != nil {
		return err
	}
	p.skipSpace()
	if p.eof() || p.peek() != '=' {
		return p.errf("expected = after key %s", strings.Join(keys, "."))
	}
	p.pos++
	p.skipSpace()
	for _, k := range keys[:len(keys)-1] {
		v := t.Fields[k]
		switch {
		case v == nil:
			v = newTable(start, line)
			t.add(k, v)
		case v.Kind != Table || v.inline:
			return p.errf("key %s is not a table", k)
		}
		t = v
	}
	last := keys[len(keys)-1]
	if _, dup := t.Fields[last]; dup {
		return p.errf("duplicate key %s", strings.Join(keys, "."))
	}
	v, err := p.value()
	if err != nil {
		return err
	}
	t.add(last, v)
	// The table the key belongs to ends with the value; closeTables carries that up to the
	// tables above it, whose own End is only their header.
	t.End = p.pos
	return nil
}

// key parses a possibly dotted key, appending its parts to keys.
func (p *parser) key(keys []string) ([]string, error) {
	for {
		k, err := p.simpleKey()
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
		p.skipSpace()
		if p.eof() || p.peek() != '.' {
			return keys, nil
		}
		p.pos++
		p.skipSpace()
	}
}

func (p *parser) simpleKey() (string, error) {
	if p.eof() {
		return "", p.errf("expected key")
	}
	switch p.peek() {
	case '"', '\'':
		if p.hasPrefix(`"""`) || p.hasPrefix(`'''`) {
			return "", p.errf("multi-line string as key")
		}
		v, err := p.str()
		if err != nil {
			return "", err
		}
		return v.Str, nil
	}
	start := p.pos
	for !p.eof() && bareKeyChar(p.peek()) {
		p.pos++
	}
	if p.pos == start {
		return "", p.errf("invalid key character %q", p.peek())
	}
	raw := p.src[start:p.pos]
	if k, ok := p.names[string(raw)]; ok {
		return k, nil
	}
	k := string(raw)
	p.names[k] = k
	return k, nil
}

func bareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func (p *parser) hasPrefix(s string) bool {
	return len(p.src)-p.pos >= len(s) && string(p.src[p.pos:p.pos+len(s)]) == s
}

// value parses any value.
func (p *parser) value() (*Value, error) {
	if p.eof() {
		return nil, p.errf("expected value")
	}
	switch p.peek() {
	case '"', '\'':
		return p.str()
	case '[':
		return p.array()
	case '{':
		return p.inlineTable()
	}
	return p.scalar()
}

// scalar reads a non-string atom up to a delimiter; datetimes with a space are kept whole.
func (p *parser) scalar() (*Value, error) {
	start := p.pos
	for !p.eof() && !strings.ContainsRune(",]}#\r\n", rune(p.peek())) {
		p.pos++
	}
	end := p.pos
	for end > start && (p.src[end-1] == ' ' || p.src[end-1] == '\t') {
		end--
	}
	if end == start {
		return nil, p.errf("expected value")
	}
	raw := string(p.src[start:end])
	if strings.ContainsAny(raw, "\"'=[{") || strings.ContainsAny(raw, " \t") && !spacedDatetime(raw) {
		return nil, p.errf("invalid value %q", raw)
	}
	return &Value{Kind: Scalar, Str: raw, Start: start, End: end, Exact: true, Line: p.line}, nil
}

// array parses `[v, v, ...]` with newlines, comments and a trailing comma.
func (p *parser) array() (*Value, error) {
	a := &Value{Kind: Array, Start: p.pos, Line: p.line, inline: true}
	p.pos++
	for {
		p.skipBlank()
		if p.eof() {
			return nil, p.errf("unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			a.End = p.pos
			return a, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a.Items = append(a.Items, v)
		p.skipBlank()
		if p.eof() {
			return nil, p.errf("unterminated array")
		}
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
		default:
			return nil, p.errf("expected , or ] in array, found %q", p.peek())
		}
	}
}

// inlineTable parses `{k = v, ...}`.
func (p *parser) inlineTable() (*Value, error) {
	t := newTable(p.pos, p.line)
	t.inline = true
	p.pos++
	for first := true; ; first = false {
		p.skipBlank()
		if p.eof() {
			return nil, p.errf("unterminated inline table")
		}
		if p.peek() == '}' {
			p.pos++
			t.End = p.pos
			return t, nil
		}
		if !first {
			if p.peek() != ',' {
				return nil, p.errf("expected , or } in inline table, found %q", p.peek())
			}
			p.pos++
			p.skipBlank()
			if !p.eof() && p.peek() == '}' {
				continue // trailing comma (TOML 1.1)
			}
		}
		if err := p.keyValue(t); err != nil {
			return nil, err
		}
	}
}

// str parses one of the four string forms.
func (p *parser) str() (*Value, error) {
	line := p.line
	q := p.peek()
	multi := p.hasPrefix(string([]byte{q, q, q}))
	delim := 1
	if multi {
		delim = 3
	}
	p.pos += delim
	if multi {
		p.newline() // a newline right after the opening delimiter is trimmed
	}
	var b strings.Builder
	exact := true
	contentStart := p.pos
	for {
		if p.eof() {
			return nil, p.errf("unterminated string")
		}
		c := p.peek()
		if c == q && (!multi || p.closesMulti(q)) {
			end := p.pos
			if multi {
				// Up to two quotes directly before the closing delimiter belong to the string.
				for n := 0; n < 2 && p.pos+3 < len(p.src) && p.src[p.pos+3] == q; n++ {
					b.WriteByte(q)
					p.pos++
					end++
				}
			}
			p.pos += delim
			s := b.String()
			return &Value{Kind: String, Str: s, Start: contentStart, End: end, Exact: exact && s == string(p.src[contentStart:end]), Line: line}, nil
		}
		switch {
		case c == '\n' || c == '\r':
			if !multi {
				return nil, p.errf("newline in string")
			}
			nlStart := p.pos
			if !p.newline() {
				return nil, p.errf("bare carriage return in string")
			}
			b.Write(p.src[nlStart:p.pos])
		case c == '\\' && q == '"':
			exact = false
			if err := p.escape(&b, multi); err != nil {
				return nil, err
			}
		default:
			// Copy the run of plain bytes up to the next quote, newline or backslash at once.
			end := p.pos + 1
			for end < len(p.src) {
				if c := p.src[end]; c == q || c == '\n' || c == '\r' || c == '\\' {
					break
				}
				end++
			}
			b.Write(p.src[p.pos:end])
			p.pos = end
		}
	}
}

// closesMulti reports whether the quotes at pos close a multi-line string of quote q.
func (p *parser) closesMulti(q byte) bool {
	return p.hasPrefix(string([]byte{q, q, q}))
}

// escape decodes one backslash escape of a basic string.
func (p *parser) escape(b *strings.Builder, multi bool) error {
	p.pos++
	if p.eof() {
		return p.errf("unterminated string")
	}
	c := p.peek()
	p.pos++
	switch c {
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case 'e':
		b.WriteByte(0x1b)
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case 'x', 'u', 'U':
		n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		if p.pos+n > len(p.src) {
			return p.errf("short \\%c escape", c)
		}
		r, err := strconv.ParseUint(string(p.src[p.pos:p.pos+n]), 16, 32)
		if err != nil || !utf8.ValidRune(rune(r)) {
			return p.errf("invalid \\%c escape", c)
		}
		p.pos += n
		b.WriteRune(rune(r))
	case ' ', '\t', '\r', '\n':
		// Line-ending backslash: trims the newline and all following whitespace.
		if !multi {
			return p.errf("invalid escape")
		}
		p.pos--
		p.skipSpace()
		if !p.newline() {
			return p.errf("invalid escape")
		}
		for !p.eof() {
			p.skipSpace()
			if !p.newline() {
				break
			}
		}
	default:
		return p.errf("invalid escape \\%c", c)
	}
	return nil
}

// spacedDatetime reports whether raw is a date and a time separated by one space
// (`1979-05-27 07:32:00Z`), the only scalar that may contain whitespace.
func spacedDatetime(raw string) bool {
	return len(raw) > 11 && raw[4] == '-' && raw[7] == '-' && raw[10] == ' ' && raw[11] >= '0' && raw[11] <= '9' &&
		!strings.ContainsAny(raw[11:], " \t")
}
