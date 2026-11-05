package bundler

import (
	"bytes"
	"strings"
)

// A Gemfile is Ruby. The lexer below knows enough of Ruby's syntax to find the `gem`, `source`,
// `group` and block statements of real Gemfiles: strings (with interpolation and escapes, which
// make a string non-literal), symbols, hash labels, %-literals (`%i[mri windows]`), comments,
// `=begin`/`=end` blocks, heredoc bodies and `__END__`. Everything else is punctuation or an
// opaque token.

type tokKind uint8

const (
	tIdent   tokKind = iota // identifiers and keywords
	tLabel                  // `name:` hash key (text without the colon)
	tSymbol                 // `:name` or `:"name"` (text without the colon)
	tString                 // a quoted string (text is its content)
	tPunct                  // punctuation and operators
	tNewline                // end of a line
	tOther                  // numbers, %-literals, anything else
)

type token struct {
	kind       tokKind
	text       string
	start, end int  // bytes of the whole token
	vs, ve     int  // tString: bytes of the content, between the quotes
	literal    bool // tString: no interpolation or escapes, so text is the content as written
	line       int  // 1-based line of the token's start
}

func isIdentStart(c byte) bool { return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isIdentChar(c byte) bool { return isIdentStart(c) || '0' <= c && c <= '9' }

// lex splits src into tokens.
func lex(src []byte) []token {
	var (
		toks      []token
		line      = 1
		lineStart = true
		heredocs  []string // terminators of the heredocs started on the current line
	)
	emit := func(t token) {
		t.line = line
		toks = append(toks, t)
		lineStart = false
	}
	for i := 0; i < len(src); {
		if lineStart {
			rest := src[i:]
			if bytes.HasPrefix(rest, []byte("__END__")) && (len(rest) == 7 || rest[7] == '\n' || rest[7] == '\r') {
				break
			}
			if bytes.HasPrefix(rest, []byte("=begin")) && (len(rest) == 6 || !isIdentChar(rest[6])) {
				// Skip to the end of the `=end` line.
				for i < len(src) {
					eol := bytes.IndexByte(src[i:], '\n')
					l := src[i:]
					if eol >= 0 {
						l = src[i : i+eol]
					}
					i += len(l)
					if i < len(src) {
						i++
						line++
					}
					if bytes.HasPrefix(l, []byte("=end")) {
						break
					}
				}
				continue
			}
		}
		c := src[i]
		switch {
		case c == '\n':
			emit(token{kind: tNewline, start: i, end: i + 1})
			i++
			line++
			lineStart = true
			for _, term := range heredocs { // skip the bodies, up to each terminator line
				for i < len(src) {
					eol := bytes.IndexByte(src[i:], '\n')
					l := src[i:]
					if eol >= 0 {
						l = src[i : i+eol]
					}
					i += len(l)
					if i < len(src) {
						i++
						line++
					}
					if string(bytes.TrimSpace(l)) == term {
						break
					}
				}
			}
			heredocs = heredocs[:0]
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			i += 2 // line continuation
			line++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			t, n := lexString(src, i)
			emit(t)
			line += n
			i = t.end
		case c == ':':
			switch {
			case i+1 < len(src) && src[i+1] == ':':
				emit(token{kind: tPunct, text: "::", start: i, end: i + 2})
				i += 2
			case i+1 < len(src) && isIdentStart(src[i+1]):
				j := i + 1
				for j < len(src) && isIdentChar(src[j]) {
					j++
				}
				if j < len(src) && (src[j] == '?' || src[j] == '!' || src[j] == '=') {
					j++
				}
				emit(token{kind: tSymbol, text: string(src[i+1 : j]), start: i, end: j})
				i = j
			case i+1 < len(src) && (src[i+1] == '"' || src[i+1] == '\''):
				t, n := lexString(src, i+1)
				t.kind, t.start = tSymbol, i
				emit(t)
				line += n
				i = t.end
			default:
				emit(token{kind: tPunct, text: ":", start: i, end: i + 1})
				i++
			}
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == '?' || src[j] == '!') && (j+1 == len(src) || src[j+1] != '=') {
				j++
			}
			if j < len(src) && src[j] == ':' && (j+1 == len(src) || src[j+1] != ':') {
				emit(token{kind: tLabel, text: string(src[i:j]), start: i, end: j + 1})
				i = j + 1
				continue
			}
			emit(token{kind: tIdent, text: string(src[i:j]), start: i, end: j})
			i = j
		case '0' <= c && c <= '9':
			j := i
			for j < len(src) && (isIdentChar(src[j]) || src[j] == '.' && j+1 < len(src) && '0' <= src[j+1] && src[j+1] <= '9') {
				j++
			}
			emit(token{kind: tOther, start: i, end: j})
			i = j
		case c == '%' && i+2 < len(src) && strings.IndexByte("wWiIqQ", src[i+1]) >= 0 && !isIdentChar(src[i+2]) && src[i+2] > ' ':
			end, n := skipDelimited(src, i+2)
			emit(token{kind: tOther, start: i, end: end})
			line += n
			i = end
		case c == '<' && i+2 < len(src) && src[i+1] == '<' && heredocStart(src[i+2:]) != "":
			term := heredocStart(src[i+2:])
			heredocs = append(heredocs, term)
			j := i + 2
			for j < len(src) && (src[j] == '~' || src[j] == '-' || src[j] == '"' || src[j] == '\'' || isIdentChar(src[j])) {
				j++
			}
			emit(token{kind: tOther, start: i, end: j})
			i = j
		case c == '=' && i+1 < len(src) && src[i+1] == '>', c == '-' && i+1 < len(src) && src[i+1] == '>':
			emit(token{kind: tPunct, text: string(src[i : i+2]), start: i, end: i + 2})
			i += 2
		default:
			emit(token{kind: tPunct, text: string(src[i : i+1]), start: i, end: i + 1})
			i++
		}
	}
	return toks
}

// lexString lexes the string starting with the quote at src[i] and returns it with the number
// of newlines it spans. Single-quoted strings are literal unless they hold a backslash; double
// and backtick strings also unless they interpolate (`#{…}`, `#@`, `#$`). An unterminated
// string runs to the end of src.
func lexString(src []byte, i int) (token, int) {
	q := src[i]
	t := token{kind: tString, start: i, vs: i + 1, literal: true}
	lines, depth := 0, 0
	j := i + 1
	for ; j < len(src); j++ {
		c := src[j]
		switch {
		case c == '\n':
			lines++
		case c == '\\':
			t.literal = false
			j++
			if j < len(src) && src[j] == '\n' {
				lines++
			}
		case q != '\'' && c == '#' && j+1 < len(src) && (src[j+1] == '{' || src[j+1] == '@' || src[j+1] == '$'):
			t.literal = false
			if src[j+1] == '{' {
				depth++
				j++
			}
		case depth > 0 && c == '}':
			depth--
		case depth == 0 && c == q:
			t.ve, t.end = j, j+1
			t.text = string(src[t.vs:t.ve])
			return t, lines
		}
	}
	t.ve, t.end = len(src), len(src)
	t.text = string(src[t.vs:t.ve])
	return t, lines
}

// skipDelimited returns the end of the %-literal whose delimiter is src[i] (nesting paired
// delimiters) and the newlines it spans.
func skipDelimited(src []byte, i int) (int, int) {
	open := src[i]
	closer := open
	switch open {
	case '[':
		closer = ']'
	case '(':
		closer = ')'
	case '{':
		closer = '}'
	case '<':
		closer = '>'
	}
	depth, lines := 1, 0
	for j := i + 1; j < len(src); j++ {
		switch c := src[j]; {
		case c == '\n':
			lines++
		case c == '\\':
			j++
		case c == closer:
			depth--
			if depth == 0 {
				return j + 1, lines
			}
		case c == open && open != closer:
			depth++
		}
	}
	return len(src), lines
}

// heredocStart returns the terminator of a heredoc whose `<<` precedes s (`<<~EOS`, `<<-EOS`,
// `<<EOS`, `<<~'EOS'`), or "" when s does not start one. A bare `<<x` with a lower-case
// identifier is an append, not a heredoc.
func heredocStart(s []byte) string {
	i := 0
	squiggly := i < len(s) && (s[i] == '~' || s[i] == '-')
	if squiggly {
		i++
	}
	quote := byte(0)
	if i < len(s) && (s[i] == '\'' || s[i] == '"') {
		quote = s[i]
		i++
	}
	j := i
	for j < len(s) && isIdentChar(s[j]) {
		j++
	}
	if j == i || !isIdentStart(s[i]) {
		return ""
	}
	if quote != 0 && (j == len(s) || s[j] != quote) {
		return ""
	}
	if !squiggly && quote == 0 && !('A' <= s[i] && s[i] <= 'Z') {
		return ""
	}
	return string(s[i:j])
}

// statements splits toks into statements, without their newline tokens: a newline or `;`
// outside brackets ends one unless the line ends with `,`, `=>`, `.`, `&&` or `||`.
func statements(toks []token) [][]token {
	var out [][]token
	// The tokens without newlines; statements are sub-slices of it, and it never grows past
	// its capacity, so they stay valid.
	kept := make([]token, 0, len(toks))
	start, depth := 0, 0
	for _, t := range toks {
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			depth = max(depth-1, 0)
		case t.kind == tNewline && (depth > 0 || continues(kept[start:])):
			continue
		case depth == 0 && (t.kind == tNewline || t.kind == tPunct && t.text == ";"):
			if len(kept) > start {
				out = append(out, kept[start:len(kept):len(kept)])
			}
			start = len(kept)
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) > start {
		out = append(out, kept[start:])
	}
	return out
}

// continues reports whether the statement s, cut at a newline, goes on on the next line: it
// ends with `,`, `=>`, `.`, `&&` or `||` (a single `|` ends block parameters, `do |x|`).
func continues(s []token) bool {
	n := len(s)
	if n == 0 || s[n-1].kind != tPunct {
		return false
	}
	switch last := s[n-1]; last.text {
	case ",", "=>", ".":
		return true
	case "&", "|":
		return n >= 2 && s[n-2].kind == tPunct && s[n-2].text == last.text && s[n-2].end == last.start
	}
	return false
}

// Block kinds of the Gemfile DSL.
type blockKind uint8

const (
	blockOther  blockKind = iota // if/unless/platforms/install_if/env/… — gems inside are plain
	blockGroup                   // group :x do
	blockSource                  // source "url" do
	blockGit                     // git/github/… "x" do
	blockPath                    // path "x" do
)

type block struct {
	kind   blockKind
	source string // blockSource: the URL; "" when it is not a literal
}

// gemDecl is a `gem` statement.
type gemDecl struct {
	name    string
	nameEnd int // the byte after the name's closing quote
	line    int
	reqs    []token // the literal requirement strings
	// reqStart and reqEnd span the requirement arguments (strings or an array), quotes and
	// brackets included; equal when there are none.
	reqStart, reqEnd int
	nonLiteral       bool   // a requirement argument that is not a literal string
	skip             string // git, path or non-literal source
	source           string // own source (`source:` or the enclosing source block)
	ownSource        bool
	group            bool
}

// rubyDecl is the `ruby` directive: the Ruby version the Gemfile requires.
type rubyDecl struct {
	line       int
	reqs       []token // the literal requirement strings
	nonLiteral bool    // a requirement argument that is not a literal string
	skip       string  // file, engine or patchlevel option
	file       string  // the `file:` option: the version file, relative to the Gemfile's directory
}

// gemfile is what parse finds in a Gemfile.
type gemfile struct {
	gems    []gemDecl
	sources []string  // the global sources (`source "url"` outside blocks), in order
	ruby    *rubyDecl // the last `ruby` directive (Bundler keeps the last); nil = none
}

// defaultSource is rubygems.org, as the legacy `source :rubygems` names it.
const defaultSource = "https://rubygems.org"

// Option keys of `gem` that make it a git dependency (Bundler's built-in git sources).
var gitKeys = map[string]bool{"git": true, "github": true, "gitlab": true, "bitbucket": true, "gist": true}

// parse finds the gems and sources of a Gemfile.
func parse(src []byte) gemfile {
	var (
		f          gemfile
		stack      []block
		gitSources = map[string]bool{} // git_source(:name) declarations
	)
	for _, s := range statements(lex(src)) {
		first := s[0]
		if first.kind != tIdent {
			continue
		}
		last := s[len(s)-1]
		oneLine := len(s) > 1 && last.kind == tIdent && last.text == "end"
		doAt := blockDo(s)
		switch first.text {
		case "end":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		case "if", "unless", "while", "until", "case", "begin", "def", "class", "module", "for":
			if !oneLine {
				stack = append(stack, block{kind: blockOther})
			}
			continue
		case "gem":
			if g, ok := parseGem(s, gitSources); ok {
				for _, b := range stack {
					switch b.kind {
					case blockGroup:
						g.group = true
					case blockSource:
						if !g.ownSource {
							g.source, g.ownSource = b.source, true
							if b.source == "" && g.skip == "" {
								g.skip = skipNonLiteralSource
							}
						}
					case blockGit:
						g.skip = skipGit
					case blockPath:
						g.skip = skipPath
					}
				}
				f.gems = append(f.gems, g)
			}
			continue
		case "git_source":
			if name, ok := firstSymbol(s[1:]); ok {
				gitSources[name] = true
			}
			continue
		case "ruby":
			if doAt < 0 {
				r := parseRubyDirective(s)
				f.ruby = &r
				continue
			}
		}
		if doAt < 0 {
			if first.text == "source" {
				if url, ok := sourceArg(s[1:]); ok {
					f.sources = append(f.sources, url)
				}
			}
			continue
		}
		b := block{kind: blockOther}
		switch {
		case first.text == "group":
			b.kind = blockGroup
		case first.text == "source":
			b.kind = blockSource
			b.source, _ = sourceArg(s[1:doAt])
		case first.text == "path":
			b.kind = blockPath
		case gitKeys[first.text] || gitSources[first.text]:
			b.kind = blockGit
		}
		stack = append(stack, b)
	}
	return f
}

// blockDo returns the index of the `do` that opens a block at the end of s (`… do` or
// `… do |x|`), or -1.
func blockDo(s []token) int {
	i := len(s) - 1
	if s[i].kind == tPunct && s[i].text == "|" { // block parameters
		for i--; i >= 0 && !(s[i].kind == tPunct && s[i].text == "|"); i-- {
		}
		i--
	}
	if i > 0 && s[i].kind == tIdent && s[i].text == "do" {
		return i
	}
	return -1
}

// firstSymbol returns the first symbol of args (`git_source(:github)`).
func firstSymbol(args []token) (string, bool) {
	for _, t := range args {
		switch {
		case t.kind == tSymbol:
			return t.text, true
		case t.kind != tPunct || t.text != "(":
			return "", false
		}
	}
	return "", false
}

// sourceArg returns the URL of a source statement's first argument: a literal string, or the
// legacy `:rubygems`/`:gemcutter` symbols for rubygems.org.
func sourceArg(args []token) (string, bool) {
	if len(args) > 0 && args[0].kind == tPunct && args[0].text == "(" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", false
	}
	switch t := args[0]; {
	case t.kind == tString && t.literal && t.text != "":
		return t.text, true
	case t.kind == tSymbol && (t.text == "rubygems" || t.text == "gemcutter"):
		return defaultSource, true
	}
	return "", false
}

// callArgs returns the argument tokens of the call statement s (after its method name) and
// whether they are parenthesized (`gem("x")`, the parenthesis right after the name); the
// opening parenthesis is dropped.
func callArgs(s []token) ([]token, bool) {
	args := s[1:]
	paren := len(args) > 0 && args[0].kind == tPunct && args[0].text == "(" && args[0].start == s[0].end
	if paren {
		args = args[1:]
	}
	return args, paren
}

// parseRubyDirective parses `ruby <requirements…>, <options…>` (s starts with `ruby`). A
// `file:` option, an engine other than `ruby` and a `patchlevel:` make it not updatable here.
func parseRubyDirective(s []token) rubyDecl {
	args, paren := callArgs(s)
	r := rubyDecl{line: s[0].line}
	for _, arg := range splitArgs(args, paren) {
		if !isKeyword(arg) {
			reqs, ok := literalStrings(arg)
			r.reqs = append(r.reqs, reqs...)
			r.nonLiteral = r.nonLiteral || !ok
			continue
		}
		key, value := keyword(arg)
		literal := len(value) == 1 && (value[0].kind == tString && value[0].literal || value[0].kind == tSymbol)
		switch key {
		case "file":
			r.skip = skipRubyFile
			if literal && value[0].kind == tString {
				r.file = value[0].text
			}
		case "engine":
			if (!literal || value[0].text != "ruby") && r.skip == "" {
				r.skip = skipRubyEngine
			}
		case "patchlevel":
			if r.skip == "" {
				r.skip = skipRubyPatchlevel
			}
		}
	}
	return r
}

// parseGem parses `gem "name", <requirements…>, <options…>` (s starts with `gem`). ok is false
// when the name is not a literal string.
func parseGem(s []token, gitSources map[string]bool) (gemDecl, bool) {
	args, paren := callArgs(s)
	var g gemDecl
	keywords := false
	for i, arg := range splitArgs(args, paren) {
		switch {
		case i == 0:
			if len(arg) != 1 || arg[0].kind != tString || !arg[0].literal || arg[0].text == "" {
				return gemDecl{}, false
			}
			g.name, g.nameEnd, g.line = arg[0].text, arg[0].end, s[0].line
			g.reqStart, g.reqEnd = g.nameEnd, g.nameEnd
			continue
		case !keywords && isKeyword(arg):
			keywords = true
		}
		if !keywords {
			if len(g.reqs) == 0 && !g.nonLiteral {
				g.reqStart = arg[0].start
			}
			g.reqEnd = arg[len(arg)-1].end
			reqs, ok := literalStrings(arg)
			g.reqs = append(g.reqs, reqs...)
			g.nonLiteral = g.nonLiteral || !ok
			continue
		}
		key, value := keyword(arg)
		switch {
		case gitKeys[key] || gitSources[key]:
			g.skip = skipGit
		case key == "path" && g.skip == "":
			g.skip = skipPath
		case key == "source":
			g.ownSource = true
			if url, ok := sourceArg(value); ok && len(value) == 1 {
				g.source = url
			} else if g.skip == "" {
				g.skip = skipNonLiteralSource
			}
		case key == "group" || key == "groups":
			g.group = true
		}
	}
	return g, g.name != ""
}

// splitArgs splits the arguments of a call at the commas outside brackets, up to the closing
// parenthesis of a parenthesized call or a modifier `if`/`unless`.
func splitArgs(args []token, paren bool) [][]token {
	var out [][]token
	depth, start := 0, 0
	for i, t := range args {
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			if depth == 0 && paren && t.text == ")" {
				return appendArg(out, args[start:i])
			}
			depth = max(depth-1, 0)
		case depth == 0 && t.kind == tPunct && t.text == ",":
			out = appendArg(out, args[start:i])
			start = i + 1
		case depth == 0 && t.kind == tIdent && (t.text == "if" || t.text == "unless"):
			return appendArg(out, args[start:i])
		}
	}
	return appendArg(out, args[start:])
}

func appendArg(out [][]token, arg []token) [][]token {
	if len(arg) == 0 {
		return out
	}
	return append(out, arg)
}

// isKeyword reports whether arg is a hash pair: `key: value`, `:key => value` or
// `"key" => value`.
func isKeyword(arg []token) bool {
	return arg[0].kind == tLabel || len(arg) > 1 && arg[1].kind == tPunct && arg[1].text == "=>"
}

// keyword returns the key and value of a hash pair.
func keyword(arg []token) (string, []token) {
	if arg[0].kind == tLabel {
		return arg[0].text, arg[1:]
	}
	if arg[0].kind == tSymbol || arg[0].kind == tString || arg[0].kind == tIdent {
		return arg[0].text, arg[2:]
	}
	return "", nil
}

// literalStrings returns the strings of a requirement argument: a literal string, or an array
// of them; ok is false for anything else.
func literalStrings(arg []token) ([]token, bool) {
	if len(arg) == 1 && arg[0].kind == tString {
		return arg, arg[0].literal
	}
	if len(arg) < 2 || arg[0].text != "[" || arg[len(arg)-1].text != "]" {
		return nil, false
	}
	var out []token
	for i, t := range arg[1 : len(arg)-1] {
		switch {
		case i%2 == 0 && t.kind == tString && t.literal:
			out = append(out, t)
		case i%2 == 1 && t.kind == tPunct && t.text == ",":
		default:
			return nil, false
		}
	}
	return out, len(out) > 0
}
