// Package gomod extracts direct requirements from go.mod files.
package gomod

import (
	"bytes"
	"regexp"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the manager name.
const Name = "gomod"

var matchRe = regexp.MustCompile(`(^|/)go\.mod$`)

// Match reports whether the slash path is a go.mod file.
func Match(path string) bool { return matchRe.MatchString(path) }

// Extract returns one Dep per require entry; RefSpan covers the version token.
func Extract(file string, src []byte, vendored bool) ([]dep.Dep, error) {
	f, err := modfile.Parse(file, src, nil)
	if err != nil {
		return nil, err
	}
	deps := make([]dep.Dep, 0, len(f.Require))
	for _, r := range f.Require {
		v := r.Mod.Version
		d := dep.Dep{Manager: Name, File: file, Name: r.Mod.Path, Version: v, Line: r.Syntax.Start.Line}
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

func replaced(f *modfile.File, p string) bool {
	for _, r := range f.Replace {
		if r.Old.Path == p {
			return true
		}
	}
	return false
}
