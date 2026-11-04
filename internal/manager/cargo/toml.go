package cargo

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// item is one event of scanTOML: a table header (kind 'h', or 'H' for an array of tables) or a
// key/value pair whose key is the full dotted path (header path plus key). The members of an
// inline table are reported as pairs of their own; the table itself is not.
type item struct {
	kind byte // 'h' table header, 'H' array-of-tables header, 's' string, 'a' array, 'v' other scalar
	key  []string
	str  string   // 's': the decoded string
	span dep.Span // 's': the raw string content; zero when it has escapes or spans lines
	strs []string // 'a': the string elements
	off  int      // start of the value (pairs) or header
}

// scanTOML walks the TOML document src and calls f for every header and key/value pair, in
// document order. It keeps byte spans of single-line strings, so values can be replaced in
// place. It accepts newlines inside inline tables (TOML 1.1) and does not check for duplicate
// keys; any other syntax error is returned with its line.
func scanTOML(src []byte, f func(item)) error {
	s := tomlScanner{src: src, f: f}
	if err := s.document(); err != nil {
		return fmt.Errorf("line %d: %w", lineOf(src, s.pos), err)
	}
	return nil
}

// lineOf returns the 1-based line of offset off in src.
func lineOf(src []byte, off int) int {
	return bytes.Count(src[:min(off, len(src))], []byte{'\n'}) + 1
}

type tomlScanner struct {
	src []byte
	pos int
	f   func(item)
}

func (s *tomlScanner) peek() byte {
	if s.pos < len(s.src) {
		return s.src[s.pos]
	}
	return 0
}

func (s *tomlScanner) hasPrefix(p string) bool {
	return len(s.src)-s.pos >= len(p) && string(s.src[s.pos:s.pos+len(p)]) == p
}

// blank skips spaces and tabs.
func (s *tomlScanner) blank() {
	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
		s.pos++
	}
}

// space skips spaces, tabs, newlines and comments.
func (s *tomlScanner) space() {
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		case '#':
			s.comment()
		default:
			return
		}
	}
}

func (s *tomlScanner) comment() {
	for s.pos < len(s.src) && s.src[s.pos] != '\n' {
		s.pos++
	}
}

// eol consumes trailing blanks, a comment and the newline after a header or pair.
func (s *tomlScanner) eol() error {
	s.blank()
	if s.peek() == '#' {
		s.comment()
	}
	switch {
	case s.pos == len(s.src):
		return nil
	case s.src[s.pos] == '\n':
		s.pos++
		return nil
	case s.hasPrefix("\r\n"):
		s.pos += 2
		return nil
	}
	return fmt.Errorf("unexpected %q", s.src[s.pos])
}

func (s *tomlScanner) document() error {
	var table []string
	for {
		s.space()
		if s.pos == len(s.src) {
			return nil
		}
		if s.peek() == '[' {
			kind, end := byte('h'), "]"
			start := s.pos
			s.pos++
			if s.peek() == '[' {
				kind, end = 'H', "]]"
				s.pos++
			}
			s.blank()
			key, err := s.key()
			if err != nil {
				return err
			}
			s.blank()
			if !s.hasPrefix(end) {
				return errors.New("unterminated table header")
			}
			s.pos += len(end)
			table = key
			s.f(item{kind: kind, key: key, off: start})
			if err := s.eol(); err != nil {
				return err
			}
			continue
		}
		if err := s.pair(table, true); err != nil {
			return err
		}
		if err := s.eol(); err != nil {
			return err
		}
	}
}

// pair reads `key = value` below prefix and reports it when emit is set.
func (s *tomlScanner) pair(prefix []string, emit bool) error {
	key, err := s.key()
	if err != nil {
		return err
	}
	s.blank()
	if s.peek() != '=' {
		return errors.New("expected '=' after key")
	}
	s.pos++
	s.blank()
	full := append(append(make([]string, 0, len(prefix)+len(key)), prefix...), key...)
	it, err := s.value(full, emit)
	if err == nil && emit && it.kind != 't' {
		s.f(it)
	}
	return err
}

// key reads a dotted key of bare and quoted parts.
func (s *tomlScanner) key() ([]string, error) {
	var parts []string
	for {
		var part string
		switch s.peek() {
		case '"', '\'':
			if s.hasPrefix(`"""`) || s.hasPrefix("'''") {
				return nil, errors.New("multi-line key")
			}
			str, _, err := s.str()
			if err != nil {
				return nil, err
			}
			part = str
		default:
			start := s.pos
			for s.pos < len(s.src) && bareKey(s.src[s.pos]) {
				s.pos++
			}
			if s.pos == start {
				return nil, errors.New("expected a key")
			}
			part = string(s.src[start:s.pos])
		}
		parts = append(parts, part)
		s.blank()
		if s.peek() != '.' {
			return parts, nil
		}
		s.pos++
		s.blank()
	}
}

func bareKey(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-'
}

// value reads the value of key. An inline table (kind 't') reports its members itself when
// emit is set.
func (s *tomlScanner) value(key []string, emit bool) (item, error) {
	it := item{key: key, off: s.pos}
	var err error
	switch c := s.peek(); {
	case s.hasPrefix(`"""`) || s.hasPrefix("'''"):
		it.kind = 's'
		it.str, err = s.multiline()
	case c == '"' || c == '\'':
		it.kind = 's'
		it.str, it.span, err = s.str()
	case c == '[':
		it.kind = 'a'
		it.strs, err = s.array()
	case c == '{':
		it.kind = 't'
		err = s.inlineTable(key, emit)
	default:
		for s.pos < len(s.src) && !strings.ContainsRune(" \t\r\n#,]}", rune(s.src[s.pos])) {
			s.pos++
		}
		if s.pos == it.off {
			return it, errors.New("expected a value")
		}
		it.kind, it.str = 'v', string(s.src[it.off:s.pos])
	}
	return it, err
}

// str reads a single-line basic or literal string. span is the raw content when it equals the
// decoded string (no escapes).
func (s *tomlScanner) str() (string, dep.Span, error) {
	q := s.src[s.pos]
	s.pos++
	start := s.pos
	var b strings.Builder
	escaped := false
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == q:
			end := s.pos
			s.pos++
			if escaped {
				return b.String(), dep.Span{}, nil
			}
			return string(s.src[start:end]), dep.Span{Start: start, End: end}, nil
		case c == '\n':
			return "", dep.Span{}, errors.New("unterminated string")
		case c == '\\' && q == '"':
			if !escaped {
				b.Write(s.src[start:s.pos])
				escaped = true
			}
			r, n, err := unescape(s.src[s.pos:])
			if err != nil {
				return "", dep.Span{}, err
			}
			b.WriteRune(r)
			s.pos += n
		default:
			if escaped {
				b.WriteByte(c)
			}
			s.pos++
		}
	}
	return "", dep.Span{}, errors.New("unterminated string")
}

// unescape decodes the escape sequence at the start of b and returns its length.
func unescape(b []byte) (rune, int, error) {
	if len(b) < 2 {
		return 0, 0, errors.New("unterminated escape")
	}
	switch b[1] {
	case 'b':
		return '\b', 2, nil
	case 't':
		return '\t', 2, nil
	case 'n':
		return '\n', 2, nil
	case 'f':
		return '\f', 2, nil
	case 'r':
		return '\r', 2, nil
	case 'e':
		return 0x1b, 2, nil
	case '"':
		return '"', 2, nil
	case '\\':
		return '\\', 2, nil
	case 'u', 'U':
		n := 4
		if b[1] == 'U' {
			n = 8
		}
		if len(b) < 2+n {
			return 0, 0, errors.New("short unicode escape")
		}
		v, err := strconv.ParseUint(string(b[2:2+n]), 16, 32)
		if err != nil || !utf8.ValidRune(rune(v)) {
			return 0, 0, errors.New("invalid unicode escape")
		}
		return rune(v), 2 + n, nil
	}
	return 0, 0, fmt.Errorf("invalid escape \\%c", b[1])
}

// multiline skips a multi-line basic or literal string and returns its raw content, undecoded:
// deps never edits multi-line values, and Cargo never needs them decoded.
func (s *tomlScanner) multiline() (string, error) {
	delim := string(s.src[s.pos : s.pos+3])
	s.pos += 3
	start := s.pos
	for s.pos < len(s.src) {
		if s.src[s.pos] == '\\' && delim == `"""` {
			s.pos += 2
			continue
		}
		if s.hasPrefix(delim) {
			// Up to two quotes may directly precede the closing delimiter.
			for s.pos+3 < len(s.src) && s.src[s.pos+3] == delim[0] {
				s.pos++
			}
			str := string(s.src[start:s.pos])
			s.pos += 3
			return str, nil
		}
		s.pos++
	}
	return "", errors.New("unterminated multi-line string")
}

// array reads an array and returns its string elements; other elements are skipped.
func (s *tomlScanner) array() ([]string, error) {
	s.pos++ // [
	var strs []string
	for {
		s.space()
		if s.peek() == ']' {
			s.pos++
			return strs, nil
		}
		it, err := s.value(nil, false)
		if err != nil {
			return nil, err
		}
		if it.kind == 's' {
			strs = append(strs, it.str)
		}
		s.space()
		switch s.peek() {
		case ',':
			s.pos++
		case ']':
		default:
			return nil, errors.New("expected ',' or ']' in array")
		}
	}
}

// inlineTable reads `{ k = v, ... }` below key, reporting its members when emit is set.
func (s *tomlScanner) inlineTable(key []string, emit bool) error {
	s.pos++ // {
	for {
		s.space()
		if s.peek() == '}' {
			s.pos++
			return nil
		}
		if err := s.pair(key, emit); err != nil {
			return err
		}
		s.space()
		switch s.peek() {
		case ',':
			s.pos++
		case '}':
		default:
			return errors.New("expected ',' or '}' in inline table")
		}
	}
}
