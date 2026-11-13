// Package gomod extracts requirements and go/toolchain directives from go.mod files and the
// go/toolchain directives from go.work files.
package gomod

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "gomod"

var matchRe = regexp.MustCompile(`(^|/)go\.(mod|work)$`)

type gomodManager struct{ scans *ScanCache }

// New returns the gomod manager; it scans a module again for every update that rewrites
// import paths, so that a file written since the last one is seen.
func New() manager.Manager { return gomodManager{} }

// NewCached returns the gomod manager memoizing its module scans in scans, for a run that
// does not change the .go files it reads.
func NewCached(scans *ScanCache) manager.Manager { return gomodManager{scans: scans} }

func (gomodManager) Name() string { return Name }

// Match reports whether the slash path is a go.mod or go.work file. The suffix check skips the
// regexp for the paths that cannot match.
func (gomodManager) Match(p string) bool {
	return (strings.HasSuffix(p, "go.mod") || strings.HasSuffix(p, "go.work")) && matchRe.MatchString(p)
}

// Extract returns one Dep per require entry; RefSpan covers the version token, NameParts the
// module path. Pseudo-versions are deps too: the engine proposes newer pseudo-versions (digest
// updates) or releases for them. A go.work gives its go and toolchain directives.
func (gomodManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if path.Base(file) == "go.work" {
		w, err := modfile.ParseWork(file, src, nil)
		if err != nil {
			return nil, err
		}
		var deps []dep.Dep
		if w.Go != nil {
			deps = append(deps, directive(file, src, "go", w.Go.Version, w.Go.Syntax))
		}
		if w.Toolchain != nil {
			deps = append(deps, directive(file, src, "toolchain", w.Toolchain.Name, w.Toolchain.Syntax))
		}
		return deps, nil
	}
	f, err := modfile.Parse(file, src, nil)
	if err != nil {
		return nil, err
	}
	deps := make([]dep.Dep, 0, len(f.Require)+len(f.Replace)+2)
	if f.Go != nil {
		deps = append(deps, directive(file, src, "go", f.Go.Version, f.Go.Syntax))
	}
	if f.Toolchain != nil {
		deps = append(deps, directive(file, src, "toolchain", f.Toolchain.Name, f.Toolchain.Syntax))
	}
	for _, r := range f.Require {
		v := r.Mod.Version
		d := dep.Dep{
			Manager: Name, Datasource: "go", Versioning: "go", DepType: "direct",
			File: file, Name: r.Mod.Path, Version: v, Line: r.Syntax.Start.Line,
		}
		if r.Indirect {
			d.DepType = "indirect"
		}
		s, e := r.Syntax.Start.Byte, r.Syntax.End.Byte
		if i := bytes.LastIndex(src[s:e], []byte(v)); i >= 0 {
			d.RefSpan = dep.Span{Start: s + i, End: s + i + len(v)}
		}
		if i := bytes.Index(src[s:e], []byte(r.Mod.Path)); i >= 0 {
			d.NameParts = []dep.NamePart{{Span: dep.Span{Start: s + i, End: s + i + len(r.Mod.Path)}, Text: r.Mod.Path}}
		}
		if replaced(f, r.Mod.Path) {
			d.Skip = "replaced"
		}
		deps = append(deps, d)
	}
	for _, r := range f.Replace {
		// A replacement by a filesystem path has no version and nothing to update; the
		// replaced require is skipped either way.
		if r.New.Version == "" {
			continue
		}
		d := dep.Dep{
			Manager: Name, Datasource: "go", Versioning: "go", DepType: "replace",
			File: file, Name: r.New.Path, Version: r.New.Version, Line: r.Syntax.Start.Line,
		}
		s, e := r.Syntax.Start.Byte, r.Syntax.End.Byte
		// The replacement is the last token of the directive, so its version and path are
		// the last occurrences even when a module replaces itself.
		if i := bytes.LastIndex(src[s:e], []byte(r.New.Version)); i >= 0 {
			d.RefSpan = dep.Span{Start: s + i, End: s + i + len(r.New.Version)}
		} else {
			d.Skip = "cannot locate value"
		}
		if i := bytes.LastIndex(src[s:e], []byte(r.New.Path)); i >= 0 {
			d.NameParts = []dep.NamePart{{Span: dep.Span{Start: s + i, End: s + i + len(r.New.Path)}, Text: r.New.Path}}
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// directive builds the dep for a go or toolchain directive; the toolchain value drops its
// "go" prefix and RefSpan covers only the numeric part.
func directive(file string, src []byte, name, value string, x *modfile.Line) dep.Dep {
	v := value
	if name == "toolchain" {
		v = strings.TrimPrefix(value, "go")
	}
	d := dep.Dep{
		Manager: Name, Datasource: "go-toolchain", Versioning: "tag", DepType: name,
		File: file, Name: name, Version: v, Line: x.Start.Line,
	}
	s, e := x.Start.Byte, x.End.Byte
	if i := bytes.LastIndex(src[s:e], []byte(value)); i >= 0 {
		start := s + i + len(value) - len(v)
		d.RefSpan = dep.Span{Start: start, End: start + len(v)}
	} else {
		d.Skip = "cannot locate value"
	}
	if _, ok := versioning.ParseTag(v); !ok && d.Skip == "" {
		d.Skip = "prerelease toolchain"
	}
	return d
}

// Edits replaces the version token, and the module path of a major module path update or a
// replacement.
func (gomodManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	edits := []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}
	if u.NewName != "" {
		ne, err := manager.NameEdits(d, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = append(edits, ne...)
	}
	return edits, nil
}

// Rename implements manager.Renamer: name is a module path. Like a major module path update,
// a replacement also rewrites the import paths (FileEdits).
func (gomodManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if err := module.CheckPath(name); err != nil {
		return d, err
	}
	d.Name = name
	return d, nil
}

// FileEdits implements manager.FileEditor: a major module path update or a replacement
// (u.NewName) rewrites the import paths of the old module (the path itself and its packages)
// in the .go files of the dep's module, i.e. every .go file below the go.mod's directory
// except in directories the go command ignores (vendor, testdata, names starting with . or _)
// and in nested modules. Only the import path literals change. The default ignorePaths do not
// apply: tests belong to the module and must keep compiling.
func (m gomodManager) FileEdits(root string, u dep.Update) (map[string][]patch.Edit, error) {
	if u.NewName == "" {
		return nil, nil
	}
	base := filepath.Join(root, filepath.FromSlash(path.Dir(u.Dep.File)))
	files, err := m.scans.files(base)
	if err != nil {
		return nil, err
	}
	old := []byte(u.Dep.Name)
	var out map[string][]patch.Edit
	var buf []byte // reused: most .go files do not mention the module
	for _, p := range files {
		src, err := readFile(p, buf)
		buf = src
		if err != nil {
			return nil, err
		}
		if !bytes.Contains(src, old) {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		edits, err := importEdits(rel, src, u.Dep.Name, u.NewName)
		if err != nil {
			return nil, err
		}
		if len(edits) > 0 {
			if out == nil {
				out = map[string][]patch.Edit{}
			}
			out[rel] = edits
		}
	}
	return out, nil
}

// ScanCache memoizes the .go files of a module directory (goFiles) by absolute path: a group
// can carry several major path updates or replacements of the same module, and each of them
// would otherwise walk the module again. The files must not change while it is in use, so
// only a run that reads one checkout once uses one (NewCached); the zero value is not
// usable, use NewScanCache.
type ScanCache struct {
	mu sync.Mutex
	m  map[string]*scanEntry
}

type scanEntry struct {
	once  sync.Once
	files []string
	err   error
}

// NewScanCache returns an empty ScanCache.
func NewScanCache() *ScanCache { return &ScanCache{m: map[string]*scanEntry{}} }

// files is goFiles of dir, memoized when c is not nil.
func (c *ScanCache) files(dir string) ([]string, error) {
	if c == nil {
		return goFiles(dir)
	}
	c.mu.Lock()
	e := c.m[dir]
	if e == nil {
		e = &scanEntry{}
		c.m[dir] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.files, e.err = goFiles(dir) })
	return e.files, e.err
}

// goFiles returns the absolute paths of the .go files of the module in the absolute directory
// base, in walk order: every one below it except in directories the go command ignores
// (vendor, testdata, names starting with . or _) and in nested modules.
func goFiles(base string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p == base {
				return nil
			}
			if n := e.Name(); n == "vendor" || n == "testdata" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
				return fs.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return fs.SkipDir
			}
			return nil
		}
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".go") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// readFile is os.ReadFile into buf (reused, grown as needed).
func readFile(p string, buf []byte) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return buf, err
	}
	defer f.Close()
	buf = buf[:0]
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf, nil
			}
			return buf, err
		}
	}
}

// importEdits returns the edits replacing the import paths old and old/... with newPath in
// the Go source src.
func importEdits(file string, src []byte, old, newPath string) ([]patch.Edit, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var edits []patch.Edit
	for _, is := range f.Imports {
		p, err := strconv.Unquote(is.Path.Value)
		if err != nil {
			continue
		}
		rest, ok := strings.CutPrefix(p, old)
		if !ok || rest != "" && rest[0] != '/' {
			continue
		}
		lit := is.Path.Value
		repl := strconv.Quote(newPath + rest)
		if lit[0] == '`' {
			repl = "`" + newPath + rest + "`"
		}
		start := fset.Position(is.Path.Pos()).Offset
		edits = append(edits, patch.Edit{Start: start, End: start + len(lit), Old: lit, New: repl})
	}
	return edits, nil
}

// LockRoots implements manager.LockMaintainer: the go.sum next to each go.mod, unless the
// module has none or is a module of a workspace that vendors its modules (Workspace, with a
// vendor/modules.txt next to the go.work). Lock file maintenance (gotool.Tidier.Maintain)
// moves the `// indirect` requirements.
func (gomodManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if path.Base(f) != "go.mod" {
			continue
		}
		dir := path.Dir(f)
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(filepath.Join(abs, "go.sum")); err != nil {
			continue
		}
		if ws, ok := Workspace(root, dir); ok && Vendors(filepath.Join(root, filepath.FromSlash(ws))) {
			continue
		}
		out = append(out, manager.LockRoot{Manifest: f, Lockfile: path.Join(dir, "go.sum")})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Vendors reports whether the absolute directory dir has a vendor directory the go command
// uses (vendor/modules.txt).
func Vendors(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "vendor", "modules.txt"))
	return err == nil
}

// Workspace returns the workspace of the module directory dir (a slash path relative to
// root): the directory of the nearest go.work at or above dir, up to root, when that go.work
// uses dir. ok is false otherwise; GOWORK is not consulted.
func Workspace(root, dir string) (ws string, ok bool) {
	for d := dir; ; d = path.Dir(d) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d), "go.work")); err == nil {
			mods, err := WorkspaceModules(filepath.Join(root, filepath.FromSlash(d)))
			return d, err == nil && slices.Contains(mods, relTo(d, dir))
		}
		if d == "." {
			return "", false
		}
	}
}

// WorkspaceModules returns the module directories the go.work in the absolute directory ws
// uses, as cleaned slash paths relative to ws ("." for ws itself). Directories outside ws
// are left out.
func WorkspaceModules(ws string) ([]string, error) {
	file := filepath.Join(ws, "go.work")
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	w, err := modfile.ParseWork(file, b, nil)
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, u := range w.Use {
		if p := path.Clean(filepath.ToSlash(u.Path)); !path.IsAbs(p) && filepath.IsLocal(filepath.FromSlash(p)) || p == "." {
			mods = append(mods, p)
		}
	}
	return mods, nil
}

// relTo returns the slash path p relative to its ancestor directory base.
func relTo(base, p string) string {
	if base == "." {
		return p
	}
	if p == base {
		return "."
	}
	return strings.TrimPrefix(p, base+"/")
}

func replaced(f *modfile.File, p string) bool {
	for _, r := range f.Replace {
		if r.Old.Path == p {
			return true
		}
	}
	return false
}
