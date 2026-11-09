// Package jsonscan is a streaming JSON scanner for decoders that keep a few members of large
// documents, such as registry metadata. It accepts exactly what encoding/json/jsontext accepts
// by default (UTF-8 strings without control characters, valid escapes and surrogate pairs, at
// most MaxDepth nested containers; duplicate names are allowed) and skips the values the
// caller does not want several times faster than jsontext.Decoder.ReadValue. It buffers one
// token at a time, or everything from a mark on.
//
// A document is read with Open, Next and the value readers: Open enters an object or array,
// Next moves to its next member (an object member's name is then available from Name and
// NameIs) and reports its end, and Str, StrBytes, Bool, Null or Skip consume a value. Several
// top-level values may follow each other.
package jsonscan

import (
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math/bits"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth is the deepest nesting of objects and arrays accepted, jsontext's limit.
const MaxDepth = 10000

// bufferSize is the initial buffer; it grows while one token or the marked span does not fit.
const bufferSize = 16 << 10

// SyntaxError reports invalid JSON. A document that ends early is one, wrapping
// io.ErrUnexpectedEOF.
type SyntaxError struct {
	Offset int64  // of the byte that made the input invalid
	Msg    string // what was wrong
	err    error
}

func (e *SyntaxError) Error() string {
	return "jsonscan: " + e.Msg + " at offset " + strconv.FormatInt(e.Offset, 10)
}

func (e *SyntaxError) Unwrap() error { return e.err }

// Scanner reads JSON from an io.Reader. Read errors other than io.EOF are returned as they are.
type Scanner struct {
	r    io.Reader
	buf  []byte
	i    int   // next unread byte of buf
	mark int   // first byte refills keep; -1 without a mark
	tok  int   // start of the string skipString reads, kept by refills too; -1 outside
	base int64 // stream offset of buf[0]

	open    []byte // the containers entered with Open: '{' or '[', | hasMember once Next moved past one
	skip    []byte // the containers Skip is in
	name    []byte // the quoted name of the current object member
	nameEsc bool   // whether name has escapes
	str     []byte // unescaped string of StrBytes
}

// hasMember marks an open container whose members are separated by commas from here on.
const hasMember = 0x80

// New returns a Scanner reading r.
func New(r io.Reader) *Scanner {
	return &Scanner{r: r, buf: make([]byte, 0, bufferSize), mark: -1, tok: -1}
}

// Offset is the stream offset of the next unread byte.
func (s *Scanner) Offset() int64 { return s.base + int64(s.i) }

// Mark keeps every byte from the next unread one on buffered, until Unmark or the next Mark.
func (s *Scanner) Mark() { s.mark = s.i }

// Unmark drops the mark.
func (s *Scanner) Unmark() { s.mark = -1 }

// Marked returns the bytes read since Mark: after Mark and Skip, the skipped value (with the
// whitespace before it). It is valid until the next read.
func (s *Scanner) Marked() []byte { return s.buf[s.mark:s.i] }

// Buffered returns the bytes read from the reader but not scanned yet; valid until the next
// read.
func (s *Scanner) Buffered() []byte { return s.buf[s.i:] }

// fill reads more input, keeping the marked bytes, the string being read and the unread ones;
// buffer indexes shift (s.i, s.tok and s.mark are adjusted). Like jsontext.Decoder it retries reads that return
// neither data nor an error, and ignores the error of a read that returned data.
func (s *Scanner) fill() error {
	keep := s.i
	if s.tok >= 0 && s.tok < keep {
		keep = s.tok
	}
	if s.mark >= 0 && s.mark < keep {
		keep = s.mark
	}
	if keep > 0 {
		n := copy(s.buf, s.buf[keep:])
		s.base += int64(keep)
		s.i -= keep
		if s.tok >= 0 {
			s.tok -= keep
		}
		if s.mark >= 0 {
			s.mark -= keep
		}
		s.buf = s.buf[:n]
	}
	if cap(s.buf)-len(s.buf) < cap(s.buf)/4 {
		s.buf = slices.Grow(s.buf, cap(s.buf))
	}
	for {
		n, err := s.r.Read(s.buf[len(s.buf):cap(s.buf)])
		if n > 0 {
			s.buf = s.buf[:len(s.buf)+n]
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// more is fill inside a value, where the end of input is unexpected.
func (s *Scanner) more() error {
	if err := s.fill(); err != nil {
		return s.eof(err)
	}
	return nil
}

// eof turns io.EOF into the SyntaxError of a truncated document.
func (s *Scanner) eof(err error) error {
	if err == io.EOF { //nolint:errorlint // Read returns io.EOF itself
		return &SyntaxError{Offset: s.Offset(), Msg: "unexpected EOF", err: io.ErrUnexpectedEOF}
	}
	return err
}

// syntax returns the SyntaxError of the byte at buf[i].
func (s *Scanner) syntax(i int, msg string) error {
	return &SyntaxError{Offset: s.base + int64(i), Msg: "invalid character " + strconv.QuoteRune(rune(s.buf[i])) + " " + msg}
}

// need buffers at least n bytes from s.i.
func (s *Scanner) need(n int) error {
	for len(s.buf)-s.i < n {
		if err := s.more(); err != nil {
			return err
		}
	}
	return nil
}

// Peek skips whitespace and returns the first byte of the next token: one of { } [ ] , : "
// t f n, or of a number. At the top level it returns io.EOF when the input ends.
func (s *Scanner) Peek() (byte, error) {
	for {
		for s.i < len(s.buf) {
			switch c := s.buf[s.i]; c {
			case ' ', '\t', '\n', '\r':
				s.i++
			default:
				return c, nil
			}
		}
		if err := s.fill(); err != nil {
			if len(s.open) > 0 {
				err = s.eof(err)
			}
			return 0, err
		}
	}
}

// Open enters the object or array (kind '{' or '[') that is the next value.
func (s *Scanner) Open(kind byte) error {
	c, err := s.Peek()
	if err != nil {
		return s.eof(err)
	}
	if c != kind {
		return s.syntax(s.i, "(expecting "+strconv.QuoteRune(rune(kind))+")")
	}
	if len(s.open)+1 > MaxDepth {
		return &SyntaxError{Offset: s.Offset(), Msg: "exceeded max depth"}
	}
	s.i++
	s.open = append(s.open, kind)
	return nil
}

// Next moves to the next member of the container entered last and reports whether there is
// one; at its end it consumes the '}' or ']' and leaves it. An object member's name and ':'
// are consumed, and the caller consumes the value either way.
func (s *Scanner) Next() (bool, error) {
	top := s.open[len(s.open)-1]
	c, err := s.Peek()
	if err != nil {
		return false, err
	}
	if c == top&^hasMember+2 { // '}' or ']'
		s.i++
		s.open = s.open[:len(s.open)-1]
		return false, nil
	}
	if top&hasMember != 0 {
		if c != ',' {
			return false, s.syntax(s.i, "after value (expecting ',' or "+strconv.QuoteRune(rune(top&^hasMember+2))+")")
		}
		s.i++
		if c, err = s.Peek(); err != nil {
			return false, err
		}
	}
	s.open[len(s.open)-1] = top | hasMember
	if top&^hasMember == '[' {
		return true, nil
	}
	if c != '"' {
		return false, s.syntax(s.i, "at start of name (expecting '\"')")
	}
	start := s.Offset()
	if s.nameEsc, err = s.skipString(); err != nil {
		return false, err
	}
	s.name = append(s.name[:0], s.buf[start-s.base:s.i]...)
	if c, err = s.Peek(); err != nil {
		return false, err
	}
	if c != ':' {
		return false, s.syntax(s.i, "after name (expecting ':')")
	}
	s.i++
	return true, nil
}

// Name returns the name of the current object member.
func (s *Scanner) Name() string {
	if !s.nameEsc {
		return string(s.name[1 : len(s.name)-1])
	}
	return string(unquote(nil, s.name))
}

// NameIs reports whether the current object member is called name.
func (s *Scanner) NameIs(name string) bool { return isName(s.name, s.nameEsc, name) }

// unquote appends the value of the valid quoted string raw to dst.
func unquote(dst, raw []byte) []byte {
	dst, _ = jsontext.AppendUnquote(dst, raw)
	return dst
}

// StrBytes reads a string value; the bytes are valid until the next read.
func (s *Scanner) StrBytes() ([]byte, error) {
	c, err := s.Peek()
	if err != nil {
		return nil, s.eof(err)
	}
	if c != '"' {
		return nil, s.syntax(s.i, "(expecting a string)")
	}
	start := s.Offset()
	esc, err := s.skipString()
	if err != nil {
		return nil, err
	}
	raw := s.buf[start-s.base : s.i]
	if !esc {
		return raw[1 : len(raw)-1], nil
	}
	s.str = unquote(s.str[:0], raw)
	return s.str, nil
}

// Str reads a string value.
func (s *Scanner) Str() (string, error) {
	b, err := s.StrBytes()
	return string(b), err
}

// Bool reads true or false.
func (s *Scanner) Bool() (bool, error) {
	c, err := s.Peek()
	if err != nil {
		return false, s.eof(err)
	}
	switch c {
	case 't':
		return true, s.literal("true")
	case 'f':
		return false, s.literal("false")
	}
	return false, s.syntax(s.i, "(expecting a boolean)")
}

// Null reads null.
func (s *Scanner) Null() error {
	c, err := s.Peek()
	if err != nil {
		return s.eof(err)
	}
	if c != 'n' {
		return s.syntax(s.i, "(expecting null)")
	}
	return s.literal("null")
}

// The states of scan: what the next token must be.
const (
	wantValue      = iota
	wantFirstValue // a value or ']'
	wantKey
	wantFirstKey // a key or '}'
	wantColon
	wantFoundColon // the ':' after the name Find looks for
	afterValue     // ',' or the container's end
)

// Skip reads and validates the next value.
func (s *Scanner) Skip() error {
	_, err := s.scan(s.skip[:0], len(s.open), wantValue, "", false)
	return err
}

// Find skips the members of the object entered last up to the next one called name, and
// reports whether there is one; its name and ':' are consumed, and the caller consumes the
// value. At the object's end Find consumes the '}' and leaves the object, like Next. It is
// Next and Skip until NameIs(name), but faster.
func (s *Scanner) Find(name string) (bool, error) {
	top := s.open[len(s.open)-1]
	state := wantFirstKey
	if top&hasMember != 0 {
		state = afterValue
	}
	found, err := s.scan(append(s.skip[:0], '{'), len(s.open)-1, state, name, true)
	if err != nil {
		return false, err
	}
	if found {
		s.open[len(s.open)-1] = top | hasMember
		s.name, s.nameEsc = append(s.name[:0], '"'), false
		s.name = append(append(s.name, name...), '"')
		return true, nil
	}
	s.open = s.open[:len(s.open)-1]
	return false, nil
}

// scan reads values from the given state with stack holding the containers it is in, on top
// of depth others, until the stack is empty or, when finding, it reaches the ':' after a member
// called name of the outermost container. It works on a local copy of the buffer position and
// leaves the buffer to s's methods for refills and for the rare tokens: strings that are not
// plain ASCII or not buffered whole, numbers and literals.
func (s *Scanner) scan(stack []byte, depth, state int, name string, finding bool) (found bool, err error) {
	defer func() { s.skip = stack[:0] }()
	buf, i := s.buf, s.i
	for {
		if state == afterValue && len(stack) == 0 {
			s.i = i
			return false, nil
		}
		if i == len(buf) {
			s.i = i
			if err := s.more(); err != nil {
				return false, err
			}
			buf, i = s.buf, s.i
		}
		c := buf[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		switch state {
		case wantFirstValue:
			if c == ']' {
				i++
				stack = stack[:len(stack)-1]
				state = afterValue
				continue
			}
			fallthrough
		case wantValue:
			switch c {
			case '{', '[':
				if depth+len(stack)+1 > MaxDepth {
					s.i = i
					return false, &SyntaxError{Offset: s.Offset(), Msg: "exceeded max depth"}
				}
				i++
				stack = append(stack, c)
				state = wantFirstValue
				if c == '{' {
					state = wantFirstKey
				}
				continue
			case '"':
				if j := quickString(buf, i); j >= 0 {
					i = j
					break
				}
				s.i = i
				if _, err := s.skipString(); err != nil {
					return false, err
				}
				buf, i = s.buf, s.i
			case 't', 'f', 'n':
				s.i = i
				if err := s.literal(literals[c]); err != nil {
					return false, err
				}
				buf, i = s.buf, s.i
			default:
				s.i = i
				if err := s.skipNumber(depth+len(stack) == 0); err != nil {
					return false, err
				}
				buf, i = s.buf, s.i
			}
			state = afterValue
		case wantFirstKey:
			if c == '}' {
				i++
				stack = stack[:len(stack)-1]
				state = afterValue
				continue
			}
			fallthrough
		case wantKey:
			if c != '"' {
				s.i = i
				return false, s.syntax(i, "at start of name (expecting '\"')")
			}
			start, esc := i, false
			if j := quickString(buf, i); j >= 0 {
				i = j
			} else {
				at := s.base + int64(i)
				s.i = i
				if esc, err = s.skipString(); err != nil {
					return false, err
				}
				buf, i = s.buf, s.i
				start = int(at - s.base)
			}
			state = wantColon
			if finding && len(stack) == 1 && isName(buf[start:i], esc, name) {
				state = wantFoundColon
			}
		case wantColon, wantFoundColon:
			if c != ':' {
				s.i = i
				return false, s.syntax(i, "after name (expecting ':')")
			}
			i++
			if state == wantFoundColon {
				s.i = i
				return true, nil
			}
			state = wantValue
		case afterValue:
			switch top := stack[len(stack)-1]; c {
			case ',':
				i++
				state = wantValue
				if top == '{' {
					state = wantKey
				}
			case top + 2: // '}' or ']'
				i++
				stack = stack[:len(stack)-1]
			default:
				s.i = i
				return false, s.syntax(i, "after value (expecting ',' or "+strconv.QuoteRune(rune(top+2))+")")
			}
		}
	}
}

// isName reports whether the quoted name raw is name.
func isName(raw []byte, esc bool, name string) bool {
	if !esc {
		return len(raw) == len(name)+2 && string(raw[1:len(raw)-1]) == name
	}
	return string(unquote(nil, raw)) == name
}

// literals are the literals by their first byte.
var literals = [256]string{'t': "true", 'f': "false", 'n': "null"}

// literal reads true, false or null.
func (s *Scanner) literal(lit string) error {
	for n := range len(lit) {
		if err := s.need(n + 1); err != nil {
			return err
		}
		if s.buf[s.i+n] != lit[n] {
			return s.syntax(s.i+n, "in literal "+lit)
		}
	}
	s.i += len(lit)
	return nil
}

// skipNumber reads a number per RFC 8259. top tells that it is a top-level value, which the
// end of input may end.
func (s *Scanner) skipNumber(top bool) error {
	c, err := s.byteAt(top)
	if err != nil {
		return err
	}
	if c == '-' {
		s.i++
		if c, err = s.byteAt(false); err != nil {
			return err
		}
	}
	switch {
	case c == '0':
		s.i++
	case '1' <= c && c <= '9':
		s.i++
		if err := s.digits(top); err != nil {
			return err
		}
	default:
		return s.syntax(s.i, "at start of value")
	}
	if c, err = s.byteAt(top); err != nil || c != '.' && c != 'e' && c != 'E' {
		return ignoreEOF(err)
	}
	if c == '.' {
		s.i++
		if err := s.someDigits(top); err != nil {
			return err
		}
		if c, err = s.byteAt(top); err != nil || c != 'e' && c != 'E' {
			return ignoreEOF(err)
		}
	}
	s.i++ // e or E
	if c, err = s.byteAt(false); err != nil {
		return err
	}
	if c == '+' || c == '-' {
		s.i++
	}
	return s.someDigits(top)
}

// errEnd is byteAt's end of a top-level number.
var errEnd = errors.New("end of input")

// ignoreEOF drops the end of a top-level number.
func ignoreEOF(err error) error {
	if errors.Is(err, errEnd) {
		return nil
	}
	return err
}

// byteAt returns the next byte without consuming it. At the end of input it returns errEnd
// when end is set, else the SyntaxError of a truncated document.
func (s *Scanner) byteAt(end bool) (byte, error) {
	if s.i == len(s.buf) {
		if err := s.fill(); err != nil {
			if end && err == io.EOF { //nolint:errorlint // Read returns io.EOF itself
				return 0, errEnd
			}
			return 0, s.eof(err)
		}
	}
	return s.buf[s.i], nil
}

// someDigits reads one or more digits.
func (s *Scanner) someDigits(top bool) error {
	c, err := s.byteAt(false)
	if err != nil {
		return err
	}
	if c < '0' || c > '9' {
		return s.syntax(s.i, "in number (expecting digit)")
	}
	s.i++
	return s.digits(top)
}

// digits reads zero or more digits.
func (s *Scanner) digits(top bool) error {
	for {
		for s.i < len(s.buf) {
			if c := s.buf[s.i]; c < '0' || c > '9' {
				return nil
			}
			s.i++
		}
		if _, err := s.byteAt(top); err != nil {
			return ignoreEOF(err)
		}
	}
}

// skipString reads a string at its opening quote and reports whether it has escapes. The
// string stays buffered until the next read.
func (s *Scanner) skipString() (esc bool, err error) {
	s.tok = s.i
	esc, err = s.scanString()
	s.tok = -1
	return esc, err
}

// scanString is skipString.
func (s *Scanner) scanString() (esc bool, err error) {
	i := s.i + 1
	for {
		i = plainRun(s.buf, i)
		if i == len(s.buf) {
			s.i = i
			if err := s.more(); err != nil {
				return false, err
			}
			i = s.i
			continue
		}
		switch c := s.buf[i]; {
		case c == '"':
			s.i = i + 1
			return esc, nil
		case c == '\\':
			esc = true
			s.i = i
			if err := s.escape(); err != nil {
				return false, err
			}
		case c < ' ':
			return false, s.syntax(i, "in string (expecting non-control character)")
		default:
			s.i = i
			if err := s.multibyte(); err != nil {
				return false, err
			}
		}
		i = s.i
	}
}

// quickString returns the index after the string at b[i] when it is buffered whole and is
// plain ASCII without escapes; -1 otherwise.
func quickString(b []byte, i int) int {
	j := plainRun(b, i+1)
	if j < len(b) && b[j] == '"' {
		return j + 1
	}
	return -1
}

// plainRun returns the index of the first byte of b from i on that a string scan must look
// at: a quote, a backslash, a control character or a non-ASCII byte; len(b) if none. It tests
// eight bytes at a time.
func plainRun(b []byte, i int) int {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	for len(b)-i >= 8 {
		x := binary.LittleEndian.Uint64(b[i:])
		q, bs := x^(ones*'"'), x^(ones*'\\')
		// A byte's high bit is set when it is zero in q or bs, below ' ' in x, or non-ASCII;
		// borrows only mark bytes above an earlier hit, so the lowest set bit is exact.
		if m := ((q-ones)&^q | (bs-ones)&^bs | (x-ones*' ')&^x | x) & highs; m != 0 {
			return i + bits.TrailingZeros64(m)>>3
		}
		i += 8
	}
	for ; i < len(b); i++ {
		if c := b[i]; c < ' ' || c >= utf8.RuneSelf || c == '"' || c == '\\' {
			return i
		}
	}
	return i
}

// escape reads the escape sequence at s.i.
func (s *Scanner) escape() error {
	if err := s.need(2); err != nil {
		return err
	}
	switch s.buf[s.i+1] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		s.i += 2
		return nil
	case 'u':
	default:
		return s.syntax(s.i+1, "in string escape")
	}
	if err := s.need(6); err != nil {
		return err
	}
	v1, ok := hex4(s.buf[s.i+2 : s.i+6])
	if !ok {
		return &SyntaxError{Offset: s.Offset(), Msg: "invalid escape sequence"}
	}
	s.i += 6
	if !utf16.IsSurrogate(rune(v1)) {
		return nil
	}
	if err := s.need(6); err != nil {
		return err
	}
	b := s.buf[s.i : s.i+6]
	v2, ok := hex4(b[2:])
	if b[0] != '\\' || b[1] != 'u' || !ok || utf16.DecodeRune(rune(v1), rune(v2)) == utf8.RuneError {
		return &SyntaxError{Offset: s.Offset() - 6, Msg: "invalid surrogate pair"}
	}
	s.i += 6
	return nil
}

// hex4 parses four hex digits.
func hex4(b []byte) (v uint16, ok bool) {
	for _, c := range b {
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c -= 'a' - 10
		case 'A' <= c && c <= 'F':
			c -= 'A' - 10
		default:
			return 0, false
		}
		v = v<<4 | uint16(c)
	}
	return v, true
}

// multibyte reads the UTF-8 sequence at s.i.
func (s *Scanner) multibyte() error {
	for !utf8.FullRune(s.buf[s.i:]) {
		if err := s.more(); err != nil {
			return err
		}
	}
	r, n := utf8.DecodeRune(s.buf[s.i:])
	if r == utf8.RuneError && n == 1 {
		return &SyntaxError{Offset: s.Offset(), Msg: "invalid UTF-8 in string"}
	}
	s.i += n
	return nil
}
