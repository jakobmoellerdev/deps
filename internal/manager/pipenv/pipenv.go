// Package pipenv extracts the `[packages]`, `[dev-packages]` and custom package categories
// of Pipfiles, resolving `index` against the `[[source]]` entries.
package pipenv

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name (Renovate's).
const Name = "pipenv"

// DepTypes and skip reasons.
const (
	DepTypePackages    = "packages"
	DepTypeDevPackages = "dev-packages"
	DepTypeCategory    = "category" // a custom package category (pipenv ≥ 2022.10)
	SkipUnknownIndex   = "unknown index"
)

var matchRe = regexp.MustCompile(`(^|/)Pipfile$`)

// pipenvManager rewrites specifiers with pyreq.Ranges.
type pipenvManager struct{ pyreq.Ranges }

// New returns the pipenv manager.
func New() manager.Manager { return pipenvManager{} }

func (pipenvManager) Name() string { return Name }

// Match reports whether the slash path is a Pipfile. The suffix check skips the regexp for the
// paths that cannot match.
func (pipenvManager) Match(p string) bool {
	return strings.HasSuffix(p, "Pipfile") && matchRe.MatchString(p)
}

// ReadLocks records the versions Pipfile.lock locks the deps to, by category
// (pytool.ReadLocks).
func (pipenvManager) ReadLocks(root string, files [][]dep.Dep) []error {
	return pytool.ReadLocks(root, files)
}

// LockRoots implements manager.LockMaintainer: the Pipfile.lock next to each Pipfile
// (pytool.LockRoots).
func (pipenvManager) LockRoots(root string, files []string) []manager.LockRoot {
	return pytool.LockRoots(root, files, pytool.PipfileLock)
}

// Extract returns one dep per entry of [packages], [dev-packages] and the custom
// categories: every other top-level table except [source], [requires], [pipenv] and [scripts].
func (pipenvManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	x := extractor{file: file, indexes: map[string]string{}, python: python(doc)}
	all := doc.Get("pipenv", "install_search_all_sources")
	searchAll := all != nil && all.Str == "true"
	if a := doc.Get("source"); a != nil && a.Kind == tomlspan.Array {
		for i, s := range a.Items {
			name, u := s.Get("name"), s.Get("url")
			if u == nil || u.Kind != tomlspan.String {
				continue
			}
			url := u.Str
			if strings.Contains(url, "${") {
				url = "" // environment variables are not expanded: PIP_INDEX_URL or PyPI
			}
			if name != nil && name.Kind == tomlspan.String {
				x.indexes[name.Str] = url
			}
			if i == 0 {
				x.defaultIndex = url
			} else if searchAll && url != "" {
				x.extra = append(x.extra, url)
			}
		}
	}
	x.table(DepTypePackages, doc.Get("packages"))
	x.table(DepTypeDevPackages, doc.Get("dev-packages"))
	for _, k := range doc.Keys {
		switch k {
		case "packages", "dev-packages", "source", "requires", "pipenv", "scripts":
		default:
			x.table(DepTypeCategory, doc.Fields[k])
		}
	}
	return x.deps, nil
}

type extractor struct {
	file         string
	indexes      map[string]string
	defaultIndex string   // the first [[source]]
	extra        []string // with [pipenv] install_search_all_sources, the other [[source]]s
	python       string   // the [requires] Python constraint
	deps         []dep.Dep
}

// python returns the Python constraint of [requires]: python_full_version exactly, else
// python_version as a prefix (3.11 → ==3.11.*).
func python(doc *tomlspan.Value) string {
	if v := doc.Get("requires", "python_full_version"); v != nil && v.Kind == tomlspan.String && v.Str != "" {
		return "==" + v.Str
	}
	if v := doc.Get("requires", "python_version"); v != nil && v.Kind == tomlspan.String && v.Str != "" {
		return "==" + v.Str + ".*"
	}
	return ""
}

func (x *extractor) table(depType string, t *tomlspan.Value) {
	if t == nil || t.Kind != tomlspan.Table {
		return
	}
	for _, name := range t.Keys {
		v := t.Fields[name]
		d := pyreq.NewDep(Name, depType, x.file, name, v.Line)
		d.Source, d.ExtraSources, d.PythonConstraint = x.defaultIndex, x.extra, x.python
		switch v.Kind {
		case tomlspan.String:
			pyreq.SetVersion(&d, v.Str, v.Start, v.Exact, pyreq.Pipenv)
		case tomlspan.Table:
			if pyreq.GitTable(&d, v, "ref") {
				break
			}
			spec, skip := pyreq.TableSpec(v)
			if skip != "" {
				d.Skip = skip
				break
			}
			d.Line = spec.Line
			pyreq.SetVersion(&d, spec.Str, spec.Start, spec.Exact, pyreq.Pipenv)
			if idx := v.Get("index"); idx != nil && idx.Kind == tomlspan.String && d.Skip == "" {
				if u, ok := x.indexes[idx.Str]; ok {
					d.Source, d.ExtraSources = u, nil
				} else {
					d.Skip = SkipUnknownIndex + " " + idx.Str
				}
			}
		default:
			d.Skip = pyreq.SkipRange
		}
		x.deps = append(x.deps, d)
	}
}
