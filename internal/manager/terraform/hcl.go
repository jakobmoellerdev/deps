package terraform

import (
	"bytes"
	"fmt"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// body is the structure of an HCL body as far as deps reads it: attributes with their value
// when it is a string literal or an object constructor, and blocks. Every other expression is
// skipped token by token (strings, templates, heredocs, comments and brackets included), so
// any valid HCL file parses.
type body struct {
	attrs  []attr
	blocks []block
}

type attr struct {
	name string
	off  int // offset of the name
	val  value
}

// value is an attribute or object item value.
type value struct {
	kind valueKind
	str  string   // kindString: the literal (escapes decoded only when span is zero)
	span dep.Span // kindString: the bytes between the quotes; zero when they differ from str
	obj  []attr   // kindObject: the items
	expr dep.Span // the whole expression (HCL values; zero for JSON), trailing blanks and comments included
}

type valueKind int

const (
	kindOther  valueKind = iota // any other expression
	kindString                  // a quoted string without interpolation
	kindObject                  // { key = value, ... }
)

type block struct {
	typ    string
	labels []string
	span   dep.Span // from the type to after the closing brace
	body   body
}

// parseHCL parses src as an HCL body.
func parseHCL(src []byte) (body, error) {
	p := hclParser{src: src}
	b, err := p.body(0)
	if err != nil {
		return body{}, err
	}
	return b, nil
}

type hclParser struct {
	src []byte
	pos int
}

// lineAt returns the 1-based line of offset off in src.
func lineAt(src []byte, off int) int { return bytes.Count(src[:off], []byte{'\n'}) + 1 }

func (p *hclParser) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", lineAt(p.src, min(p.pos, len(p.src))), fmt.Sprintf(format, args...))
}

func (p *hclParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

// space skips blanks and comments; newlines too when nl is set. A line comment ends before its
// newline.
func (p *hclParser) space(nl bool) error {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || (nl && c == '\n'):
			p.pos++
		case c == '#' || (c == '/' && p.at(1) == '/'):
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.at(1) == '*':
			end := bytes.Index(p.src[p.pos+2:], []byte("*/"))
			if end < 0 {
				return p.errorf("unterminated comment")
			}
			p.pos += 2 + end + 2
		default:
			return nil
		}
	}
	return nil
}

// at returns the byte i positions after pos, or 0.
func (p *hclParser) at(i int) byte {
	if p.pos+i < len(p.src) {
		return p.src[p.pos+i]
	}
	return 0
}

func isIdentStart(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

func isIdent(c byte) bool { return isIdentStart(c) || c == '-' || '0' <= c && c <= '9' }

func (p *hclParser) ident() string {
	start := p.pos
	if !isIdentStart(p.peek()) {
		return ""
	}
	for p.pos < len(p.src) && isIdent(p.src[p.pos]) {
		p.pos++
	}
	return string(p.src[start:p.pos])
}

// body parses attributes and blocks up to end ('}' or 0 for the end of input), which is not
// consumed.
func (p *hclParser) body(end byte) (body, error) {
	var b body
	for {
		if err := p.space(true); err != nil {
			return body{}, err
		}
		if p.pos >= len(p.src) {
			if end != 0 {
				return body{}, p.errorf("unterminated block")
			}
			return b, nil
		}
		if p.peek() == end {
			return b, nil
		}
		start := p.pos
		name := p.ident()
		if name == "" {
			return body{}, p.errorf("unexpected %q", p.peek())
		}
		if err := p.space(false); err != nil {
			return body{}, err
		}
		if p.peek() == '=' && p.at(1) != '=' {
			p.pos++
			v, err := p.value()
			if err != nil {
				return body{}, err
			}
			b.attrs = append(b.attrs, attr{name: name, off: start, val: v})
			continue
		}
		blk, err := p.block(name, start)
		if err != nil {
			return body{}, err
		}
		b.blocks = append(b.blocks, blk)
	}
}

// block parses the labels and body of a block whose type was read at start.
func (p *hclParser) block(typ string, start int) (block, error) {
	blk := block{typ: typ}
	for {
		if err := p.space(false); err != nil {
			return block{}, err
		}
		switch c := p.peek(); {
		case c == '"':
			s, _, _, err := p.quoted()
			if err != nil {
				return block{}, err
			}
			blk.labels = append(blk.labels, s)
		case isIdentStart(c):
			blk.labels = append(blk.labels, p.ident())
		case c == '{':
			p.pos++
			b, err := p.body('}')
			if err != nil {
				return block{}, err
			}
			p.pos++
			blk.body, blk.span = b, dep.Span{Start: start, End: p.pos}
			return blk, nil
		default:
			return block{}, p.errorf("unexpected %q in block header", c)
		}
	}
}

// value parses an attribute value: a string literal or an object constructor directly
// followed by the end of the expression, else any expression (kindOther).
func (p *hclParser) value() (value, error) {
	if err := p.space(false); err != nil {
		return value{}, err
	}
	start := p.pos
	var v value
	switch p.peek() {
	case '"':
		s, span, literal, err := p.quoted()
		if err != nil {
			return value{}, err
		}
		if literal {
			v = value{kind: kindString, str: s, span: span}
		}
	case '{':
		items, ok, err := p.object()
		if err != nil {
			return value{}, err
		}
		if ok {
			v = value{kind: kindObject, obj: items}
		} else {
			p.pos = start
		}
	}
	if v.kind != kindOther {
		if err := p.space(false); err != nil {
			return value{}, err
		}
		if p.exprEnd() {
			v.expr = dep.Span{Start: start, End: p.pos}
			return v, nil
		}
		v = value{}
	}
	err := p.skipExpr()
	v.expr = dep.Span{Start: start, End: p.pos}
	return v, err
}

// exprEnd reports whether pos ends an expression: a newline, a comma, a closing bracket or the
// end of input.
func (p *hclParser) exprEnd() bool {
	switch p.peek() {
	case 0, '\n', ',', '}', ']', ')':
		return true
	}
	return false
}

// object parses `{ key = value, ... }` with identifier or quoted keys; ok is false (pos
// undefined) for any other brace expression, such as a for expression.
func (p *hclParser) object() (items []attr, ok bool, err error) {
	p.pos++ // {
	for {
		if err := p.space(true); err != nil {
			return nil, false, err
		}
		switch p.peek() {
		case '}':
			p.pos++
			return items, true, nil
		case ',':
			p.pos++
			continue
		}
		start := p.pos
		var key string
		switch c := p.peek(); {
		case c == '"':
			s, _, literal, err := p.quoted()
			if err != nil || !literal {
				return nil, false, err
			}
			key = s
		case isIdentStart(c):
			key = p.ident()
		default:
			return nil, false, nil
		}
		if err := p.space(false); err != nil {
			return nil, false, err
		}
		if c := p.peek(); (c != '=' || p.at(1) == '=') && c != ':' {
			return nil, false, nil
		}
		p.pos++
		v, err := p.value()
		if err != nil {
			return nil, false, err
		}
		items = append(items, attr{name: key, off: start, val: v})
	}
}

// skipExpr skips an expression up to its end (see exprEnd) outside brackets.
func (p *hclParser) skipExpr() error {
	depth := 0
	for p.pos < len(p.src) {
		if err := p.space(depth > 0); err != nil {
			return err
		}
		if p.pos >= len(p.src) {
			break
		}
		switch c := p.src[p.pos]; {
		case depth == 0 && p.exprEnd():
			return nil
		case c == '(' || c == '[' || c == '{':
			depth++
			p.pos++
		case c == ')' || c == ']' || c == '}':
			depth--
			p.pos++
		case c == '"':
			if _, _, _, err := p.quoted(); err != nil {
				return err
			}
		case c == '<' && p.at(1) == '<':
			if err := p.heredoc(); err != nil {
				return err
			}
		default:
			p.pos++
		}
	}
	if depth > 0 {
		return p.errorf("unterminated expression")
	}
	return nil
}

// quoted reads a quoted template at pos. literal reports a string without interpolation or
// directive; span covers the bytes between the quotes, and is zero when they contain escapes.
func (p *hclParser) quoted() (s string, span dep.Span, literal bool, err error) {
	open := p.pos
	p.pos++
	literal, escaped := true, false
	var buf []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			if !escaped {
				return string(p.src[open+1 : p.pos-1]), dep.Span{Start: open + 1, End: p.pos - 1}, literal, nil
			}
			return string(buf), dep.Span{}, literal, nil
		case c == '\n':
			return "", dep.Span{}, false, p.errorf("unterminated string")
		case c == '\\':
			if !escaped {
				escaped, buf = true, append(buf, p.src[open+1:p.pos]...)
			}
			if r, n := unescape(p.src[p.pos:]); n > 0 {
				buf = append(buf, r...)
				p.pos += n
				continue
			}
			return "", dep.Span{}, false, p.errorf("invalid escape")
		case (c == '$' || c == '%') && p.at(1) == c && p.at(2) == '{':
			// $${ and %%{ are literal ${ and %{.
			if !escaped {
				escaped, buf = true, append(buf, p.src[open+1:p.pos]...)
			}
			buf = append(buf, c, '{')
			p.pos += 3
		case (c == '$' || c == '%') && p.at(1) == '{':
			literal = false
			p.pos += 2
			if err := p.template(); err != nil {
				return "", dep.Span{}, false, err
			}
		default:
			if escaped {
				buf = append(buf, c)
			}
			p.pos++
		}
	}
	return "", dep.Span{}, false, p.errorf("unterminated string")
}

// unescape decodes the escape sequence at the start of b, returning its bytes and length.
func unescape(b []byte) ([]byte, int) {
	if len(b) < 2 {
		return nil, 0
	}
	switch b[1] {
	case 'n':
		return []byte{'\n'}, 2
	case 'r':
		return []byte{'\r'}, 2
	case 't':
		return []byte{'\t'}, 2
	case '"', '\\':
		return b[1:2], 2
	case 'u', 'U':
		n := 4
		if b[1] == 'U' {
			n = 8
		}
		if len(b) < 2+n {
			return nil, 0
		}
		var r rune
		for _, h := range b[2 : 2+n] {
			d, ok := hexDigit(h)
			if !ok {
				return nil, 0
			}
			r = r<<4 | rune(d)
		}
		return []byte(string(r)), 2 + n
	}
	return nil, 0
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// template skips an interpolation or directive body after its `${`/`%{`, through the closing
// brace.
func (p *hclParser) template() error {
	depth := 1
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; c {
		case '{':
			depth++
			p.pos++
		case '}':
			depth--
			p.pos++
			if depth == 0 {
				return nil
			}
		case '"':
			if _, _, _, err := p.quoted(); err != nil {
				return err
			}
		default:
			p.pos++
		}
	}
	return p.errorf("unterminated template")
}

// heredoc skips `<<MARKER` or `<<-MARKER` and its lines through the closing marker line.
func (p *hclParser) heredoc() error {
	p.pos += 2
	if p.peek() == '-' {
		p.pos++
	}
	marker := p.ident()
	if marker == "" {
		return p.errorf("invalid heredoc")
	}
	nl := bytes.IndexByte(p.src[p.pos:], '\n')
	if nl < 0 {
		return p.errorf("unterminated heredoc")
	}
	p.pos += nl + 1
	for p.pos < len(p.src) {
		end := bytes.IndexByte(p.src[p.pos:], '\n')
		line := p.src[p.pos:]
		if end >= 0 {
			line = line[:end]
		}
		if string(bytes.TrimSpace(line)) == marker {
			p.pos += len(line) // the newline ends the expression
			return nil
		}
		if end < 0 {
			break
		}
		p.pos += end + 1
	}
	return p.errorf("unterminated heredoc")
}
