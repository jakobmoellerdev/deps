package helmds

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"
)

// field is an index version key that deps reads.
type field uint8

const (
	fieldOther field = iota
	fieldVersion
	fieldAppVersion
	fieldCreated
	fieldDigest
	fieldURLs
)

// chartScan reads the version list of one captured chart block line by line, without building
// a YAML node tree. It handles the block style `helm repo index` writes: one `- key: value`
// mapping per version with plain or quoted one-line values for the keys deps reads, and URLs as
// a block sequence. It reads the other keys' values only far enough to know where they end
// (quoted and flow scalars, block scalars, plain continuation lines); anything it cannot place
// with certainty (anchors, tags or aliases on read keys, explicit or quoted keys, escapes,
// multi-line flow collections, tabs, YAML line breaks other than LF, control characters) makes
// scanChart report false, and the caller decodes the block as YAML instead.
type chartScan struct {
	rels []Release

	dash, item int      // columns of the version dashes and of the current version's keys
	keys       [][]byte // keys of the current version, for the duplicate key check
	cur        struct {
		version, appVersion, created, digest []byte
		urls                                 int
	}
	field   field // last key of the current version
	empty   bool  // its value is empty: nested block content may follow
	urlDash int   // column of the urls sequence dashes, -1 before the first

	quote       byte // quote of a quoted scalar continued on the next line, 0 = none
	blockParent int  // parent column of an open block scalar, -1 = none
	plainParent int  // parent column of a plain scalar that may continue, -1 = none
}

// scanChart decodes a chart block captured by scanIndex like decodeChartYAML does; false when
// the block is outside the layout chartScan handles.
func scanChart(block []byte, chart string) ([]Release, bool) {
	if !printable(block) {
		return nil, false
	}
	line, rest, _ := bytes.Cut(block, []byte("\n"))
	if !chartLine(line, chart) {
		return nil, false
	}
	s := chartScan{dash: -1, item: -1, urlDash: -1, blockParent: -1, plainParent: -1}
	for len(rest) > 0 {
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if !s.line(line) {
			return nil, false
		}
	}
	if s.quote != 0 {
		return nil, false
	}
	s.finish()
	if s.rels == nil {
		s.rels = []Release{}
	}
	return s.rels, true
}

// printable reports whether b holds only characters the YAML reader accepts and LF as its only
// line break: no other control characters, CR, NEL, line or paragraph separators, byte order
// marks or invalid UTF-8.
func printable(b []byte) bool {
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
				return false
			}
			i++
			continue
		}
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && n == 1, r < 0xa0, r == 0x2028, r == 0x2029, r == 0xfeff, r == 0xfffe, r == 0xffff:
			return false
		}
		i += n
	}
	return true
}

// chartLine reports whether line is the block's `chart:` key line with nothing after the colon
// but a comment.
func chartLine(line []byte, chart string) bool {
	var key, rest []byte
	switch {
	case len(line) == 0:
		return false
	case line[0] == '"' || line[0] == '\'':
		end := bytes.IndexByte(line[1:], line[0]) + 1
		if end == 0 {
			return false
		}
		key, rest = line[1:end], line[end+1:]
		if bytes.IndexByte(key, '\\') >= 0 && line[0] == '"' {
			return false
		}
	default:
		i := bytes.IndexByte(line, ':')
		if i < 0 || isIndicator(line[0]) || isNull(line[:i]) || bytes.Contains(line[:i], []byte(" #")) {
			return false
		}
		key, rest = line[:i], line[i:]
	}
	if string(key) != chart || len(rest) == 0 || rest[0] != ':' {
		return false
	}
	return trailing(rest[1:])
}

// line consumes one line of the version list.
func (s *chartScan) line(line []byte) bool {
	if s.quote != 0 {
		end := closeQuote(line, s.quote)
		if end < 0 {
			return true
		}
		s.quote = 0
		return trailing(line[end+1:])
	}
	content := bytes.TrimLeft(line, " ")
	indent := len(line) - len(content)
	if len(content) == 0 {
		return true
	}
	if s.blockParent >= 0 {
		if indent > s.blockParent {
			return true
		}
		s.blockParent = -1
	}
	if content[0] == '#' {
		s.plainParent = -1
		return true
	}
	if s.plainParent >= 0 {
		if indent > s.plainParent {
			return true
		}
		s.plainParent = -1
	}
	if bytes.IndexByte(content, '\t') >= 0 {
		return false
	}
	switch {
	case s.dash < 0 || indent == s.dash && isDash(content):
		if s.dash >= 0 {
			s.finish()
		}
		s.dash = indent
		return s.startItem(content, indent)
	case indent < s.item:
		return false
	case indent == s.item && !isDash(content):
		return s.key(content, indent)
	case s.field == fieldURLs:
		return s.empty && isDash(content) && s.url(content, indent)
	case s.field == fieldOther:
		// Nested content of the last key: deeper lines, or an indentless sequence.
		return (indent > s.item || s.empty) && s.lex(content, indent, -1)
	default:
		// A read scalar continued on the next line, or given block content.
		return false
	}
}

// isDash reports whether content starts with a block sequence entry indicator.
func isDash(content []byte) bool {
	return content[0] == '-' && (len(content) == 1 || content[1] == ' ')
}

// startItem starts a version at its `- key: value` line.
func (s *chartScan) startItem(content []byte, indent int) bool {
	if !isDash(content) {
		return false
	}
	rest := bytes.TrimLeft(content[1:], " ")
	if len(rest) == 0 {
		return false
	}
	s.item = indent + len(content) - len(rest)
	s.keys = s.keys[:0]
	s.cur.version, s.cur.appVersion, s.cur.created, s.cur.digest, s.cur.urls = nil, nil, nil, nil, 0
	return s.key(rest, s.item)
}

// finish keeps the current version if it has a version and download URLs.
func (s *chartScan) finish() {
	c := &s.cur
	if len(c.version) == 0 || c.urls == 0 {
		return
	}
	var created time.Time
	if len(c.created) > 0 {
		created, _ = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(c.created)))
	}
	s.rels = append(s.rels, Release{Version: string(c.version), AppVersion: string(c.appVersion), Created: created, Digest: string(c.digest)})
}

// key consumes a `key: value` line of the current version at column col.
func (s *chartScan) key(content []byte, col int) bool {
	i := 0
	for i < len(content) && isKeyByte(content[i]) {
		i++
	}
	if i == 0 || i > 1024 || !isLetter(content[0]) || i == len(content) || content[i] != ':' || i+1 < len(content) && content[i+1] != ' ' {
		return false
	}
	key := content[:i]
	for _, k := range s.keys {
		if bytes.Equal(k, key) {
			return false
		}
	}
	s.keys = append(s.keys, key)
	v := bytes.TrimLeft(content[i+1:], " ")
	s.field, s.urlDash = fieldOf(key), -1
	s.empty = len(v) == 0 || v[0] == '#'
	var dst *[]byte
	switch s.field {
	case fieldVersion:
		dst = &s.cur.version
	case fieldAppVersion:
		dst = &s.cur.appVersion
	case fieldCreated:
		dst = &s.cur.created
	case fieldDigest:
		dst = &s.cur.digest
	case fieldURLs:
		if s.empty {
			return true
		}
		v = cutComment(v)
		return string(v) == "[]" || isNull(v)
	default:
		return s.lex(v, col+len(content)-len(v), col)
	}
	val, ok := scalar(v)
	*dst = val
	return ok
}

// url consumes one `- url` entry of a block sequence.
func (s *chartScan) url(content []byte, indent int) bool {
	if s.urlDash < 0 {
		s.urlDash = indent
	}
	if indent != s.urlDash {
		return false
	}
	val, ok := scalar(bytes.TrimLeft(content[1:], " "))
	if !ok || val == nil {
		return false
	}
	s.cur.urls++
	return true
}

func fieldOf(key []byte) field {
	switch string(key) {
	case "version":
		return fieldVersion
	case "appVersion":
		return fieldAppVersion
	case "created":
		return fieldCreated
	case "digest":
		return fieldDigest
	case "urls":
		return fieldURLs
	}
	return fieldOther
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isKeyByte(c byte) bool {
	return isLetter(c) || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.'
}

// isIndicator reports whether a plain scalar may not start with c (or deps does not read one
// that does).
func isIndicator(c byte) bool {
	return strings.IndexByte("-?:,[]{}#&*!|>'\"%@`", c) >= 0
}

// isNull reports whether a plain scalar is null.
func isNull(v []byte) bool {
	switch string(v) {
	case "", "~", "null", "Null", "NULL":
		return true
	}
	return false
}

// trailing reports whether rest, following a complete value, is blank or a comment.
func trailing(rest []byte) bool {
	t := bytes.TrimLeft(rest, " ")
	return len(t) == 0 || t[0] == '#' && len(t) < len(rest)
}

// cutComment returns a plain value without its comment and trailing spaces.
func cutComment(v []byte) []byte {
	if i := bytes.Index(v, []byte(" #")); i >= 0 {
		v = v[:i]
	}
	return bytes.TrimRight(v, " ")
}

// scalar returns the string a one-line scalar value decodes to, nil for null; false for values
// that are not one-line plain or quoted scalars, or that need escapes resolved.
func scalar(v []byte) ([]byte, bool) {
	if len(v) == 0 || v[0] == '#' {
		return nil, true
	}
	switch q := v[0]; q {
	case '"', '\'':
		end := closeQuote(v[1:], q) + 1
		if end == 0 || !trailing(v[end+1:]) {
			return nil, false
		}
		val := v[1:end]
		if q == '"' {
			return val, bytes.IndexByte(val, '\\') < 0
		}
		if bytes.Contains(val, []byte("''")) {
			val = bytes.ReplaceAll(val, []byte("''"), []byte("'"))
		}
		return val, true
	}
	if isIndicator(v[0]) {
		return nil, false
	}
	v = cutComment(v)
	if bytes.Contains(v, []byte(": ")) || v[len(v)-1] == ':' {
		return nil, false
	}
	if isNull(v) {
		return nil, true
	}
	return v, true
}

// closeQuote returns the index of the quote that closes a q-quoted scalar continued in b, -1
// when it continues past b.
func closeQuote(b []byte, q byte) int {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '\\':
			if q == '"' {
				i++
			}
		case q:
			if q == '\'' && i+1 < len(b) && b[i+1] == '\'' {
				i++
				continue
			}
			return i
		}
	}
	return -1
}

// closeFlow returns the index after the bracket that closes the flow collection v starts, -1
// unless it closes on this line without quotes or comments.
func closeFlow(v []byte) int {
	depth := 0
	for i, c := range v {
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		case '"', '\'', '#':
			return -1
		}
	}
	return -1
}

// plainKeyEnd returns the index of the colon ending a plain key at the start of v, -1 when v
// is a plain scalar.
func plainKeyEnd(v []byte) int {
	for i, c := range v {
		switch {
		case c == ':' && (i+1 == len(v) || v[i+1] == ' '):
			return i
		case c == '#' && i > 0 && v[i-1] == ' ':
			return -1
		}
	}
	return -1
}

// lex follows a value deps does not read, v starting at column col, far enough to know which
// of the next lines belong to it. parent is the column of the key or dash v belongs to, -1 for
// none on this line.
func (s *chartScan) lex(v []byte, col, parent int) bool {
	for {
		if len(v) == 0 || v[0] == '#' {
			return true
		}
		var rest []byte
		switch c := v[0]; {
		case c == '-' && (len(v) == 1 || v[1] == ' '):
			parent, rest = col, bytes.TrimLeft(v[1:], " ")
		case (c == '?' || c == ':') && (len(v) == 1 || v[1] == ' '):
			return false
		case c == '&' || c == '!':
			i := bytes.IndexByte(v, ' ')
			if i < 0 {
				return true
			}
			rest = bytes.TrimLeft(v[i:], " ")
		case c == '"' || c == '\'':
			end := closeQuote(v[1:], c) + 1
			if end == 0 {
				s.quote = c
				return true
			}
			if rest = v[end+1:]; len(rest) == 0 || rest[0] != ':' || len(rest) > 1 && rest[1] != ' ' {
				return trailing(rest)
			}
			parent, rest = col, bytes.TrimLeft(rest[1:], " ")
		case c == '[' || c == '{':
			end := closeFlow(v)
			if end < 0 {
				return false
			}
			if rest = v[end:]; len(rest) == 0 || rest[0] != ':' || len(rest) > 1 && rest[1] != ' ' {
				return trailing(rest)
			}
			parent, rest = col, bytes.TrimLeft(rest[1:], " ")
		case c == '|' || c == '>':
			s.blockParent = parent
			return parent >= 0
		case c == '*':
			return true
		default:
			i := plainKeyEnd(v)
			if i < 0 {
				s.plainParent = parent
				return parent >= 0
			}
			parent, rest = col, bytes.TrimLeft(v[i+1:], " ")
		}
		col += len(v) - len(rest)
		v = rest
	}
}
