// Package piprequirements extracts the requirements of pip requirements files.
package piprequirements

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

// Name is the manager name (Renovate's).
const Name = "pip_requirements"

// DepType of every requirement.
const DepType = "requirements"

// matchRe is Renovate's default pip_requirements file pattern.
var matchRe = regexp.MustCompile(`(^|/)[\w-]*requirements([-.]\w+)?\.(txt|pip)$`)

type pipManager struct{}

// New returns the pip_requirements manager.
func New() manager.Manager { return pipManager{} }

func (pipManager) Name() string { return Name }

// Match reports whether the slash path is a requirements file (`requirements.txt`,
// `requirements-dev.txt`, `dev-requirements.txt`, `requirements.pip`, …).
func (pipManager) Match(p string) bool { return matchRe.MatchString(p) }

// Edits rewrites the specifier (pyreq.Edits).
func (pipManager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (pipManager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (pipManager) NewRange(d dep.Dep, v string) (string, bool) { return pyreq.NewRange(d, v) }

// Extract returns one dep per requirement line. Backslash continuations are joined, comments
// dropped; option lines (`-r`, `-c`, `-e`, `--extra-index-url`, …) yield no deps, an
// `--index-url`/`-i` anywhere in the file becomes the Source of every dep and each
// `--extra-index-url` one of its ExtraSources. Requirements with `--hash` options are updated
// like the others; post-processing rewrites their hashes.
func (pipManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	reqs := parse(file, src)
	deps := make([]dep.Dep, len(reqs))
	for i, r := range reqs {
		deps[i] = r.Dep
	}
	return deps, nil
}

// Hashed is a requirement with `--hash` options.
type Hashed struct {
	Dep    dep.Dep    // as Extract returns it
	Hashes []dep.Span // the `--hash=…` options, in file order
}

// HashedRequirements returns the updatable requirements of src that have `--hash` options.
func HashedRequirements(file string, src []byte) []Hashed {
	var out []Hashed
	for _, r := range parse(file, src) {
		if r.Dep.Skip == "" && len(r.Hashes) > 0 {
			out = append(out, r)
		}
	}
	return out
}

func parse(file string, src []byte) []Hashed {
	lines := logicalLines(src)
	index, extras := "", []string(nil)
	for _, l := range lines {
		if u := indexURL(l.text); u != "" {
			index = u
		}
		if u := option(l.text, "--extra-index-url"); u != "" {
			extras = append(extras, u)
		}
	}
	var reqs []Hashed
	for _, l := range lines {
		text := l.text
		if text == "" || text[0] == '-' {
			continue
		}
		req, opts := splitOptions(text)
		r, ok := pyreq.Parse(req)
		var d dep.Dep
		switch skip := pathSkip(req); {
		case ok && r.URL != "":
			d = pyreq.NewDep(Name, DepType, file, r.Name, pyreq.Line(src, l.offset(r.NameStart)))
			d.Skip = pyreq.URLSkip(r.URL)
		case skip != "":
			d = pyreq.NewDep(Name, DepType, file, req, pyreq.Line(src, l.offset(0)))
			d.Skip = skip
		case !ok:
			continue
		}
		if d.Skip != "" {
			d.Source, d.ExtraSources = index, extras
			reqs = append(reqs, Hashed{Dep: d})
			continue
		}
		d = pyreq.NewDep(Name, DepType, file, r.Name, pyreq.Line(src, l.offset(r.NameStart)))
		d.Source, d.ExtraSources = index, extras
		at := l.offset(r.SpecStart)
		exact := at+len(r.Spec) <= len(src) && string(src[at:at+len(r.Spec)]) == r.Spec
		pyreq.SetVersion(&d, r.Spec, at, exact, pyreq.PEP440)
		reqs = append(reqs, Hashed{Dep: d, Hashes: hashSpans(l, len(text)-len(opts), opts)})
	}
	return reqs
}

// hashSpans returns the file spans of the `--hash=<value>` and `--hash <value>` options in
// opts, which starts at offset at of l.text.
func hashSpans(l line, at int, opts string) []dep.Span {
	var spans []dep.Span
	for i := 0; i < len(opts); {
		if opts[i] == ' ' || opts[i] == '\t' {
			i++
			continue
		}
		end := i + strings.IndexAny(opts[i:]+" ", " \t")
		tok := opts[i:end]
		if tok == "--hash" { // value in the next field
			for end < len(opts) && (opts[end] == ' ' || opts[end] == '\t') {
				end++
			}
			end += strings.IndexAny(opts[end:]+" ", " \t")
			tok = opts[i:end]
		}
		if v, ok := strings.CutPrefix(tok, "--hash"); ok && len(v) > 1 && strings.IndexByte("= \t", v[0]) >= 0 {
			spans = append(spans, dep.Span{Start: l.offset(at + i), End: l.offset(at+end-1) + 1})
		}
		i = end
	}
	return spans
}

// HashEdit replaces the `--hash` options of r with one `--hash=sha256:<hex>` option per
// hash, sorted (as pip-compile writes them) and separated like the first two options were
// (or as the first was from the requirement).
func HashEdit(src []byte, r Hashed, sha256 []string) (patch.Edit, error) {
	if len(r.Hashes) == 0 || len(sha256) == 0 {
		return patch.Edit{}, fmt.Errorf("%s: no hashes to replace", r.Dep.Name)
	}
	first, last := r.Hashes[0], r.Hashes[len(r.Hashes)-1]
	for i := 1; i < len(r.Hashes); i++ {
		if strings.Trim(string(src[r.Hashes[i-1].End:r.Hashes[i].Start]), " \t\r\n\\") != "" {
			return patch.Edit{}, fmt.Errorf("%s: other options between --hash options", r.Dep.Name)
		}
	}
	var sep string
	if len(r.Hashes) > 1 {
		sep = string(src[first.End:r.Hashes[1].Start])
	} else {
		s := first.Start
		for s > 0 && strings.IndexByte(" \t\r\n\\", src[s-1]) >= 0 {
			s--
		}
		sep = string(src[s:first.Start])
	}
	prefix := "--hash="
	if bytes.HasPrefix(src[first.Start:], []byte("--hash ")) || bytes.HasPrefix(src[first.Start:], []byte("--hash\t")) {
		prefix = "--hash "
	}
	hs := slices.Clone(sha256)
	slices.Sort(hs)
	hs = slices.Compact(hs)
	var b strings.Builder
	for i, h := range hs {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(prefix + "sha256:" + h)
	}
	return patch.Edit{Start: first.Start, End: last.End, Old: string(src[first.Start:last.End]), New: b.String()}, nil
}

// line is one logical line: physical lines joined at trailing backslashes, comment removed,
// trimmed. segs map offsets in text back to the file.
type line struct {
	text string
	segs []seg
}

// seg is a run of text starting at text offset at and file offset file.
type seg struct{ at, file int }

// offset maps an offset in l.text to a file offset.
func (l line) offset(i int) int {
	s := l.segs[0]
	for _, x := range l.segs[1:] {
		if x.at > i {
			break
		}
		s = x
	}
	return s.file + i - s.at
}

func logicalLines(src []byte) []line {
	var out []line
	var cur line
	var b strings.Builder
	for pos := 0; pos < len(src); {
		end := pos
		for end < len(src) && src[end] != '\n' {
			end++
		}
		next := end + 1
		raw := strings.TrimSuffix(string(src[pos:end]), "\r")
		cont := strings.HasSuffix(raw, "\\")
		raw = strings.TrimSuffix(raw, "\\")
		cur.segs = append(cur.segs, seg{at: b.Len(), file: pos})
		b.WriteString(raw)
		if !cont || next >= len(src) {
			cur.text = b.String()
			out = append(out, cur.finish())
			cur, b = line{}, strings.Builder{}
		}
		pos = next
	}
	return out
}

// finish removes the comment (a `#` at the start or after whitespace) and surrounding
// whitespace, shifting the segments.
func (l line) finish() line {
	t := l.text
	for i := range len(t) {
		if t[i] == '#' && (i == 0 || t[i-1] == ' ' || t[i-1] == '\t') {
			t = t[:i]
			break
		}
	}
	t = strings.TrimRight(t, " \t")
	lead := len(t) - len(strings.TrimLeft(t, " \t"))
	l.text = t[lead:]
	for i := range l.segs {
		l.segs[i].at -= lead
	}
	return l
}

// indexURL returns the URL of an `--index-url`/`-i` option line, or "".
func indexURL(text string) string { return option(text, "--index-url", "-i") }

// option returns the value of an option line of one of names (`--opt value`, `--opt=value`),
// or "".
func option(text string, names ...string) string {
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	for _, opt := range names {
		switch {
		case f[0] == opt && len(f) > 1:
			return f[1]
		case strings.HasPrefix(f[0], opt+"="):
			return strings.TrimPrefix(f[0], opt+"=")
		}
	}
	return ""
}

// splitOptions splits a requirement line at its first per-requirement option (whitespace
// followed by `-` outside quotes): `pkg==1 --hash=sha256:…`.
func splitOptions(text string) (req, opts string) {
	var quote byte
	for i := 1; i < len(text); i++ {
		switch c := text[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '-' && (text[i-1] == ' ' || text[i-1] == '\t'):
			return strings.TrimRight(text[:i], " \t"), text[i:]
		}
	}
	return text, ""
}

// pathSkip returns the skip reason of a requirement line that is a URL, VCS reference or
// local path rather than a name, or "".
func pathSkip(req string) string {
	switch {
	case pyreq.HasAnyPrefix(req, "git+", "hg+", "svn+", "bzr+"):
		return pyreq.SkipGit
	case strings.Contains(req, "://"):
		return pyreq.SkipURL
	case pyreq.HasAnyPrefix(req, ".", "/", "~/", "file:") || strings.HasSuffix(req, ".whl") || strings.HasSuffix(req, ".tar.gz") || strings.HasSuffix(req, ".zip"):
		return pyreq.SkipLocal
	}
	return ""
}
