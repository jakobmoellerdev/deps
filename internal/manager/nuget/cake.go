package nuget

import (
	"bytes"
	"net/url"
	"path"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// CakeName is the cake manager's name (Renovate's).
const CakeName = "cake"

// Cake dep types: the preprocessor directive (`#l` is `#load`); InstallTool and InstallTools
// arguments are `tool`, as they install tools like `#tool`.
const (
	DepTypeAddin  = "addin"
	DepTypeTool   = "tool"
	DepTypeModule = "module"
	DepTypeLoad   = "load"
)

// skipCakeSource is the skip of a package URI whose source is neither empty nor http(s).
const skipCakeSource = "unsupported url"

// cakeDirectives maps the Cake preprocessor directives that take a package URI to dep types.
var cakeDirectives = map[string]string{"addin": DepTypeAddin, "tool": DepTypeTool, "module": DepTypeModule, "load": DepTypeLoad, "l": DepTypeLoad}

type cakeManager struct {
	rangeEditor
	configs configSet
}

// visitFunc receives a package URI, its dep type, byte offset and line.
type visitFunc func(depType, uri string, off, line int)

// NewCake returns the cake manager.
func NewCake() manager.Manager { return &cakeManager{} }

func (*cakeManager) Name() string { return CakeName }

// Match reports whether the slash path is a Cake script (`*.cake`) or a `nuget.config` (any
// case), whose package sources apply to packages without a source of their own.
func (*cakeManager) Match(p string) bool {
	return strings.HasSuffix(p, ".cake") || isConfig(p)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (*cakeManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".cake", ".config"}}
}

// Extract returns one dep per package URI (`nuget:` or `dotnet:`) of a `#addin`, `#tool`,
// `#module`, `#load` or `#l` directive (quoted or not) and per such string argument of an
// InstallTool or InstallTools call, outside comments and strings. A nuget.config yields no
// deps: Link applies its sources.
func (m *cakeManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if isConfig(file) {
		return nil, m.configs.add(file, src)
	}
	var deps []dep.Dep
	scanCake(src, func(depType, uri string, off, line int) {
		if d, ok := cakeDep(uri, off); ok {
			d.DepType, d.File, d.Line = depType, file, line
			deps = append(deps, d)
		}
	})
	return deps, nil
}

// Link implements manager.Linker: packages without a source in their URI get the package
// sources of the nuget.config files above their script (applySources). Then it forgets the
// run's configurations.
func (m *cakeManager) Link(files [][]dep.Dep) {
	configs := m.configs.take()
	for _, deps := range files {
		if len(deps) > 0 {
			applySources(configs, path.Dir(deps[0].File), deps)
		}
	}
}

// cakeDep parses a package URI `<scheme>:[<source>]?package=<id>[&version=<v>]…` found at
// byte offset off. It reports false for a URI without a package id.
func cakeDep(uri string, off int) (dep.Dep, bool) {
	_, rest, _ := strings.Cut(uri, ":")
	source, query, ok := strings.Cut(rest, "?")
	if !ok {
		return dep.Dep{}, false
	}
	var id, version string
	var vspan dep.Span
	hasVersion := false
	poff := off + len(uri) - len(query) // offset of the current parameter in the file
	for p := range strings.SplitSeq(query, "&") {
		k, v, _ := strings.Cut(p, "=")
		switch {
		case strings.EqualFold(k, "package") && id == "":
			id, _ = url.QueryUnescape(v)
		case strings.EqualFold(k, "version") && !hasVersion:
			hasVersion = true
			version = v
			if u, err := url.QueryUnescape(v); err == nil {
				version = u
			}
			if version == v {
				vspan = dep.Span{Start: poff + len(k) + 1, End: poff + len(p)}
			} // else escaped: the bytes are not the version
		}
		poff += len(p) + 1
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return dep.Dep{}, false
	}
	d := dep.Dep{Manager: CakeName, Datasource: dsNuGet, Versioning: scheme, Name: id}
	if hasVersion {
		setVersion(&d, version, vspan)
	} else {
		d.Skip = skipNoVersion
	}
	if source != "" {
		u, err := url.Parse(source)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			d.Skip = skipCakeSource
		} else {
			d.Source = source
		}
	}
	return d, true
}

// scanCake calls visit with every package URI of src: the argument of a package directive at
// the start of a line and the string arguments of InstallTool/InstallTools calls. Comments,
// strings and character literals are skipped as C# lexes them.
func scanCake(src []byte, visit visitFunc) {
	line, bol := 1, true
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line, bol = line+1, true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '#' && bol:
			i = cakeDirective(src, i, line, visit)
		case c == '/' && i+1 < len(src) && (src[i+1] == '/' || src[i+1] == '*'):
			var n int
			i, n = skipComment(src, i)
			line += n
		case isStringStart(src, i):
			var n int
			_, n, i = csString(src, i)
			line += n
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			if name := src[i:j]; string(name) == "InstallTool" || string(name) == "InstallTools" {
				j = installTools(src, j, &line, visit)
			}
			i = j
		default:
			i++
		}
		bol = false
	}
}

// cakeDirective handles the directive starting at src[i] ('#') on line: a package directive
// whose argument (quoted or not) is a `nuget:` or `dotnet:` URI is visited. It returns the
// offset of the line's end.
func cakeDirective(src []byte, i, line int, visit visitFunc) int {
	end := lineEnd(src, i)
	j := i + 1
	for j < end && isIdent(src[j]) {
		j++
	}
	depType, ok := cakeDirectives[string(src[i+1:j])]
	if !ok || j == end || (src[j] != ' ' && src[j] != '\t') {
		return end
	}
	for j < end && (src[j] == ' ' || src[j] == '\t') {
		j++
	}
	arg := bytes.TrimRight(src[j:end], " \t\r")
	if len(arg) > 0 && arg[0] == '"' {
		q := bytes.IndexByte(arg[1:], '"')
		if q < 0 || q+2 != len(arg) {
			return end
		}
		arg, j = arg[1:q+1], j+1
	} else if sp := bytes.IndexAny(arg, " \t"); sp >= 0 {
		arg = arg[:sp]
	}
	if isPackageURI(arg) {
		visit(depType, string(arg), j, line)
	}
	return end
}

// installTools visits the package URI string arguments of the InstallTool(s) call whose name
// ends at src[i], up to its closing parenthesis, counting lines in *line. It returns the offset
// after the call, or i when no argument list follows.
func installTools(src []byte, i int, line *int, visit visitFunc) int {
	j := i
	for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
		j++
	}
	if j == len(src) || src[j] != '(' {
		return i
	}
	*line += bytes.Count(src[i:j], []byte{'\n'})
	depth := 0
	for j < len(src) {
		switch c := src[j]; {
		case c == '(':
			depth++
			j++
		case c == ')':
			depth--
			j++
			if depth == 0 {
				return j
			}
		case c == '\n':
			*line++
			j++
		case c == '/' && j+1 < len(src) && (src[j+1] == '/' || src[j+1] == '*'):
			var n int
			j, n = skipComment(src, j)
			*line += n
		case isStringStart(src, j):
			start, l := j, *line
			body, n, end := csString(src, j)
			*line += n
			// only a plain "…" literal without escapes is the URI as written
			if c == '"' && body == start+1 && end-1 > body && src[end-1] == '"' {
				if s := src[body : end-1]; isPackageURI(s) && bytes.IndexByte(s, '\\') < 0 {
					visit(DepTypeTool, string(s), body, l)
				}
			}
			j = end
		default:
			j++
		}
	}
	return j
}

// isPackageURI reports whether s is a NuGet package URI as Cake installs them.
func isPackageURI(s []byte) bool {
	return bytes.HasPrefix(s, []byte("nuget:")) || bytes.HasPrefix(s, []byte("dotnet:"))
}

// skipComment skips the `//` or `/* */` comment at src[i]; it returns the offset after it (a
// line comment ends before its newline) and the number of newlines skipped.
func skipComment(src []byte, i int) (end, newlines int) {
	if src[i+1] == '/' {
		return lineEnd(src, i), 0
	}
	e := bytes.Index(src[i+2:], []byte("*/"))
	if e < 0 {
		return len(src), bytes.Count(src[i:], []byte{'\n'})
	}
	end = i + 2 + e + 2
	return end, bytes.Count(src[i:end], []byte{'\n'})
}

// isStringStart reports whether a C# string or character literal starts at src[i]: a quote, or
// `@`/`$` prefixes followed by one.
func isStringStart(src []byte, i int) bool {
	j := i
	for j < len(src) && j-i < 3 && (src[j] == '$' || src[j] == '@') {
		j++
	}
	return j < len(src) && (src[j] == '"' || src[j] == '\'' && j == i)
}

// csString skips the C# string or character literal starting at src[i] (isStringStart: `"…"`,
// `'…'`, verbatim `@"…"`, raw `"""…"""`, interpolated `$"…"`, `$@"…"`, `@$"…"`; holes are not
// lexed). It returns the offset of the literal's content, the number of newlines inside and
// the offset after it.
func csString(src []byte, i int) (body, newlines, end int) {
	j := i
	verbatim := false
	for src[j] == '$' || src[j] == '@' {
		verbatim = verbatim || src[j] == '@'
		j++
	}
	q := src[j]
	if q == '"' && j+2 < len(src) && src[j+1] == '"' && src[j+2] == '"' { // raw string
		n := 3
		for j+n < len(src) && src[j+n] == '"' {
			n++
		}
		e := bytes.Index(src[j+n:], bytes.Repeat([]byte{'"'}, n))
		if e < 0 {
			return j + n, bytes.Count(src[j:], []byte{'\n'}), len(src)
		}
		end = j + n + e + n
		return j + n, bytes.Count(src[j:end], []byte{'\n'}), end
	}
	body = j + 1
	for k := body; k < len(src); k++ {
		switch c := src[k]; {
		case c == '\\' && !verbatim:
			k++
		case c == '\n' && !verbatim:
			return body, 0, k // unterminated: the line ends it
		case c == q && verbatim && k+1 < len(src) && src[k+1] == q:
			k++
		case c == q:
			return body, bytes.Count(src[j:k], []byte{'\n'}), k + 1
		}
	}
	return body, bytes.Count(src[j:], []byte{'\n'}), len(src)
}

// lineEnd returns the offset of the '\n' ending the line of src[i], or len(src).
func lineEnd(src []byte, i int) int {
	if e := bytes.IndexByte(src[i:], '\n'); e >= 0 {
		return i + e
	}
	return len(src)
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }
