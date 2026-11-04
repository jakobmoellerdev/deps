// Package pyreq parses Python dependency declarations shared by the Python managers: PEP 508
// requirement strings and the version specifiers of pip, Poetry and Pipenv, and builds the
// deps they turn into.
package pyreq

import (
	"bytes"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Datasource and Versioning of every Python dep.
const (
	Datasource = "pypi"
	Versioning = "pep440"
)

// Skip reasons shared by the Python managers.
const (
	SkipUnconstrained = "unconstrained"
	SkipRange         = "unsupported range"
	SkipURL           = "url dependency"
	SkipGit           = "git dependency"
	SkipLocal         = "local dependency"
	SkipLocate        = "cannot locate value"
)

// Requirement is a parsed PEP 508 requirement. Offsets are byte offsets into the parsed string.
type Requirement struct {
	Name               string
	NameStart          int
	Extras             string // without brackets, "" when absent
	Spec               string // version specifier without parentheses, trimmed; "" when absent
	SpecStart, SpecEnd int
	URL                string // `name @ <url>`
	Marker             string // environment marker after `;`, trimmed
}

// Parse parses a PEP 508 requirement string (`name[extras] specifier ; marker` or
// `name[extras] @ url ; marker`). ok is false when s does not start with a valid name.
func Parse(s string) (r Requirement, ok bool) {
	i := skipWS(s, 0)
	r.NameStart = i
	for i < len(s) && nameChar(s[i]) {
		i++
	}
	r.Name = s[r.NameStart:i]
	if !ValidName(r.Name) {
		return Requirement{}, false
	}
	i = skipWS(s, i)
	if i < len(s) && s[i] == '[' {
		end := strings.IndexByte(s[i:], ']')
		if end < 0 {
			return Requirement{}, false
		}
		r.Extras = strings.TrimSpace(s[i+1 : i+end])
		i = skipWS(s, i+end+1)
	}
	rest := s[i:]
	body, marker, _ := strings.Cut(rest, ";")
	r.Marker = strings.TrimSpace(marker)
	if u, isURL := strings.CutPrefix(body, "@"); isURL {
		if r.URL = strings.TrimSpace(u); r.URL == "" {
			return Requirement{}, false
		}
		return r, true
	}
	start, end := i, i+len(body)
	if t := strings.TrimSpace(body); strings.HasPrefix(t, "(") {
		if !strings.HasSuffix(t, ")") {
			return Requirement{}, false
		}
		open := strings.IndexByte(s[start:end], '(')
		start += open + 1
		end = start + strings.LastIndexByte(s[start:end], ')')
	}
	for start < end && isWS(s[start]) {
		start++
	}
	for end > start && isWS(s[end-1]) {
		end--
	}
	r.Spec, r.SpecStart, r.SpecEnd = s[start:end], start, end
	return r, true
}

// ValidName reports whether name is a valid Python project name (PEP 508).
func ValidName(name string) bool {
	if name == "" || !alnum(name[0]) || !alnum(name[len(name)-1]) {
		return false
	}
	for i := range len(name) {
		if !nameChar(name[i]) {
			return false
		}
	}
	return true
}

func alnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func nameChar(c byte) bool { return alnum(c) || c == '.' || c == '-' || c == '_' }

func isWS(c byte) bool { return c == ' ' || c == '\t' }

func skipWS(s string, i int) int {
	for i < len(s) && isWS(s[i]) {
		i++
	}
	return i
}

// Grammar selects the specifier syntax of a manifest.
type Grammar uint8

// Grammars.
const (
	// PEP440 is a PEP 440 specifier (requirements files, PEP 621).
	PEP440 Grammar = iota
	// Pipenv is a PEP 440 specifier in which a bare version means `==`.
	Pipenv
	// Poetry is a Poetry constraint: PEP 440 operators plus `^`, `~`, `=` and a bare version.
	Poetry
)

// ops are the comparison operators of each grammar, longest first.
var ops = [...][]string{
	PEP440: {"===", "==", "~=", "!=", "<=", ">=", "<", ">"},
	Pipenv: {"===", "==", "~=", "!=", "<=", ">=", "<", ">"},
	Poetry: {"===", "==", "~=", "!=", "<=", ">=", "^", "~", "=", "<", ">"},
}

// NewDep returns a Python dep of manager m without version.
func NewDep(m, depType, file, name string, line int) dep.Dep {
	return dep.Dep{Manager: m, Datasource: Datasource, Versioning: Versioning, DepType: depType, File: file, Line: line, Name: name}
}

// SetVersion sets d's range from the specifier spec of grammar g found at file offset at
// (spec is src[at:at+len(spec)] when exact) or the skip reason: Range is spec trimmed,
// RefSpan its bytes and Version its anchor version (see anchor).
func SetVersion(d *dep.Dep, spec string, at int, exact bool, g Grammar) {
	t := strings.TrimSpace(spec)
	ver, skip := anchor(t, g)
	switch {
	case skip != "":
		d.Version, d.Skip = t, skip
	case !exact:
		d.Skip = SkipLocate
	default:
		lead := strings.Index(spec, t)
		d.Version, d.Range = ver, t
		d.RefSpan = dep.Span{Start: at + lead, End: at + lead + len(t)}
	}
}

// URLSkip returns the skip reason of a direct URL reference.
func URLSkip(u string) string {
	switch {
	case HasAnyPrefix(u, "git+", "hg+", "svn+", "bzr+"):
		return SkipGit
	case HasAnyPrefix(u, "file:", "./", "../", "/", "~/", "."):
		return SkipLocal
	}
	return SkipURL
}

// HasAnyPrefix reports whether s starts with one of prefixes.
func HasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Line returns the 1-based line of offset off in src.
func Line(src []byte, off int) int {
	return bytes.Count(src[:off], []byte{'\n'}) + 1
}

// TableSpec reads a Poetry or Pipenv dependency table (`{version = "…", extras = […]}`): the
// version specifier value, or the skip reason of a git, path or url dependency or of a table
// without version.
func TableSpec(t *tomlspan.Value) (spec *tomlspan.Value, skip string) {
	switch {
	case t.Get("git") != nil:
		return nil, SkipGit
	case t.Get("path") != nil:
		return nil, SkipLocal
	case t.Get("url") != nil || t.Get("file") != nil:
		return nil, SkipURL
	}
	v := t.Get("version")
	if v == nil || v.Kind != tomlspan.String {
		return nil, SkipUnconstrained
	}
	return v, ""
}

// TOMLRequirement returns the dep of the PEP 508 requirement string item v (a TOML string).
func TOMLRequirement(m, depType, file string, v *tomlspan.Value) (dep.Dep, bool) {
	r, ok := Parse(v.Str)
	if !ok {
		return dep.Dep{}, false
	}
	d := NewDep(m, depType, file, r.Name, v.Line)
	if r.URL != "" {
		d.Skip = URLSkip(r.URL)
		return d, true
	}
	SetVersion(&d, r.Spec, v.Start+r.SpecStart, v.Exact, PEP440)
	return d, true
}

// Normalize returns the PEP 503 normalized form of a project name: lowercase, with runs of
// `-`, `_` and `.` replaced by one `-`.
func Normalize(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	sep := false
	for i := range len(name) {
		c := name[i]
		switch {
		case c == '-' || c == '_' || c == '.':
			sep = true
			continue
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		}
		if sep && b.Len() > 0 {
			b.WriteByte('-')
		}
		sep = false
		b.WriteByte(c)
	}
	return b.String()
}
