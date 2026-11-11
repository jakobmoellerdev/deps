package docker

import (
	"bytes"
	"regexp"
	"slices"
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
	// substituted are the deps already extracted by substitute, by name and edited bytes.
	substituted map[substKey]bool
	// apkRegistry is the apk registry of the current stage's base image (stageRegistry),
	// "" = unknown; registries are those of the named stages, by lowercased name.
	apkRegistry string
	registries  map[string]string
}

type substKey struct {
	name        string
	ref, digest dep.Span
	insertAt    int
}

// dockerfile extracts the images of FROM, `COPY --from=` and `RUN --mount=...,from=`, and the
// packages RUN pins with `apk add` and `apt-get install` (installs).
// Instructions are case-insensitive and may span lines with the escape character (`\`, or
// the `# escape=` directive's). scratch, stage names and stage indexes are not dependencies.
// `$X`/`${X}` resolve to the default of `ARG X=<image>` in scope, extracted at the ARG; other
// references with ARGs are substituted (substitute).
func (x *extractor) dockerfile() {
	src := x.src
	esc := byte('\\')
	directives := true
	st := stageState{stages: map[string]bool{}, globals: map[string]arg{}, used: map[int]bool{}, substituted: map[substKey]bool{}, registries: map[string]string{}}
	var toks []token
	// A leading byte order mark is no part of the first instruction; offsets stay those of src.
	i := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		i = 3
	}
	for i < len(src) {
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
			args := toks[1:]
			for len(args) > 0 && strings.HasPrefix(x.text(args[0]), "--") {
				args = args[1:]
			}
			if len(args) > 0 && !strings.HasPrefix(x.text(args[0]), "[") {
				x.installs(args, &st)
			}
		}
	}
}

// from handles the arguments of one FROM: `[--flag=...] <image> [AS <name>]`.
func (x *extractor) from(args []token, st *stageState) {
	platform := ""
	for len(args) > 0 && strings.HasPrefix(x.text(args[0]), "--") {
		if p, ok := strings.CutPrefix(x.text(args[0]), "--platform="); ok {
			platform = p
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	image := x.text(args[0])
	name := ""
	if len(args) >= 3 && strings.EqualFold(x.text(args[1]), "AS") {
		name = x.text(args[2])
	}
	x.stageRegistry(image, platform, name, st)
	if !x.variable(image, st) {
		lower := strings.ToLower(image)
		if lower != "scratch" && !st.stages[lower] && !(strings.Contains(image, "$") && x.substitute(args[0].start, image, st)) {
			x.add(TypeDockerfile, x.lineOf(args[0].start), args[0].start, image)
		}
	}
	if name != "" {
		st.stages[strings.ToLower(name)] = true
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
		case v == "", lower == "scratch", st.stages[lower], isIndex(v), x.variable(v, st):
		case strings.Contains(v, "$"):
			x.substitute(off, v, st) // else possibly a stage name: none
		default:
			x.add(TypeDockerfile, x.lineOf(off), off, v)
		}
	}
}

// variable handles a reference that is exactly `$X` or `${X}` with X an ARG in scope, and
// reports whether it did. The dep is extracted once per ARG at its default value.
func (x *extractor) variable(ref string, st *stageState) bool {
	a, ok := st.lookup(varName(ref))
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

// lookup returns the ARG name in scope: the stage's, else the global one.
func (st *stageState) lookup(name string) (arg, bool) {
	if name == "" {
		return arg{}, false
	}
	a, ok := st.args[name]
	if !ok {
		a, ok = st.globals[name]
	}
	return a, ok
}

// substitute extracts the reference ref written at off whose ARG references (`$X`, `${X}`)
// are not the whole reference (variable): `alpine:${ALPINE_VERSION}`, `${BASE}:3.22`,
// `${REGISTRY}/app:${APP_VERSION}`. Each reference to an ARG with a literal default in scope
// is replaced by the default; other variables stay as written, for a registry alias (Alias)
// to resolve. The tag and the digest must each be one run of source bytes, in the instruction
// or in one ARG default (then the dep's VersionRef is `ARG <name>`, so deps sharing it get one
// version); else, and with variables left, the dep is a "variable reference". A dep is
// extracted once per name and edited bytes. It reports whether ref had a substituted ARG.
func (x *extractor) substitute(off int, ref string, st *stageState) bool {
	var (
		b    strings.Builder
		pos  []int
		args []string // the substituted ARG names, in order
	)
	for i := 0; i < len(ref); {
		n, name := varRefAt(ref[i:])
		a, ok := st.lookup(name)
		if n == 0 || !ok || a.skip {
			n = max(n, 1)
			b.WriteString(ref[i : i+n])
			for j := range n {
				pos = append(pos, off+i+j)
			}
			i += n
			continue
		}
		b.WriteString(a.val)
		for j := range len(a.val) {
			pos = append(pos, a.off+j)
		}
		args = append(args, name)
		i += n
	}
	if len(args) == 0 {
		return false
	}
	resolved := b.String()
	if v, rest, ok := alias(resolved, x.aliases); ok {
		resolved = v + resolved[rest:]
		pos = append(slices.Repeat([]int{-1}, len(v)), pos[rest:]...)
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeDockerfile, File: x.file, Line: x.lineOf(off), Name: ref}
	if !strings.Contains(resolved, "$") {
		ParseRef(&d, 0, resolved)
	}
	switch {
	case strings.Contains(resolved, "$"), d.Skip == "" && !remap(&d, pos, 0):
		d = dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeDockerfile, File: x.file, Line: x.lineOf(off), Name: ref, Skip: skipVariable}
	case d.Skip == "":
		for _, name := range args {
			if a, _ := st.lookup(name); !Untagged(d) && a.off <= d.RefSpan.Start && d.RefSpan.End <= a.off+len(a.val) {
				d.VersionRef = "ARG " + name
			}
		}
		k := substKey{d.Name, d.RefSpan, d.DigestSpan, d.InsertAt}
		if st.substituted[k] {
			return true
		}
		st.substituted[k] = true
	}
	x.deps = append(x.deps, d)
	return true
}

// varRefAt returns the length and the name of the ARG reference `$X` or `${X}` at the start
// of s; 0 when there is none (`${X:-default}` and other modifiers are none).
func varRefAt(s string) (int, string) {
	if !strings.HasPrefix(s, "$") {
		return 0, ""
	}
	if rest, ok := strings.CutPrefix(s, "${"); ok {
		end := strings.IndexByte(rest, '}')
		if end < 0 || varNameLen(rest[:end]) != end {
			return 0, ""
		}
		return end + 3, rest[:end]
	}
	n := varNameLen(s[1:])
	if n == 0 {
		return 0, ""
	}
	return n + 1, s[1 : n+1]
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
