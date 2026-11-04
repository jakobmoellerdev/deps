// Package gomod extracts requirements and go/toolchain directives from go.mod files.
package gomod

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "gomod"

var matchRe = regexp.MustCompile(`(^|/)go\.mod$`)

type gomodManager struct{}

// New returns the gomod manager.
func New() manager.Manager { return gomodManager{} }

func (gomodManager) Name() string { return Name }

// Match reports whether the slash path is a go.mod file.
func (gomodManager) Match(p string) bool { return matchRe.MatchString(p) }

// Extract returns one Dep per require entry; RefSpan covers the version token, NameSpan the
// module path. env.Vendored is consulted with the go.mod's directory. Pseudo-versions are
// deps too: the engine proposes newer pseudo-versions (digest updates) or releases for them.
func (gomodManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	f, err := modfile.Parse(file, src, nil)
	if err != nil {
		return nil, err
	}
	vendored := env.Vendored != nil && env.Vendored(path.Dir(file))
	deps := make([]dep.Dep, 0, len(f.Require)+2)
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
			d.NameSpan = dep.Span{Start: s + i, End: s + i + len(r.Mod.Path)}
		}
		switch {
		case replaced(f, r.Mod.Path):
			d.Skip = "replaced"
		case vendored:
			d.Skip = "vendored module (not supported)"
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

// Edits replaces the version token, and the module path of a major module path update.
func (gomodManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	edits := []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}
	if u.NewName != "" {
		if d.NameSpan.End <= d.NameSpan.Start {
			return nil, errors.New("cannot locate module path")
		}
		edits = append(edits, patch.Edit{Start: d.NameSpan.Start, End: d.NameSpan.End, Old: d.Name, New: u.NewName})
	}
	return edits, nil
}

// FileEdits implements manager.FileEditor: a major module path update (u.NewName) rewrites
// the import paths of the old module (the path itself and its packages) in the .go files of
// the dep's module, i.e. every .go file below the go.mod's directory except in directories the
// go command ignores (vendor, testdata, names starting with . or _) and in nested modules.
// Only the import path literals change. The default ignorePaths do not apply: tests belong to
// the module and must keep compiling.
func (gomodManager) FileEdits(root string, u dep.Update) (map[string][]patch.Edit, error) {
	if u.NewName == "" {
		return nil, nil
	}
	old := []byte(u.Dep.Name)
	base := filepath.Join(root, filepath.FromSlash(path.Dir(u.Dep.File)))
	var out map[string][]patch.Edit
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
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, old) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		edits, err := importEdits(rel, src, u.Dep.Name, u.NewName)
		if err != nil {
			return err
		}
		if len(edits) > 0 {
			if out == nil {
				out = map[string][]patch.Edit{}
			}
			out[rel] = edits
		}
		return nil
	})
	return out, err
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

func replaced(f *modfile.File, p string) bool {
	for _, r := range f.Replace {
		if r.Old.Path == p {
			return true
		}
	}
	return false
}
