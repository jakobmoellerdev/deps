package terraform

import (
	"path"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// text is a string evaluated from an HCL value, with the runs of it that the extracted file
// writes verbatim, so a version inside it can still be edited.
type text struct {
	s     string
	segs  []textSeg
	local bool // derived from a path function (get_repo_root(), find_in_parent_folders(), …)
}

// textSeg records that s[at:at+n] are the bytes src[off:off+n] of the extracted file.
type textSeg struct{ at, off, n int }

// literalText is the text of the string literal v.
func literalText(v value) text {
	t := text{s: v.str}
	if v.span != (dep.Span{}) && v.str != "" {
		t.segs = []textSeg{{at: 0, off: v.span.Start, n: len(v.str)}}
	}
	return t
}

// lit returns t.s[i:i+n] as a string value whose span locates it in the extracted file, or
// with a zero span when no single run holds it (escapes, interpolated or included values).
func (t text) lit(i, n int) value {
	v := value{kind: kindString, str: t.s[i : i+n]}
	for _, g := range t.segs {
		if i >= g.at && i+n <= g.at+g.n {
			start := g.off + i - g.at
			v.span = dep.Span{Start: start, End: start + n}
			break
		}
	}
	return v
}

func (t *text) add(u text) {
	for _, g := range u.segs {
		g.at += len(t.s)
		t.segs = append(t.segs, g)
	}
	t.s += u.s
	t.local = t.local || u.local
}

// tgScope is a Terragrunt configuration file whose expressions are evaluated: its top-level
// locals and, for the extracted file, its labeled include blocks.
type tgScope struct {
	file     string // slash path relative to the scanned root
	src      []byte
	foreign  bool // another file than the extracted one: its bytes cannot be edited
	locals   map[string]value
	includes map[string]value // include label -> path expression
}

func newScope(file string, src []byte, b body, foreign bool) *tgScope {
	sc := &tgScope{file: file, src: src, foreign: foreign, locals: map[string]value{}}
	for _, blk := range b.blocks {
		switch {
		case blk.typ == "locals" && len(blk.labels) == 0:
			for _, a := range blk.body.attrs {
				sc.locals[a.name] = a.val
			}
		case blk.typ == "include" && len(blk.labels) == 1 && !foreign:
			for _, a := range blk.body.attrs {
				if a.name == "path" {
					if sc.includes == nil {
						sc.includes = map[string]value{}
					}
					sc.includes[blk.labels[0]] = a.val
				}
			}
		}
	}
	return sc
}

// tgEval evaluates the subset of Terragrunt expressions that module sources are built from:
// string templates, `local.<name>`, `include.<label>.locals.<name>` (read from the included
// file) and the path functions get_terragrunt_dir(), get_repo_root(),
// get_path_to_repo_root(), get_parent_terragrunt_dir(), find_in_parent_folders() and
// dirname(). Paths from path functions are absolute, rooted at the scanned root ("/").
type tgEval struct {
	exists   func(string) bool
	readFile func(string) ([]byte, error)
	included map[string]*tgScope // root-relative path -> parsed file; nil entry = unreadable
	depth    int
}

// maxEvalDepth bounds local references (cycles, deep chains).
const maxEvalDepth = 16

// eval evaluates the expression v of sc; ok is false when it uses anything else. A false ok
// may still report local (the expression is a path below the repository).
func (e *tgEval) eval(sc *tgScope, v value) (text, bool) {
	if v.kind == kindString {
		if sc.foreign {
			return text{s: v.str}, true
		}
		return literalText(v), true
	}
	if v.expr == (dep.Span{}) || e.depth >= maxEvalDepth {
		return text{}, false
	}
	e.depth++
	defer func() { e.depth-- }()
	p := exprParser{e: e, sc: sc, src: sc.src, pos: v.expr.Start, end: v.expr.End}
	t, ok := p.expr()
	p.skip()
	if p.pos != p.end {
		ok = false
	}
	return t, ok
}

// path returns the root-relative file an include path names; relative paths resolve against
// the directory of sc's file.
func (sc *tgScope) path(p string) (string, bool) {
	if strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(path.Clean(p), "/")
	} else {
		p = path.Join(path.Dir(sc.file), p)
	}
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// include returns the scope of the file include block label of sc names.
func (e *tgEval) include(sc *tgScope, label string) (*tgScope, bool) {
	pv, ok := sc.includes[label]
	if !ok || e.readFile == nil {
		return nil, false
	}
	pt, ok := e.eval(sc, pv)
	if !ok {
		return nil, false
	}
	file, ok := sc.path(pt.s)
	if !ok {
		return nil, false
	}
	if inc, seen := e.included[file]; seen {
		return inc, inc != nil
	}
	if e.included == nil {
		e.included = map[string]*tgScope{}
	}
	e.included[file] = nil
	src, err := e.readFile(file)
	if err != nil {
		return nil, false
	}
	b, err := parseHCL(src)
	if err != nil {
		return nil, false
	}
	inc := newScope(file, src, b, true)
	e.included[file] = inc
	return inc, true
}

// exprParser reads one expression of src[pos:end].
type exprParser struct {
	e        *tgEval
	sc       *tgScope
	src      []byte
	pos, end int
}

func (p *exprParser) peek() byte {
	if p.pos < p.end {
		return p.src[p.pos]
	}
	return 0
}

func (p *exprParser) at(i int) byte {
	if p.pos+i < p.end {
		return p.src[p.pos+i]
	}
	return 0
}

// skip skips blanks, newlines and comments.
func (p *exprParser) skip() {
	for p.pos < p.end {
		switch c := p.src[p.pos]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case c == '#' || c == '/' && p.at(1) == '/':
			for p.pos < p.end && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.at(1) == '*':
			i := strings.Index(string(p.src[p.pos+2:p.end]), "*/")
			if i < 0 {
				p.pos = p.end
				return
			}
			p.pos += i + 4
		default:
			return
		}
	}
}

func (p *exprParser) ident() string {
	start := p.pos
	if p.pos < p.end && isIdentStart(p.src[p.pos]) {
		for p.pos < p.end && isIdent(p.src[p.pos]) {
			p.pos++
		}
	}
	return string(p.src[start:p.pos])
}

func (p *exprParser) expr() (text, bool) {
	p.skip()
	if p.peek() == '"' {
		return p.template()
	}
	name := p.ident()
	if name == "" {
		return text{}, false
	}
	p.skip()
	if p.peek() == '(' {
		return p.call(name)
	}
	parts := []string{name}
	for p.peek() == '.' {
		p.pos++
		part := p.ident()
		if part == "" {
			return text{}, false
		}
		parts = append(parts, part)
		p.skip()
	}
	return p.traversal(parts)
}

// template reads a quoted template: literal runs are located in the extracted file unless
// they contain escapes.
func (p *exprParser) template() (text, bool) {
	p.pos++ // "
	var t text
	ok := true
	run := p.pos // start of the pending literal run
	flush := func() {
		if p.pos > run {
			lit := string(p.src[run:p.pos])
			if !p.sc.foreign {
				t.segs = append(t.segs, textSeg{at: len(t.s), off: run, n: len(lit)})
			}
			t.s += lit
		}
	}
	for p.pos < p.end {
		c := p.src[p.pos]
		switch {
		case c == '"':
			flush()
			p.pos++
			return t, ok
		case c == '\n':
			return t, false
		case c == '\\':
			flush()
			r, n := unescape(p.src[p.pos:p.end])
			if n == 0 {
				return t, false
			}
			t.s += string(r)
			p.pos += n
			run = p.pos
		case (c == '$' || c == '%') && p.at(1) == c && p.at(2) == '{':
			flush()
			t.s += string(c) + "{"
			p.pos += 3
			run = p.pos
		case c == '%' && p.at(1) == '{':
			return t, false // directives
		case c == '$' && p.at(1) == '{':
			flush()
			p.pos += 2
			u, uok := p.expr()
			t.add(u)
			p.skip()
			if !uok || p.peek() != '}' {
				return t, false
			}
			p.pos++
			run = p.pos
		default:
			p.pos++
		}
	}
	return t, false
}

// args reads the arguments of a call after its name.
func (p *exprParser) args() ([]text, bool) {
	p.pos++ // (
	var out []text
	ok := true
	for {
		p.skip()
		if p.peek() == ')' {
			p.pos++
			return out, ok
		}
		a, aok := p.expr()
		out, ok = append(out, a), ok && aok
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
		default:
			return out, false
		}
	}
}

func (p *exprParser) call(name string) (text, bool) {
	args, ok := p.args()
	local := text{local: true}
	// Directories are written without a trailing slash, the root as "", so that
	// "${get_repo_root()}/x" is "/x".
	dir := strings.TrimSuffix("/"+path.Dir(p.sc.file), "/.")
	switch name {
	case "get_terragrunt_dir":
		return text{s: dir, local: true}, ok && len(args) == 0
	case "get_repo_root":
		return text{s: "", local: true}, ok && len(args) == 0
	case "get_path_to_repo_root":
		up := "."
		if n := strings.Count(dir, "/"); n > 0 {
			up = strings.TrimSuffix(strings.Repeat("../", n), "/")
		}
		return text{s: up, local: true}, ok && len(args) == 0
	case "get_parent_terragrunt_dir":
		return local, false
	case "dirname":
		if !ok || len(args) != 1 {
			return text{local: anyLocal(args)}, false
		}
		return text{s: path.Dir(args[0].s), local: args[0].local}, true
	case "find_in_parent_folders":
		if !ok || len(args) > 2 || p.e.exists == nil {
			return local, false
		}
		name := "terragrunt.hcl"
		if len(args) > 0 {
			name = args[0].s
		}
		// Terragrunt starts in the parent of the configuration's directory.
		for d := path.Dir(p.sc.file); d != "."; {
			d = path.Dir(d)
			if f := path.Join(d, name); p.e.exists(f) {
				return text{s: "/" + f, local: true}, true
			}
		}
		if len(args) == 2 {
			fb := args[1]
			fb.local = true
			return fb, true
		}
		return local, false
	}
	return text{local: anyLocal(args)}, false
}

func anyLocal(ts []text) bool {
	for _, t := range ts {
		if t.local {
			return true
		}
	}
	return false
}

// traversal resolves `local.<name>` and `include.<label>.locals.<name>`.
func (p *exprParser) traversal(parts []string) (text, bool) {
	switch {
	case len(parts) == 2 && parts[0] == "local":
		v, ok := p.sc.locals[parts[1]]
		if !ok {
			return text{}, false
		}
		return p.e.eval(p.sc, v)
	case len(parts) == 4 && parts[0] == "include" && parts[2] == "locals":
		inc, ok := p.e.include(p.sc, parts[1])
		if !ok {
			return text{}, false
		}
		v, ok := inc.locals[parts[3]]
		if !ok {
			return text{}, false
		}
		return p.e.eval(inc, v)
	}
	return text{}, false
}
