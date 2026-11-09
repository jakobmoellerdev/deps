// Package kotlinscript extracts the Maven dependencies of Kotlin scripts (`*.main.kts`):
// the coordinates of `@file:DependsOn(…)` annotations, looked up in the repositories of
// `@file:Repository(…)` and Maven Central.
package kotlinscript

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "kotlin-script"

// DepTypeDependencies is the dep type of the `@file:DependsOn` coordinates.
const DepTypeDependencies = "dependencies"

// Skip reasons.
const (
	skipTemplate = "string template"
	skipRange    = "version range"
	skipVersion  = "unsupported version "
)

const suffix = ".main.kts"

// annotationRe matches the start of a `@file:DependsOn(` or `@file:Repository(` annotation,
// also with a qualified name (`@file:kotlin.script.experimental.dependencies.DependsOn(`).
var annotationRe = regexp.MustCompile(`@file\s*:\s*(?:[A-Za-z_]\w*\s*\.\s*)*(DependsOn|Repository)\s*\(`)

type kotlinScript struct{}

// New returns the kotlin-script manager.
func New() manager.Manager { return kotlinScript{} }

func (kotlinScript) Name() string { return Name }

// Match reports whether the slash path is a `*.main.kts` (Renovate's `/^.+\.main\.kts$/`).
func (kotlinScript) Match(p string) bool {
	return len(p) > len(suffix) && strings.HasSuffix(p, suffix)
}

// Edits replaces the version of the coordinate string.
func (kotlinScript) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// literal is a string literal of an annotation's arguments: its content (between the quotes)
// in the source.
type literal struct{ start, end int }

// Extract returns one dep per Maven coordinate string of the `@file:DependsOn` annotations
// outside comments and strings. Like Kotlin's resolver, every dep is looked up in the
// repositories of all `@file:Repository` annotations of the file (only the http(s) ones can be
// queried), in file order, and in Maven Central only when there are none.
func (kotlinScript) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	code := blank(src)
	var (
		repos []string
		lits  []literal
	)
	for _, m := range annotationRe.FindAllSubmatchIndex(code, -1) {
		args := arguments(code, m[1])
		if string(code[m[2]:m[3]]) == "Repository" {
			for _, l := range args {
				repos = appendRepo(repos, string(src[l.start:l.end]))
			}
			continue
		}
		lits = append(lits, args...)
	}
	source := strings.Join(repos, " ")
	var deps []dep.Dep
	pos, line := 0, 1
	for _, l := range lits {
		d, ok := coordinate(src, l)
		if !ok {
			continue
		}
		line += bytes.Count(src[pos:l.start], []byte{'\n'})
		pos = l.start
		d.File, d.Line, d.Source = file, line, source
		deps = append(deps, d)
	}
	return deps, nil
}

// coordinate returns the dep of the coordinate string l, `group:artifact:version`, also with
// Maven's extension and classifier (`group:artifact:extension[:classifier]:version`); ok is
// false for strings that are no Maven coordinate (a jar path, missing parts).
func coordinate(src []byte, l literal) (dep.Dep, bool) {
	s := string(src[l.start:l.end])
	parts := strings.Split(s, ":")
	n := len(parts)
	if n < 3 || n > 5 || parts[0] == "" || parts[1] == "" || parts[n-1] == "" || n == 4 && parts[2] == "" || strings.ContainsAny(s, " \t\n") {
		return dep.Dep{}, false
	}
	v := parts[n-1]
	d := dep.Dep{
		Manager: Name, Datasource: "maven", Versioning: "maven", DepType: DepTypeDependencies,
		Name: parts[0] + ":" + parts[1], Version: v,
	}
	switch {
	case strings.ContainsAny(s, `$\`):
		d.Skip = skipTemplate
	case versioning.IsMavenRange(v):
		d.Skip = skipRange
	case v == "LATEST" || v == "RELEASE":
		d.Skip = skipVersion + v
	default:
		d.RefSpan = dep.Span{Start: l.end - len(v), End: l.end}
	}
	return d, true
}

// appendRepo appends the repository URL u (with a trailing slash) unless it is no http(s)
// URL (a local `file://` repository, an option string) or already listed.
func appendRepo(repos []string, u string) []string {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || strings.ContainsAny(u, "$\\ ") {
		return repos
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	if slices.Contains(repos, u) {
		return repos
	}
	return append(repos, u)
}

// arguments returns the string literals of the argument list of code starting after its
// opening parenthesis at i, up to the matching closing one. code has comments and string
// contents blanked (blank), so parentheses inside them do not count.
func arguments(code []byte, i int) []literal {
	var lits []literal
	depth := 1
	for ; i < len(code); i++ {
		switch code[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return lits
			}
		case '"':
			if i+2 < len(code) && code[i+1] == '"' && code[i+2] == '"' {
				end := bytes.Index(code[i+3:], []byte(`"""`))
				if end < 0 {
					return lits
				}
				lits = append(lits, literal{i + 3, i + 3 + end})
				i += end + 5
				continue
			}
			end := bytes.IndexByte(code[i+1:], '"')
			if end < 0 {
				return lits
			}
			lits = append(lits, literal{i + 1, i + 1 + end})
			i += end + 1
		}
	}
	return lits
}

// blank returns a copy of src with comments (line and nested block comments) and the
// contents of string and character literals replaced by spaces, newlines kept, so offsets
// and lines stay those of src and quotes delimit every literal.
func blank(src []byte) []byte {
	b := bytes.Clone(src)
	fill := func(from, to int) {
		for k := from; k < to; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	for i := 0; i < len(b); {
		switch {
		case bytes.HasPrefix(b[i:], []byte("//")):
			end := bytes.IndexByte(b[i:], '\n')
			if end < 0 {
				end = len(b) - i
			}
			fill(i, i+end)
			i += end
		case bytes.HasPrefix(b[i:], []byte("/*")):
			j, depth := i+2, 1
			for j < len(b) && depth > 0 {
				switch {
				case bytes.HasPrefix(b[j:], []byte("/*")):
					depth++
					j += 2
				case bytes.HasPrefix(b[j:], []byte("*/")):
					depth--
					j += 2
				default:
					j++
				}
			}
			fill(i, j)
			i = j
		case bytes.HasPrefix(b[i:], []byte(`"""`)):
			end := bytes.Index(b[i+3:], []byte(`"""`))
			if end < 0 {
				fill(i+3, len(b))
				return b
			}
			// A raw string ends at the last quote of a run (`""""` ends with a quote).
			j := i + 3 + end
			for j+3 < len(b) && b[j+3] == '"' {
				j++
			}
			fill(i+3, j)
			i = j + 3
		case b[i] == '"' || b[i] == '\'':
			q, j := b[i], i+1
			for j < len(b) && b[j] != q && b[j] != '\n' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j, len(b))
			fill(i+1, j)
			i = j + 1
		default:
			i++
		}
	}
	return b
}
