// Package beam holds the managers of the BEAM ecosystems whose packages come from Hex: mix
// (Elixir's mix.exs with mix.lock) and gleam (gleam.toml with manifest.toml). Both write Elixir
// version requirements (`~> 1.7`, `>= 1.0.0 and < 2.0.0`), which they rewrite per
// rangeStrategy alike, read the versions their lockfile locks, and update the lockfile after
// an update with the ecosystem's tool (MixLocker, GleamLocker).
package beam

import (
	"bytes"
	"fmt"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Manager names (Renovate's).
const (
	NameMix   = "mix"
	NameGleam = "gleam"
)

// Datasource is the datasource of Hex packages; git dependencies with a tag use GitDatasource.
const (
	Datasource    = "hex"
	GitDatasource = "git-tags"
)

// Versioning is the versioning scheme of Hex packages.
const Versioning = "hex"

// Mix dep types (Renovate's): dependencies with an `only:` option that leaves out :prod are
// dev.
const (
	DepTypeProd = "prod"
	DepTypeDev  = "dev"
)

// Skip reasons.
const (
	skipPath        = "path dependency"
	skipUmbrella    = "umbrella dependency"
	skipGit         = "git dependency"
	skipNoReq       = "no requirement"
	skipUnsupported = "unsupported requirement"
	skipEscaped     = "cannot locate value"
	skipRepo        = "custom hex repository"
	skipNoLock      = "no locked version"
)

const mixManifest = "mix.exs"

type mixManager struct{}

// NewMix returns the mix manager.
func NewMix() manager.Manager { return mixManager{} }

func (mixManager) Name() string { return NameMix }

// Match reports whether the slash path is a mix.exs.
func (mixManager) Match(p string) bool {
	return p == mixManifest || strings.HasSuffix(p, "/"+mixManifest)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (mixManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{mixManifest}}
}

// Extract returns one Dep per dependency tuple of the deps functions (mixEntries), in file
// order. A Hex dependency's Range is its requirement as written (RefSpan its bytes), Version
// the requirement's anchor ("" without a lower bound; ReadLocks takes the locked version
// then), Name the Hex package (`hex:` overrides the app name; `name:organization` for an
// `organization:` or `repo: "hexpm:<organization>"`). A git dependency (`git:`, `github:`)
// with a `tag:` is a git-tags dep of the repository whose Version is the tag; other git,
// path and umbrella dependencies are skipped.
func (mixManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	es := mixEntries(src)
	deps := make([]dep.Dep, 0, len(es))
	line, pos := 1, 0
	for _, e := range es {
		line += bytes.Count(src[pos:e.at], []byte{'\n'})
		pos = e.at
		deps = append(deps, mixDep(file, line, e))
	}
	return deps, nil
}

// mixDep is the dep of dependency tuple e at line.
func mixDep(file string, line int, e entry) dep.Dep {
	d := dep.Dep{Manager: NameMix, Datasource: Datasource, Versioning: Versioning, DepType: DepTypeProd, File: file, Line: line, Name: e.app}
	if only, ok := e.opts["only"]; ok && !strings.Contains(only.raw, ":prod") {
		d.DepType = DepTypeDev
	}
	if h, ok := e.opts["hex"]; ok {
		if a := h.atom(); a != "" {
			d.Name = a
		} else if h.lit != nil && h.lit.exact {
			d.Name = h.lit.s
		}
	}
	org := ""
	if o, ok := e.opts["organization"]; ok && o.lit != nil {
		org = o.lit.s
	}
	if r, ok := e.opts["repo"]; ok {
		repo := strings.TrimPrefix(r.raw, ":")
		if r.lit != nil {
			repo = r.lit.s
		}
		switch o, isOrg := strings.CutPrefix(repo, "hexpm:"); {
		case isOrg:
			org = o
		case repo != "hexpm":
			d.Skip = skipRepo
		}
	}
	if org != "" {
		d.Name += ":" + org
	}
	if e.req != nil {
		d.Version = e.req.s
	}
	switch {
	case e.opts["path"].raw != "":
		d.Skip = skipPath
	case e.opts["in_umbrella"].raw == "true":
		d.Skip = skipUmbrella
	case e.opts["git"].raw != "" || e.opts["github"].raw != "":
		gitDep(&d, e)
	case d.Skip != "":
	case e.req == nil && e.reqText == "":
		d.Skip = skipNoReq
	case e.req == nil:
		d.Version, d.Skip = e.reqText, skipUnsupported
	case !e.req.exact:
		d.Skip = skipEscaped
	default:
		a, ok := anchor(e.req.s)
		if !ok {
			d.Skip = skipUnsupported
			break
		}
		d.Version, d.Range, d.RefSpan = a, e.req.s, dep.Span{Start: e.req.at.start, End: e.req.at.end}
	}
	return d
}

// gitDep makes a git dependency with a `tag:` on a repository deps can list the tags of a
// git-tags dep (Name the app, Source the repository); a branch, ref or default-branch one is
// skipped.
func gitDep(d *dep.Dep, e entry) {
	d.Datasource, d.Versioning, d.Name, d.Version = GitDatasource, "tag", e.app, ""
	src := ""
	if g := e.opts["github"].lit; g != nil && strings.Count(g.s, "/") == 1 {
		src = "https://github.com/" + strings.TrimSuffix(g.s, ".git")
	} else if g := e.opts["git"].lit; g != nil {
		_, src = manager.GitRepo(g.s)
	}
	tag := e.opts["tag"].lit
	switch {
	case tag == nil || src == "" || e.opts["ref"].raw != "" || e.opts["branch"].raw != "":
		d.Skip = skipGit
	case !tag.exact:
		d.Skip = skipEscaped
	default:
		d.Source, d.Version, d.RefSpan = src, tag.s, dep.Span{Start: tag.at.start, End: tag.at.end}
	}
}

// Edits replaces a git dependency's tag, or rewrites the requirement for the new version under
// u.RangeStrategy (newRange).
func (mixManager) Edits(u dep.Update) ([]patch.Edit, error) {
	return edits(u)
}

// NewRange implements manager.RangeRewriter (newRange).
func (mixManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return newRange(d.Range, v, strategy)
}

// edits are the Edits of both managers.
func edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	out, ok := newRange(d.Range, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("requirement %q cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}
