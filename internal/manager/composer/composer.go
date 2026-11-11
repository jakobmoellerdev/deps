// Package composer extracts the packages composer.json requires (`require`, `require-dev`),
// reads the versions composer.lock locks them to, rewrites their constraints per
// rangeStrategy, and updates composer.lock after an update with `composer update` (see
// Locker).
package composer

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "composer"

// Datasource is the datasource of packages; packages of a vcs repository keyed by their name
// use GitDatasource.
const (
	Datasource    = "packagist"
	GitDatasource = "git-tags"
)

// Versioning is the versioning scheme of packages.
const Versioning = "composer"

// PackagistURL is Packagist, the last repository Composer searches unless composer.json
// disables it.
const PackagistURL = "https://repo.packagist.org"

// DepTypes.
const (
	DepTypeRequire    = "require"
	DepTypeRequireDev = "require-dev"
)

// Skip reasons.
const (
	skipPlatform    = "platform package"
	skipUnsupported = "unsupported constraint"
	skipEscaped     = "cannot locate value"
	skipPath        = "path repository"
	skipPackageRepo = "package repository"
	skipGitURL      = "unsupported repository URL"
	skipNoLock      = "no locked version"
)

const manifest = "composer.json"

// typeTYPO3Extension is the package type whose constraints rangeStrategy auto widens
// (Renovate's composer getRangeStrategy).
const typeTYPO3Extension = "typo3-cms-extension"

type composerManager struct{}

// New returns the composer manager.
func New() manager.Manager { return composerManager{} }

func (composerManager) Name() string { return Name }

// Match reports whether the slash path is a composer.json.
func (composerManager) Match(p string) bool {
	return p == manifest || strings.HasSuffix(p, "/"+manifest)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (composerManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{manifest}}
}

// Extract returns one Dep per entry of `require` and `require-dev`, in file order. Range is
// the constraint as written (RefSpan its bytes), Version the version of its last
// alternative's first lower bound (`^1.0|^2.3` → 2.3), "" without one (`*`, `<2.0`; ReadLocks
// takes the locked version then). Platform packages (`php`, `ext-*`, `lib-*`,
// `composer-plugin-api`: names without a vendor) and constraints outside the grammar
// (`dev-main`, `1.0 as 2.0`, `self.version`) are skipped. The `repositories` decide where a
// package is looked up (repositories.apply). The deps of a `"type": "typo3-cms-extension"`
// are AutoWiden.
func (composerManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))
	x := extractor{file: file, src: src, dec: dec, line: 1}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // a truncated document, not a clean end
		}
		return nil, err
	}
	x.repos.apply(x.deps)
	if x.typ == typeTYPO3Extension {
		for i := range x.deps {
			x.deps[i].AutoWiden = true
		}
	}
	return x.deps, nil
}

// Edits rewrites the constraint for the new version under u.RangeStrategy (newRange).
func (composerManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	out, ok := newRange(d.Range, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("constraint %s cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}

// NewRange implements manager.RangeRewriter (newRange).
func (composerManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return newRange(d.Range, v, strategy)
}

type extractor struct {
	file  string
	src   []byte
	dec   *jsontext.Decoder
	deps  []dep.Dep
	repos repositories
	typ   string // the package `type`

	// line is the 1-based line of offset pos; offsets only grow, so lines are counted
	// incrementally.
	pos, line int
}

func (x *extractor) run() error {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return errors.New("top level is not an object")
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.dec.ReadToken()
		if err != nil {
			return err
		}
		switch key.String() {
		case DepTypeRequire, DepTypeRequireDev:
			err = x.section(key.String())
		case "repositories":
			var v jsontext.Value
			if v, err = x.dec.ReadValue(); err == nil {
				x.repos = parseRepositories(v)
			}
		case "type":
			if x.dec.PeekKind() != '"' {
				err = x.dec.SkipValue()
				break
			}
			var tok jsontext.Token
			if tok, err = x.dec.ReadToken(); err == nil {
				x.typ = tok.String()
			}
		default:
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err = x.dec.ReadToken()
	return err
}

// span returns the content span of the string token s just read, or the zero span when the
// raw bytes differ from it (escapes).
func (x *extractor) span(s string) dep.Span {
	end := int(x.dec.InputOffset())
	start := end - len(s) - 2
	if start >= 0 && x.src[start] == '"' && x.src[end-1] == '"' && string(x.src[start+1:end-1]) == s {
		return dep.Span{Start: start + 1, End: end - 1}
	}
	return dep.Span{}
}

// lineAt returns the 1-based line of offset off (non-decreasing across calls).
func (x *extractor) lineAt(off int) int {
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos = off
	return x.line
}

func (x *extractor) section(depType string) error {
	if x.dec.PeekKind() != '{' {
		return x.dec.SkipValue()
	}
	if _, err := x.dec.ReadToken(); err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.dec.ReadToken()
		if err != nil {
			return err
		}
		name := key.String()
		if x.dec.PeekKind() != '"' {
			if err := x.dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		tok, err := x.dec.ReadToken()
		if err != nil {
			return err
		}
		raw := tok.String()
		span := x.span(raw)
		off := span.Start
		if span == (dep.Span{}) {
			off = int(x.dec.InputOffset())
		}
		d := dep.Dep{
			Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: depType,
			File: x.file, Line: x.lineAt(off), Name: name, Version: raw,
		}
		switch {
		case !strings.Contains(name, "/"):
			d.Skip = skipPlatform
		case span == (dep.Span{}):
			d.Skip = skipEscaped
		default:
			if a, ok := anchor(raw); ok {
				d.Version, d.Range, d.RefSpan = a, raw, span
			} else {
				d.Skip = skipUnsupported
			}
		}
		x.deps = append(x.deps, d)
	}
	_, err := x.dec.ReadToken()
	return err
}
