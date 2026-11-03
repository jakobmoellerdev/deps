package docker

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// directiveRe matches a parser directive such as `# escape=` + "`".
var directiveRe = regexp.MustCompile(`^#[ \t]*([a-zA-Z][a-zA-Z0-9]*)[ \t]*=[ \t]*(\S+)[ \t]*\r?$`)

type token struct{ start, end int }

// arg is a resolvable ARG default: the literal value at byte offset off, or skip when the
// default is itself variable or quoted with escapes.
type arg struct {
	val  string
	off  int
	skip bool
}

// stageState tracks the names in scope while walking a Dockerfile.
type stageState struct {
	stages  map[string]bool // earlier stage names, lowercased
	globals map[string]arg  // ARGs before the first FROM
	args    map[string]arg  // ARGs of the current stage; nil before the first FROM
	used    map[int]bool    // ARG defaults already extracted
}

// dockerfile extracts the images of FROM, `COPY --from=` and `RUN --mount=...,from=`.
// Instructions are case-insensitive and may span lines with the escape character (`\`, or
// the `# escape=` directive's). scratch, stage names and stage indexes are not dependencies.
// `$X`/`${X}` resolve to the default of `ARG X=<image>` in scope, extracted at the ARG.
func (x *extractor) dockerfile() {
	src := x.src
	esc := byte('\\')
	directives := true
	st := stageState{stages: map[string]bool{}, globals: map[string]arg{}, used: map[int]bool{}}
	var toks []token
	for i := 0; i < len(src); {
		le := lineEnd(src, i)
		s := skipBlank(src, i, le)
		if directives {
			if m := directiveRe.FindSubmatch(src[s:le]); m != nil {
				if strings.EqualFold(string(m[1]), "escape") && len(m[2]) == 1 && (m[2][0] == '\\' || m[2][0] == '`') {
					esc = m[2][0]
				}
				i = le + 1
				continue
			}
			directives = false
		}
		if isBlankOrComment(src, s, le) {
			i = le + 1
			continue
		}
		toks, i = x.instruction(toks[:0], s, esc)
		if len(toks) < 2 {
			continue
		}
		switch kw := x.text(toks[0]); {
		case strings.EqualFold(kw, "FROM"):
			st.args = map[string]arg{}
			x.from(toks[1:], &st)
		case strings.EqualFold(kw, "ARG"):
			x.arg(toks[1:], &st)
		case strings.EqualFold(kw, "COPY"):
			x.flagImages(toks[1:], "--from=", &st)
		case strings.EqualFold(kw, "RUN"):
			x.flagImages(toks[1:], "--mount=", &st)
		}
	}
}

// from handles the arguments of one FROM: `[--flag=...] <image> [AS <name>]`.
func (x *extractor) from(args []token, st *stageState) {
	for len(args) > 0 && strings.HasPrefix(x.text(args[0]), "--") {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	image := x.text(args[0])
	if !x.variable(image, st) {
		lower := strings.ToLower(image)
		if lower != "scratch" && !st.stages[lower] {
			x.add(TypeDockerfile, x.lineOf(args[0].start), args[0].start, image)
		}
	}
	if len(args) >= 3 && strings.EqualFold(x.text(args[1]), "AS") {
		st.stages[strings.ToLower(x.text(args[2]))] = true
	}
}

// flagImages extracts `--from=<image>` (COPY) or the `from=` field of `--mount=` (RUN).
func (x *extractor) flagImages(args []token, flag string, st *stageState) {
	for _, t := range args {
		s := x.text(t)
		if !strings.HasPrefix(s, "--") {
			return
		}
		if !strings.HasPrefix(s, flag) {
			continue
		}
		off := t.start + len(flag)
		v := s[len(flag):]
		if flag == "--mount=" {
			v = ""
			for f := range strings.SplitSeq(s[len(flag):], ",") {
				if strings.HasPrefix(f, "from=") {
					v = f[len("from="):]
					break
				}
				off += len(f) + 1
			}
			off += len("from=")
		}
		lower := strings.ToLower(v)
		switch {
		case v == "", lower == "scratch", st.stages[lower], isIndex(v), x.variable(v, st), strings.Contains(v, "$"):
		default:
			x.add(TypeDockerfile, x.lineOf(off), off, v)
		}
	}
}

// variable handles a reference that is exactly `$X` or `${X}` with X an ARG in scope, and
// reports whether it did. The dep is extracted once per ARG at its default value.
func (x *extractor) variable(ref string, st *stageState) bool {
	name := varName(ref)
	if name == "" {
		return false
	}
	a, ok := st.args[name]
	if !ok {
		a, ok = st.globals[name]
	}
	if !ok {
		return false
	}
	if st.used[a.off] {
		return true
	}
	st.used[a.off] = true
	if a.skip {
		x.deps = append(x.deps, dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeDockerfile,
			File: x.file, Line: x.lineOf(a.off), Name: ref, Skip: "variable reference"})
		return true
	}
	if lower := strings.ToLower(a.val); lower != "scratch" && !st.stages[lower] && !isIndex(a.val) {
		x.add(TypeDockerfile, x.lineOf(a.off), a.off, a.val)
	}
	return true
}

// arg records `ARG NAME[=default] ...`. A stage ARG without default inherits the global one.
func (x *extractor) arg(args []token, st *stageState) {
	scope := st.globals
	if st.args != nil {
		scope = st.args
	}
	for _, t := range args {
		s := x.text(t)
		name, val, hasVal := strings.Cut(s, "=")
		if !hasVal {
			if g, ok := st.globals[name]; ok && st.args != nil {
				scope[name] = g
			}
			continue
		}
		a := arg{val: val, off: t.start + len(name) + 1}
		if n := len(val); n > 0 && (val[0] == '"' || val[0] == '\'') {
			if n < 2 || val[n-1] != val[0] {
				a.skip = true
			} else {
				a.val, a.off = val[1:n-1], a.off+1
			}
		}
		if a.val == "" || strings.ContainsAny(a.val, `$\"'`) {
			a.skip = true
		}
		scope[name] = a
	}
}

// varName returns X for `$X` or `${X}`, else "".
func varName(s string) string {
	switch {
	case strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}"):
		s = s[2 : len(s)-1]
	case strings.HasPrefix(s, "$"):
		s = s[1:]
	default:
		return ""
	}
	if s == "" {
		return ""
	}
	for i := range len(s) {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || c < '0' || c > '9') {
			return ""
		}
	}
	return s
}

func isIndex(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// instruction splits the logical line starting at i into whitespace-separated tokens,
// following line continuations; comment and blank lines inside a continuation are skipped.
// It returns the tokens and the offset after the logical line.
func (x *extractor) instruction(toks []token, i int, esc byte) ([]token, int) {
	src := x.src
	start := -1
	flush := func(end int) {
		if start >= 0 {
			toks = append(toks, token{start, end})
			start = -1
		}
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			flush(i)
			return toks, i + 1
		case c == esc && continuation(src, i+1):
			flush(i)
			i = lineEnd(src, i) + 1
			for i < len(src) {
				le := lineEnd(src, i)
				if !isBlankOrComment(src, skipBlank(src, i, le), le) {
					break
				}
				i = le + 1
			}
			continue
		case c == ' ' || c == '\t' || c == '\r':
			flush(i)
		case start < 0:
			start = i
		}
		i++
	}
	flush(i)
	return toks, i
}

func (x *extractor) text(t token) string { return string(x.src[t.start:t.end]) }

// continuation reports whether only blanks follow offset i up to the end of the line.
func continuation(src []byte, i int) bool {
	for ; i < len(src) && src[i] != '\n'; i++ {
		if c := src[i]; c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

// lineEnd returns the offset of the '\n' ending the line containing i, or len(src).
func lineEnd(src []byte, i int) int {
	for ; i < len(src) && src[i] != '\n'; i++ {
	}
	return i
}

func skipBlank(src []byte, i, le int) int {
	for i < le && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return i
}

func isBlankOrComment(src []byte, s, le int) bool {
	return s == le || src[s] == '#' || src[s] == '\r' && s+1 == le
}
