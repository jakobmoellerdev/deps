// Package renovate translates Renovate configuration into deps configuration.
package renovate

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxJSON5Depth = 1000

// parseJSON5 parses a JSON5 document (which includes JSON and JSONC) into
// map[string]any, []any, string, float64, bool and nil values.
func parseJSON5(src []byte) (any, error) {
	p := &json5Parser{src: src}
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	if p.pos < len(p.src) {
		return nil, p.unexpected()
	}
	return v, nil
}

type json5Parser struct {
	src []byte
	pos int
}

// errAt formats msg with the 1-based line and rune column of offset off.
func (p *json5Parser) errAt(off int, msg string) error {
	line, col := 1, 1
	for i := 0; i < off && i < len(p.src); {
		r, n := utf8.DecodeRune(p.src[i:])
		switch r {
		case '\n', '\u2028', '\u2029':
			line, col = line+1, 1
		case '\r':
			if i+1 >= len(p.src) || p.src[i+1] != '\n' {
				line, col = line+1, 1
			}
		default:
			col++
		}
		i += n
	}
	return fmt.Errorf("%d:%d: %s", line, col, msg)
}

func (p *json5Parser) peek() (rune, int) {
	if p.pos >= len(p.src) {
		return -1, 0
	}
	return utf8.DecodeRune(p.src[p.pos:])
}

func (p *json5Parser) unexpected() error {
	r, _ := p.peek()
	if r < 0 {
		return p.errAt(p.pos, "unexpected end of input")
	}
	return p.errAt(p.pos, fmt.Sprintf("unexpected character %q", r))
}

func isJSON5Space(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', '\u00a0', '\u2028', '\u2029', '\ufeff':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

func (p *json5Parser) skipSpace() error {
	for p.pos < len(p.src) {
		r, n := p.peek()
		switch {
		case isJSON5Space(r):
			p.pos += n
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			for p.pos < len(p.src) {
				r, n := p.peek()
				if r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' {
					break
				}
				p.pos += n
			}
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			start := p.pos
			end := bytes.Index(p.src[p.pos+2:], []byte("*/"))
			if end < 0 {
				return p.errAt(start, "unterminated block comment")
			}
			p.pos += 2 + end + 2
		default:
			return nil
		}
	}
	return nil
}

func (p *json5Parser) value(depth int) (any, error) {
	if depth > maxJSON5Depth {
		return nil, p.errAt(p.pos, "nesting too deep")
	}
	r, _ := p.peek()
	switch {
	case r == '{':
		return p.object(depth)
	case r == '[':
		return p.array(depth)
	case r == '"' || r == '\'':
		return p.str()
	case r == '+' || r == '-' || r == '.' || r == 'I' || r == 'N' || (r >= '0' && r <= '9'):
		return p.number()
	case r == 't' || r == 'f' || r == 'n':
		start := p.pos
		id, err := p.ident()
		if err != nil {
			p.pos = start
			return nil, p.unexpected()
		}
		switch id {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		p.pos = start
	}
	return nil, p.unexpected()
}

func (p *json5Parser) object(depth int) (any, error) {
	p.pos++ // {
	m := map[string]any{}
	for {
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		r, _ := p.peek()
		if r == '}' {
			p.pos++
			return m, nil
		}
		var key string
		switch {
		case r == '"' || r == '\'':
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			key = k
		case isIdentStart(r) || r == '\\':
			k, err := p.ident()
			if err != nil {
				return nil, err
			}
			key = k
		default:
			return nil, p.unexpected()
		}
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		if r, _ := p.peek(); r != ':' {
			return nil, p.unexpected()
		}
		p.pos++
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		m[key] = v
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		switch r, _ := p.peek(); r {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return m, nil
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *json5Parser) array(depth int) (any, error) {
	p.pos++ // [
	a := []any{}
	for {
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		if r, _ := p.peek(); r == ']' {
			p.pos++
			return a, nil
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		switch r, _ := p.peek(); r {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return a, nil
		default:
			return nil, p.unexpected()
		}
	}
}

// isIdentStart and isIdentPart approximate the ID_Start and ID_Continue properties JSON5 uses
// for unquoted keys; the connector and combining characters they also allow are rare enough in
// Renovate configuration to leave out.
func isIdentStart(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r)
}

// ident reads an unquoted key, which may spell any of its characters as \uXXXX.
func (p *json5Parser) ident() (string, error) {
	var b strings.Builder
	for first := true; p.pos < len(p.src); first = false {
		at := p.pos
		r, n := p.peek()
		if r == '\\' {
			p.pos++
			if c, _ := p.peek(); c != 'u' {
				return "", p.errAt(at, "invalid escape in key")
			}
			p.pos++
			v, ok := p.hex(4)
			if !ok {
				return "", p.errAt(at, "invalid \\u escape")
			}
			if utf16IsHigh(v) && p.pos+1 < len(p.src) && p.src[p.pos] == '\\' && p.src[p.pos+1] == 'u' {
				save := p.pos
				p.pos += 2
				if lo, ok := p.hex(4); ok && lo >= 0xdc00 && lo <= 0xdfff {
					v = (v-0xd800)<<10 + (lo - 0xdc00) + 0x10000
				} else {
					p.pos = save
				}
			}
			r = v
		} else {
			if !isIdentPart(r) {
				break
			}
			p.pos += n
		}
		if first && !isIdentStart(r) || !first && !isIdentPart(r) {
			return "", p.errAt(at, fmt.Sprintf("invalid character %q in key", r))
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "", p.unexpected()
	}
	return b.String(), nil
}

func (p *json5Parser) number() (any, error) {
	start := p.pos
	sign := 1.0
	if c := p.src[p.pos]; c == '+' || c == '-' {
		if c == '-' {
			sign = -1
		}
		p.pos++
	}
	rest := p.src[p.pos:]
	switch {
	case len(rest) > 0 && (rest[0] == 'I' || rest[0] == 'N'):
		id, err := p.ident()
		if err != nil {
			p.pos = start
			return nil, p.unexpected()
		}
		switch id {
		case "Infinity":
			return math.Inf(int(sign)), nil
		case "NaN":
			return math.NaN(), nil
		}
		p.pos = start
		return nil, p.unexpected()
	case len(rest) > 1 && rest[0] == '0' && (rest[1] == 'x' || rest[1] == 'X'):
		p.pos += 2
		ds := p.pos
		for p.pos < len(p.src) && isHex(p.src[p.pos]) {
			p.pos++
		}
		if ds == p.pos {
			return nil, p.unexpected()
		}
		u, err := strconv.ParseUint(string(p.src[ds:p.pos]), 16, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, p.errAt(start, "invalid number")
		}
		f := float64(u)
		if err != nil {
			f = math.Inf(1)
		}
		return sign * f, p.checkNumberEnd()
	}
	digits := func() int {
		s := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		return p.pos - s
	}
	n := digits()
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		p.pos++
		n += digits()
	}
	if n == 0 {
		return nil, p.unexpected()
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			return nil, p.unexpected()
		}
	}
	text := strings.TrimPrefix(string(p.src[start:p.pos]), "+")
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, p.errAt(start, "invalid number")
	}
	return f, p.checkNumberEnd()
}

// checkNumberEnd rejects identifier characters glued to a number (e.g. 1x).
func (p *json5Parser) checkNumberEnd() error {
	if r, _ := p.peek(); isIdentStart(r) || (r >= '0' && r <= '9') {
		return p.unexpected()
	}
	return nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (p *json5Parser) hex(n int) (rune, bool) {
	if p.pos+n > len(p.src) {
		return 0, false
	}
	s := string(p.src[p.pos : p.pos+n])
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || strings.ContainsAny(s, "+-") {
		return 0, false
	}
	p.pos += n
	return rune(v), true
}

func (p *json5Parser) str() (string, error) {
	start := p.pos
	quote := p.src[p.pos]
	p.pos++
	var b strings.Builder
	for {
		r, n := p.peek()
		switch r {
		case -1:
			return "", p.errAt(start, "unterminated string")
		case rune(quote):
			p.pos++
			return b.String(), nil
		case '\n', '\r':
			return "", p.errAt(p.pos, "unterminated string")
		case '\\':
			p.pos++
			if err := p.escape(&b); err != nil {
				return "", err
			}
			continue
		}
		b.WriteRune(r)
		p.pos += n
	}
}

func (p *json5Parser) escape(b *strings.Builder) error {
	at := p.pos - 1
	r, n := p.peek()
	if r < 0 {
		return p.errAt(at, "unterminated string")
	}
	p.pos += n
	switch r {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'v':
		b.WriteByte('\v')
	case '0':
		if p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			return p.errAt(at, "invalid escape \\0 followed by a digit")
		}
		b.WriteByte(0)
	case '\r':
		if p.pos < len(p.src) && p.src[p.pos] == '\n' {
			p.pos++
		}
	case '\n', '\u2028', '\u2029':
	case 'x':
		v, ok := p.hex(2)
		if !ok {
			return p.errAt(at, "invalid \\x escape")
		}
		b.WriteRune(v)
	case 'u':
		v, ok := p.hex(4)
		if !ok {
			return p.errAt(at, "invalid \\u escape")
		}
		if utf16IsHigh(v) && p.pos+1 < len(p.src) && p.src[p.pos] == '\\' && p.src[p.pos+1] == 'u' {
			save := p.pos
			p.pos += 2
			if lo, ok := p.hex(4); ok && lo >= 0xdc00 && lo <= 0xdfff {
				v = (v-0xd800)<<10 + (lo - 0xdc00) + 0x10000
			} else {
				p.pos = save
			}
		}
		b.WriteRune(v) // lone surrogates become U+FFFD
	default:
		if r >= '1' && r <= '9' {
			return p.errAt(at, fmt.Sprintf("invalid escape \\%c", r))
		}
		b.WriteRune(r)
	}
	return nil
}

func utf16IsHigh(r rune) bool { return r >= 0xd800 && r <= 0xdbff }
