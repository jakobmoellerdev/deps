package docker

import (
	"regexp"
	"strings"
)

// directiveRe matches a parser directive such as `# escape=` + "`".
var directiveRe = regexp.MustCompile(`^#[ \t]*([a-zA-Z][a-zA-Z0-9]*)[ \t]*=[ \t]*(\S+)[ \t]*\r?$`)

type token struct{ start, end int }

// dockerfile extracts the image of every FROM instruction. Instructions are case-insensitive
// and may span lines with the escape character (`\`, or the `# escape=` directive's).
// scratch and references to earlier build stages are not dependencies.
func (x *extractor) dockerfile() {
	src := x.src
	esc := byte('\\')
	directives := true
	stages := map[string]bool{}
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
		if len(toks) > 1 && strings.EqualFold(x.text(toks[0]), "FROM") {
			x.from(toks[1:], stages)
		}
	}
}

// from handles the arguments of one FROM: `[--flag=...] <image> [AS <name>]`.
func (x *extractor) from(args []token, stages map[string]bool) {
	for len(args) > 0 && strings.HasPrefix(x.text(args[0]), "--") {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	image := x.text(args[0])
	lower := strings.ToLower(image)
	skip := lower == "scratch" || stages[lower]
	if len(args) >= 3 && strings.EqualFold(x.text(args[1]), "AS") {
		stages[strings.ToLower(x.text(args[2]))] = true
	}
	if !skip {
		x.add(TypeDockerfile, x.lineOf(args[0].start), args[0].start, image)
	}
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
