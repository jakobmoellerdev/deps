// Package smithy extracts the Maven dependencies of Smithy build files (`smithy-build.json`):
// the `maven.dependencies` coordinates, looked up in `maven.repositories`.
package smithy

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"slices"
	"strings"

	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "smithy"

// DepTypeMaven is the dep type of the `maven.dependencies` entries.
const DepTypeMaven = "maven"

// Skip reasons.
const (
	skipInvalid  = "invalid dependency specification"
	skipVariable = "variable reference"
	skipNoVer    = "no version"
	skipLocate   = "cannot locate value"
)

type smithyManager struct{}

// New returns the smithy manager.
func New() manager.Manager { return smithyManager{} }

func (smithyManager) Name() string { return Name }

// Match reports whether the slash path is a smithy-build.json.
func (smithyManager) Match(p string) bool {
	return p == "smithy-build.json" || strings.HasSuffix(p, "/smithy-build.json")
}

// Edits replaces the version.
func (smithyManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// extractor walks a smithy-build.json.
type extractor struct {
	file  string
	src   []byte
	dec   *jsontext.Decoder
	deps  []dep.Dep
	repos []string
}

// Extract returns a dep per string of `maven.dependencies`; non-string entries are ignored.
// The deps are looked up in the `url`s of `maven.repositories` (without `${…}`), then Maven
// Central. Comments and trailing commas are blanked first (jsonc.Strip), so offsets are
// those of src.
func (smithyManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	src = jsonc.Strip(src)
	e := &extractor{file: file, src: src,
		dec: jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))}
	if e.dec.PeekKind() != '{' {
		if _, err := e.dec.ReadToken(); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, nil
	}
	if err := e.object(func(key string) error {
		if key == "maven" && e.dec.PeekKind() == '{' {
			return e.object(e.maven)
		}
		return e.dec.SkipValue()
	}); err != nil {
		return nil, err
	}
	if len(e.deps) == 0 {
		return nil, nil
	}
	if len(e.repos) > 0 {
		if !slices.Contains(e.repos, mavends.Central) {
			e.repos = append(e.repos, mavends.Central)
		}
		source := strings.Join(e.repos, " ")
		for i := range e.deps {
			e.deps[i].Source = source
		}
	}
	return e.deps, nil
}

// object reads an object, calling member with the decoder at each member's value; member
// must consume the value.
func (e *extractor) object(member func(key string) error) error {
	if _, err := e.dec.ReadToken(); err != nil {
		return err
	}
	for e.dec.PeekKind() != '}' {
		k, err := e.dec.ReadToken()
		if err != nil {
			return err
		}
		if err := member(k.String()); err != nil {
			return err
		}
	}
	_, err := e.dec.ReadToken()
	return err
}

// array reads an array, calling item with the decoder at each element; item must consume it.
func (e *extractor) array(item func() error) error {
	if _, err := e.dec.ReadToken(); err != nil {
		return err
	}
	for e.dec.PeekKind() != ']' {
		if err := item(); err != nil {
			return err
		}
	}
	_, err := e.dec.ReadToken()
	return err
}

// maven reads a member of the `maven` object.
func (e *extractor) maven(key string) error {
	switch {
	case key == "dependencies" && e.dec.PeekKind() == '[':
		e.deps = e.deps[:0]
		return e.array(func() error {
			if e.dec.PeekKind() != '"' {
				return e.dec.SkipValue()
			}
			raw, err := e.dec.ReadValue()
			if err != nil {
				return err
			}
			e.deps = append(e.deps, e.coordinate(raw, int(e.dec.InputOffset())))
			return nil
		})
	case key == "repositories" && e.dec.PeekKind() == '[':
		e.repos = e.repos[:0]
		return e.array(func() error {
			if e.dec.PeekKind() != '{' {
				return e.dec.SkipValue()
			}
			return e.object(func(key string) error {
				if key != "url" || e.dec.PeekKind() != '"' {
					return e.dec.SkipValue()
				}
				t, err := e.dec.ReadToken()
				if err != nil {
					return err
				}
				if u := t.String(); u != "" && !strings.Contains(u, "${") {
					u = strings.TrimRight(u, "/") + "/"
					if !slices.Contains(e.repos, u) {
						e.repos = append(e.repos, u)
					}
				}
				return nil
			})
		})
	default:
		return e.dec.SkipValue()
	}
}

// coordinate returns the dep of the JSON string raw (with quotes) `group:artifact:version`
// ending at offset end.
func (e *extractor) coordinate(raw jsontext.Value, end int) dep.Dep {
	start := end - len(raw)
	s, exact := string(raw[1:len(raw)-1]), true
	if bytes.IndexByte(raw, '\\') >= 0 {
		u, _ := jsontext.AppendUnquote(nil, raw)
		s, exact = string(u), false
	}
	d := dep.Dep{Manager: Name, Datasource: mavends.Name, Versioning: "maven", DepType: DepTypeMaven, File: e.file,
		Line: 1 + bytes.Count(e.src[:start], []byte{'\n'}), Name: s}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" ||
		strings.Contains(parts[0], "${") || strings.Contains(parts[1], "${") {
		d.Skip = skipInvalid
		if strings.Contains(s, "${") {
			d.Skip = skipVariable
		}
		return d
	}
	d.Name = parts[0] + ":" + parts[1]
	switch {
	case len(parts) == 2 || parts[2] == "":
		d.Skip = skipNoVer
	case strings.Contains(parts[2], "${"):
		d.Skip = skipVariable
	case !exact:
		d.Version, d.Skip = parts[2], skipLocate
	default:
		d.Version = parts[2]
		d.RefSpan = dep.Span{Start: end - 1 - len(d.Version), End: end - 1}
	}
	return d
}
