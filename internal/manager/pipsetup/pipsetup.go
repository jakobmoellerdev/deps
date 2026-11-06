// Package pipsetup extracts the requirement string literals of setup.py files.
package pipsetup

import (
	"cmp"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

// Name is the manager name (Renovate's).
const Name = "pip_setup"

// DepTypes: the setup() keyword argument a literal is written in, else DepTypeOther.
const (
	DepTypeInstall = "install"
	DepTypeSetup   = "setup"
	DepTypeTest    = "test"
	DepTypeExtra   = "extra"
	DepTypeOther   = "other"
	// SkipIgnored is the skip reason of a literal followed by a `# renovate: ignore` comment.
	SkipIgnored = "ignored"
)

var (
	matchRe  = regexp.MustCompile(`(^|/)setup\.py$`)
	ignoreRe = regexp.MustCompile(`^#\s*renovate\s*:\s*ignore\s*$`)
)

// keywords maps the setup() keyword arguments holding requirements to their dep types.
var keywords = map[string]string{
	"install_requires": DepTypeInstall,
	"setup_requires":   DepTypeSetup,
	"tests_require":    DepTypeTest,
	"extras_require":   DepTypeExtra,
}

type pipSetupManager struct{}

// New returns the pip_setup manager.
func New() manager.Manager { return pipSetupManager{} }

func (pipSetupManager) Name() string { return Name }

// Match reports whether the slash path is a setup.py file.
func (pipSetupManager) Match(p string) bool {
	return strings.HasSuffix(p, "setup.py") && matchRe.MatchString(p)
}

// Edits rewrites the range (pyreq.Edits).
func (pipSetupManager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (pipSetupManager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (pipSetupManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return pyreq.NewRange(d, v, strategy)
}

// Extract lexes src as Python and returns one dep per plain string literal that is a PEP 508
// requirement with a version specifier (`"requests>=2.31"`, `'django[argon2]~=5.0'`), as
// Renovate's pip_setup does: the code is not evaluated, so requirements built at runtime
// (concatenated, formatted, read from files) yield no deps.
func (pipSetupManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	lx := lexer{src: string(src), line: 1}
	var (
		deps    []dep.Dep
		depth   int    // bracket depth
		kwType  string // dep type of the keyword argument being read
		kwDepth = -1   // depth at which kwType was set
		ident   string // last identifier, for `name=`
	)
	for lx.i < len(lx.src) {
		c := lx.src[lx.i]
		switch {
		case c == '\n':
			lx.line++
			lx.i++
		case c == '#':
			lx.comment()
		case c == '(' || c == '[' || c == '{':
			depth++
			lx.i++
			ident = ""
		case c == ')' || c == ']' || c == '}':
			depth--
			lx.i++
			ident = ""
			if depth < kwDepth {
				kwType, kwDepth = "", -1
			}
		case c == '=' && !strings.HasPrefix(lx.src[lx.i:], "=="):
			if t, ok := keywords[ident]; ok {
				kwType, kwDepth = t, depth
			}
			lx.i++
			ident = ""
		case c == ',' && depth == kwDepth:
			kwType, kwDepth = "", -1
			lx.i++
		case c == '"' || c == '\'':
			if d, ok := lx.literal(file); ok {
				d.DepType = cmp.Or(kwType, DepTypeOther)
				deps = append(deps, d)
			}
			ident = ""
		case isIdent(c):
			s := lx.i
			for lx.i < len(lx.src) && isIdent(lx.src[lx.i]) {
				lx.i++
			}
			ident = lx.src[s:lx.i]
		default:
			if c != ' ' && c != '\t' && c != '\r' && c != '\\' {
				ident = ""
			}
			lx.i++
		}
	}
	return deps, nil
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

type lexer struct {
	src  string
	i    int
	line int
}

// comment skips the comment at i.
func (lx *lexer) comment() {
	j := strings.IndexByte(lx.src[lx.i:], '\n')
	if j < 0 {
		lx.i = len(lx.src)
		return
	}
	lx.i += j
}

// literal reads the string literal at i (its prefix is the identifier right before it) and returns
// its dep when it is a plain requirement with a specifier.
func (lx *lexer) literal(file string) (dep.Dep, bool) {
	q := lx.src[lx.i]
	line, start := lx.line, lx.i
	p := start
	for p > 0 && isIdent(lx.src[p-1]) {
		p--
	}
	prefix := lx.src[p:start]
	plain := !strings.ContainsAny(prefix, "fFbB") // f-strings and bytes are no requirements
	if prefix != "" && strings.Trim(prefix, "rRuUfFbB") != "" {
		plain = false // an identifier, not a prefix
	}
	if strings.HasPrefix(lx.src[lx.i:], strings.Repeat(string(q), 3)) {
		lx.i += 3
		for lx.i < len(lx.src) && !strings.HasPrefix(lx.src[lx.i:], strings.Repeat(string(q), 3)) {
			if lx.src[lx.i] == '\\' {
				lx.i++
			}
			if lx.i < len(lx.src) && lx.src[lx.i] == '\n' {
				lx.line++
			}
			lx.i++
		}
		lx.i = min(lx.i+3, len(lx.src))
		return dep.Dep{}, false
	}
	lx.i++
	contentStart := lx.i
	for lx.i < len(lx.src) && lx.src[lx.i] != q && lx.src[lx.i] != '\n' {
		if lx.src[lx.i] == '\\' {
			plain = false
			lx.i++
			if lx.i < len(lx.src) && lx.src[lx.i] == '\n' {
				lx.line++
			}
		}
		lx.i++
	}
	if lx.i >= len(lx.src) || lx.src[lx.i] != q {
		return dep.Dep{}, false
	}
	content := lx.src[contentStart:lx.i]
	lx.i++
	if !plain || concatenated(lx.src[:start-len(prefix)], lx.src[lx.i:]) {
		return dep.Dep{}, false
	}
	r, ok := pyreq.Parse(content)
	if !ok || r.URL != "" || r.Spec == "" || !strings.ContainsRune("<>=!~", rune(r.Spec[0])) || !isIdentStart(r.Name[0]) {
		return dep.Dep{}, false
	}
	d := pyreq.NewDep(Name, "", file, r.Name, line)
	pyreq.SetVersion(&d, r.Spec, contentStart+r.SpecStart, true, pyreq.PEP440)
	if d.Skip == pyreq.SkipRange {
		return dep.Dep{}, false // no version: an entry point (`name=module:func`), not a requirement
	}
	if lx.ignored() {
		d.Skip = SkipIgnored
	}
	return d, true
}

func isIdentStart(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// concatenated reports whether the literal between before and after is an operand of `+`,
// `*` or `%`, or the receiver of a method call (`"x>={}".format(v)`).
func concatenated(before, after string) bool {
	b := strings.TrimRight(before, " \t")
	a := strings.TrimLeft(after, " \t")
	return strings.HasSuffix(b, "+") || a != "" && strings.ContainsRune("+*%.", rune(a[0]))
}

// ignored reports whether the literal just read is followed, on its line and after an
// optional comma, by a `# renovate: ignore` comment.
func (lx *lexer) ignored() bool {
	rest := lx.src[lx.i:]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ","))
	return ignoreRe.MatchString(strings.TrimRight(rest, "\r"))
}
