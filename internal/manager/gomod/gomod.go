// Package gomod extracts requirements and go/toolchain directives from go.mod files.
package gomod

import (
	"bytes"
	"path"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

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

// Extract returns one Dep per require entry; RefSpan covers the version token.
// env.Vendored is consulted with the go.mod's directory.
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
		switch {
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
