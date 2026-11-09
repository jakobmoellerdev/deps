package clojure

import (
	"errors"
	"strings"
)

// kind is the kind of a form read by readForms.
type kind uint8

const (
	kindList    kind = iota // (…), also #(…) and reader conditionals #?(…)
	kindVector              // […]
	kindMap                 // {…}, also #:ns{…}
	kindSet                 // #{…}
	kindString              // "…", also #"…" regex literals
	kindKeyword             // :name, ::name
	kindSymbol              // name, ns/name, nil, true, false
	kindOther               // numbers, characters, ##Inf
)

// form is a Clojure/EDN form with its byte span. Discarded forms (`#_`) and metadata (`^…`)
// are not elements of their collection; metadata is kept in meta.
type form struct {
	kind       kind
	start, end int    // the form's bytes, prefixes (`~`, `'`) excluded
	text       string // keyword without `:`, symbol, string contents between the quotes, namespace of `#:ns{…}`
	plain      bool   // a string without escapes: text is its value
	unquote    bool   // written `~form` (Leiningen evaluates it)
	kids       []*form
	meta       []*form // ^:replace, ^{…} before the form
}

// isKeyword reports whether f is the keyword :name.
func (f *form) isKeyword(name string) bool {
	return f != nil && f.kind == kindKeyword && f.text == name
}

// mapGet returns the value of the keyword key in the map f, nil when f is no map or lacks it.
func (f *form) mapGet(key string) *form {
	if f == nil || f.kind != kindMap {
		return nil
	}
	for i := 0; i+1 < len(f.kids); i += 2 {
		if f.kids[i].isKeyword(key) {
			return f.kids[i+1]
		}
	}
	return nil
}

// hasMeta reports whether f carries the metadata keyword :name (`^:replace`).
func (f *form) hasMeta(name string) bool {
	for _, m := range f.meta {
		if m.isKeyword(name) || m.mapGet(name) != nil {
			return true
		}
	}
	return false
}

var errUnbalanced = errors.New("unbalanced brackets")

// reader reads Clojure forms from src.
type reader struct {
	src []byte
	pos int
}

// readForms returns the top-level forms of src, a Clojure source or EDN file.
func readForms(src []byte) ([]*form, error) {
	r := &reader{src: src}
	var out []*form
	for {
		f, closed, err := r.read()
		switch {
		case err != nil:
			return nil, err
		case closed != 0:
			return nil, errUnbalanced
		case f == nil:
			return out, nil
		}
		out = append(out, f)
	}
}

// delimiter reports whether c ends a symbol, keyword or number token.
func delimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', ',', '(', ')', '[', ']', '{', '}', '"', ';':
		return true
	}
	return false
}

// skipSpace skips whitespace, commas and `;` comments.
func (r *reader) skipSpace() {
	for r.pos < len(r.src) {
		switch r.src[r.pos] {
		case ';':
			for r.pos < len(r.src) && r.src[r.pos] != '\n' {
				r.pos++
			}
		case ' ', '\t', '\n', '\r', '\f', ',':
			r.pos++
		default:
			return
		}
	}
}

// token reads a symbol, keyword or number token at pos.
func (r *reader) token() string {
	start := r.pos
	for r.pos < len(r.src) && !delimiter(r.src[r.pos]) {
		r.pos++
	}
	return string(r.src[start:r.pos])
}

// read returns the next form, or nil at the end of the input or before a closing bracket,
// which it consumes and returns as closed.
func (r *reader) read() (f *form, closed byte, err error) {
	var meta []*form
	unquote := false
	for {
		r.skipSpace()
		if r.pos >= len(r.src) {
			if meta != nil || unquote {
				return nil, 0, errors.New("form expected at end of input")
			}
			return nil, 0, nil
		}
		start := r.pos
		c := r.src[r.pos]
		switch c {
		case ')', ']', '}':
			if meta != nil || unquote {
				return nil, 0, errUnbalanced
			}
			r.pos++
			return nil, c, nil
		case '(', '[', '{', '"':
			var f *form
			var err error
			if i := strings.IndexByte("([{", c); i >= 0 {
				f, err = r.coll(start, [3]kind{kindList, kindVector, kindMap}[i], ")]}"[i])
			} else {
				f, err = r.str(start)
			}
			if f != nil {
				f.meta, f.unquote = meta, unquote
			}
			return f, 0, err
		case '^': // metadata: the next form applies to the one after it
			r.pos++
			m, err := r.need()
			if err != nil {
				return nil, 0, err
			}
			meta = append(meta, m)
			continue
		case '~':
			r.pos++
			if r.pos < len(r.src) && r.src[r.pos] == '@' {
				r.pos++
			}
			unquote = true
			continue
		case '\'', '`', '@':
			r.pos++
			continue
		case '\\': // character literal: \a, \(, \newline, \u00e9
			r.pos += 2
			for r.pos < len(r.src) && !delimiter(r.src[r.pos]) {
				r.pos++
			}
			return &form{kind: kindOther, start: start, end: min(r.pos, len(r.src)), meta: meta, unquote: unquote}, 0, nil
		case '#':
			f, skip, err := r.dispatch(start)
			if err != nil {
				return nil, 0, err
			}
			if skip {
				continue
			}
			f.meta, f.unquote = meta, unquote
			return f, 0, nil
		}
		t := r.token()
		f := &form{kind: kindSymbol, start: start, end: r.pos, text: t, meta: meta, unquote: unquote}
		switch {
		case strings.HasPrefix(t, ":"):
			f.kind, f.text = kindKeyword, strings.TrimLeft(t, ":")
		case c >= '0' && c <= '9' || (c == '+' || c == '-') && len(t) > 1 && t[1] >= '0' && t[1] <= '9':
			f.kind = kindOther
		}
		return f, 0, nil
	}
}

// need reads a form that must exist.
func (r *reader) need() (*form, error) {
	f, closed, err := r.read()
	switch {
	case err != nil:
		return nil, err
	case closed != 0 || f == nil:
		return nil, errUnbalanced
	}
	return f, nil
}

// dispatch reads a `#` form at start: sets, regexes, anonymous functions, reader
// conditionals, namespaced maps, symbolic values and tagged literals. skip reports a
// discarded form (`#_`) or a tag (`#inst`), after which the next form is read.
func (r *reader) dispatch(start int) (f *form, skip bool, err error) {
	r.pos++
	if r.pos >= len(r.src) {
		return nil, false, errors.New("form expected after #")
	}
	switch c := r.src[r.pos]; c {
	case '{':
		f, err = r.coll(start, kindSet, '}')
		return f, false, err
	case '(':
		f, err = r.coll(start, kindList, ')')
		return f, false, err
	case '"':
		f, err = r.str(start)
		return f, false, err
	case '_':
		r.pos++
		_, err = r.need()
		return nil, true, err
	case '\'', '=': // var quote #'sym, read-eval #=(…)
		r.pos++
		return nil, true, nil
	case '#': // ##Inf, ##NaN
		r.token()
		return &form{kind: kindOther, start: start, end: r.pos}, false, nil
	case '?': // #?(…), #?@(…)
		r.pos++
		if r.pos < len(r.src) && r.src[r.pos] == '@' {
			r.pos++
		}
		r.skipSpace()
		if r.pos >= len(r.src) || r.src[r.pos] != '(' {
			return nil, false, errors.New("reader conditional without list")
		}
		f, err = r.coll(start, kindList, ')')
		return f, false, err
	case ':': // #:ns{…} namespaced map; text is the namespace
		ns := strings.TrimLeft(r.token(), ":")
		if r.pos >= len(r.src) || r.src[r.pos] != '{' {
			return nil, false, errors.New("namespaced map without map")
		}
		if f, err = r.coll(start, kindMap, '}'); f != nil {
			f.text = ns
		}
		return f, false, err
	}
	r.token() // a tagged literal's tag: the tagged form follows
	return nil, true, nil
}

// coll reads a collection whose opening bracket is at pos (the form starts at start, before a
// `#` prefix) and whose elements end at closer.
func (r *reader) coll(start int, k kind, closer byte) (*form, error) {
	r.pos++
	f := &form{kind: k, start: start}
	for {
		kid, closed, err := r.read()
		switch {
		case err != nil:
			return nil, err
		case closed == closer:
			f.end = r.pos
			return f, nil
		case closed != 0 || kid == nil:
			return nil, errUnbalanced
		}
		f.kids = append(f.kids, kid)
	}
}

// str reads a string whose opening quote is at pos; start is where the form starts (before
// `#` of a regex).
func (r *reader) str(start int) (*form, error) {
	r.pos++ // opening quote
	open := r.pos
	plain := true
	for r.pos < len(r.src) {
		switch r.src[r.pos] {
		case '\\':
			plain = false
			r.pos += 2
			continue
		case '"':
			f := &form{kind: kindString, start: start, end: r.pos + 1, text: string(r.src[open:r.pos]), plain: plain}
			r.pos++
			return f, nil
		}
		r.pos++
	}
	return nil, errors.New("unterminated string")
}
