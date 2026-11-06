// Package pep723 extracts the dependencies of PEP 723 inline script metadata: the TOML of a
// `# /// script` comment block in a Python file.
package pep723

import (
	"bytes"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name (Renovate's).
const Name = "pep723"

// DepType is the dep type of every `dependencies` entry (Renovate's, shared with pep621).
const DepType = "project.dependencies"

var (
	openLine  = []byte("# /// script")
	closeLine = "# ///"
)

type pep723Manager struct{}

// New returns the pep723 manager.
func New() manager.Manager { return pep723Manager{} }

func (pep723Manager) Name() string { return Name }

// Match reports whether the slash path is a Python file.
func (pep723Manager) Match(p string) bool { return strings.HasSuffix(p, ".py") }

// Edits rewrites the range (pyreq.Edits).
func (pep723Manager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (pep723Manager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (pep723Manager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return pyreq.NewRange(d, v, strategy)
}

// Extract returns one dep per PEP 508 string of the `dependencies` array of the script
// block, with `requires-python` as Python constraint. A file without block has no deps.
func (pep723Manager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	toml, ok := block(src)
	if !ok {
		return nil, nil
	}
	doc, err := tomlspan.Parse(toml)
	if err != nil {
		return nil, err
	}
	var python string
	if rp := doc.Get("requires-python"); rp != nil && rp.Kind == tomlspan.String {
		python = rp.Str
	}
	var deps []dep.Dep
	for _, v := range doc.Get("dependencies").Strings() {
		d, ok := pyreq.TOMLRequirement(Name, DepType, file, v)
		if !ok {
			continue
		}
		d.PythonConstraint = python
		deps = append(deps, d)
	}
	return deps, nil
}

// block returns the TOML of the first `# /// script` block as a buffer as long as src: the
// block's content lines without their `#`/`# ` prefix at their own offsets, everything else
// blanked (newlines kept), so spans and lines of the parse are those of src. Per PEP 723's
// reference regex the block is the consecutive comment lines after an opening line up to the
// last `# ///` line among them; an opening line without such a closing line is no block, and
// the search continues after it. ok is false when no opening line has a closing one.
func block(src []byte) ([]byte, bool) {
	start, end := -1, -1 // first content line, offset of the closing line
	for i := 0; end < 0; {
		j := bytes.Index(src[i:], openLine)
		if j < 0 {
			return nil, false
		}
		i += j
		if (i > 0 && src[i-1] != '\n') || strings.TrimRight(lineAt(src, i), "\r") != string(openLine) {
			i += len(openLine)
			continue
		}
		start = i + len(lineAt(src, i)) + 1
		if start >= len(src) {
			return nil, false
		}
		for p := start; p < len(src); {
			l := strings.TrimRight(lineAt(src, p), "\r")
			if l != "#" && !strings.HasPrefix(l, "# ") {
				break
			}
			if l == closeLine {
				end = p
			}
			p += len(lineAt(src, p)) + 1
		}
		i = start
	}
	out := bytes.Repeat([]byte{' '}, len(src))
	for k, c := range src {
		if c == '\n' {
			out[k] = '\n'
		}
	}
	for p := start; p < end; {
		l := lineAt(src, p)
		if len(l) > 2 {
			copy(out[p+2:], l[2:])
		}
		p += len(l) + 1
	}
	return out, true
}

// lineAt returns the line of src starting at i, without its newline.
func lineAt(src []byte, i int) string {
	if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
		return string(src[i : i+j])
	}
	return string(src[i:])
}
