// Package gomod extracts direct requirements from go.mod files.
package gomod

import (
	"bytes"
	"path"
	"regexp"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
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

// Extract returns one Dep per require entry; RefSpan covers the version token.
// env.Vendored is consulted with the go.mod's directory.
func (gomodManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	f, err := modfile.Parse(file, src, nil)
	if err != nil {
		return nil, err
	}
	vendored := env.Vendored != nil && env.Vendored(path.Dir(file))
	deps := make([]dep.Dep, 0, len(f.Require))
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
		switch {
		case r.Indirect:
			d.Skip = "indirect"
		case replaced(f, r.Mod.Path):
			d.Skip = "replaced"
		case module.IsPseudoVersion(v):
			d.Skip = "pseudo-version"
		case vendored:
			d.Skip = "vendored module (not supported)"
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// Edits replaces the version token.
func (gomodManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

func replaced(f *modfile.File, p string) bool {
	for _, r := range f.Replace {
		if r.Old.Path == p {
			return true
		}
	}
	return false
}
