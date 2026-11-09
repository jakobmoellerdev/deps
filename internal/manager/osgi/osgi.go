// Package osgi extracts the Maven artifacts of OSGi feature models (Apache Sling's
// `src/main/features/*.json`): `bundles`, the framework of
// `execution-environment:JSON|false` and the artifact list extensions `<name>:ARTIFACTS|…`.
package osgi

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"

	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jsonc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "osgi"

// Dep types.
const (
	DepTypeBundles   = "bundles"
	DepTypeFramework = "framework"
	DepTypeArtifacts = "artifacts"
)

// Skip reasons.
const (
	skipInvalid  = "invalid value"
	skipVariable = "variable reference"
	skipNoVer    = "no version"
	skipLocate   = "cannot locate value"
)

// execEnv is the Sling feature model's execution environment section.
const execEnv = "execution-environment:JSON|false"

type osgiManager struct{}

// New returns the osgi manager.
func New() manager.Manager { return osgiManager{} }

func (osgiManager) Name() string { return Name }

// Match reports whether the slash path is a JSON file below a src/main/features directory
// (Renovate's `(^|/)src/main/features/.+\.json$`).
func (osgiManager) Match(p string) bool {
	i := strings.Index("/"+p, "/src/main/features/")
	if i < 0 {
		return false
	}
	rest := p[i+len("src/main/features/"):]
	return len(rest) > len(".json") && strings.HasSuffix(rest, ".json")
}

// Edits replaces the version.
func (osgiManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// extractor walks a feature model, tracking the line of the last dep for Line.
type extractor struct {
	file      string
	src       []byte
	dec       *jsontext.Decoder
	deps      []dep.Dep
	pos, line int
}

// Extract returns a dep per artifact identifier of `bundles`, the `framework` of
// `execution-environment:JSON|false` and the arrays of `<name>:ARTIFACTS|…` sections; an
// identifier is a string or the `id` of an object. A file whose `feature-resource-version`
// is not 1.x has no deps. Comments and trailing commas (the specification allows JSON
// comments) are blanked first (jsonc.Strip), so offsets are those of src.
func (osgiManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	src = jsonc.Strip(src)
	e := &extractor{file: file, src: src, line: 1,
		dec: jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))}
	if e.dec.PeekKind() != '{' {
		if _, err := e.dec.ReadToken(); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, nil
	}
	if _, err := e.dec.ReadToken(); err != nil {
		return nil, err
	}
	resourceVersion := ""
	for e.dec.PeekKind() != '}' {
		k, err := e.dec.ReadToken()
		if err != nil {
			return nil, err
		}
		key, kind := k.String(), e.dec.PeekKind()
		switch {
		case key == "feature-resource-version" && (kind == '"' || kind == '0'):
			t, err := e.dec.ReadToken()
			if err != nil {
				return nil, err
			}
			resourceVersion = t.String()
			continue
		case key == "bundles" && kind == '[':
			err = e.list(DepTypeBundles)
		case key == execEnv && kind == '{':
			err = e.framework()
		case strings.Contains(key, ":ARTIFACTS|") && kind == '[':
			err = e.list(DepTypeArtifacts)
		default:
			err = e.dec.SkipValue()
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := e.dec.ReadToken(); err != nil {
		return nil, err
	}
	if !supported(resourceVersion) {
		return nil, nil
	}
	return e.deps, nil
}

// supported reports whether a feature-resource-version is absent or 1.x: its first number
// (semver's coerce) must be 1.
func supported(v string) bool {
	if v == "" {
		return true
	}
	i := strings.IndexAny(v, "0123456789")
	if i < 0 {
		return false
	}
	j := i
	for j < len(v) && v[j] >= '0' && v[j] <= '9' {
		j++
	}
	return strings.TrimLeft(v[i:j], "0") == "1"
}

// list reads an array of artifacts.
func (e *extractor) list(depType string) error {
	if _, err := e.dec.ReadToken(); err != nil {
		return err
	}
	for e.dec.PeekKind() != ']' {
		if err := e.artifact(depType); err != nil {
			return err
		}
	}
	_, err := e.dec.ReadToken()
	return err
}

// framework reads the execution environment's `framework` artifact.
func (e *extractor) framework() error {
	if _, err := e.dec.ReadToken(); err != nil {
		return err
	}
	for e.dec.PeekKind() != '}' {
		k, err := e.dec.ReadToken()
		if err != nil {
			return err
		}
		if k.String() == "framework" {
			err = e.artifact(DepTypeFramework)
		} else {
			err = e.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err := e.dec.ReadToken()
	return err
}

// artifact reads an artifact: an identifier string or an object with an `id` string. Other
// values are ignored.
func (e *extractor) artifact(depType string) error {
	switch e.dec.PeekKind() {
	case '"':
		return e.id(depType)
	case '{':
		if _, err := e.dec.ReadToken(); err != nil {
			return err
		}
		for e.dec.PeekKind() != '}' {
			k, err := e.dec.ReadToken()
			if err != nil {
				return err
			}
			if k.String() == "id" && e.dec.PeekKind() == '"' {
				err = e.id(depType)
			} else {
				err = e.dec.SkipValue()
			}
			if err != nil {
				return err
			}
		}
		_, err := e.dec.ReadToken()
		return err
	default:
		return e.dec.SkipValue()
	}
}

// id reads the identifier string `group:artifact[:type[:classifier]]:version` (`/` also
// separates) and appends its dep; an empty identifier is ignored.
func (e *extractor) id(depType string) error {
	raw, err := e.dec.ReadValue()
	if err != nil {
		return err
	}
	end := int(e.dec.InputOffset())
	start := end - len(raw)
	s, exact := string(raw[1:len(raw)-1]), true
	if bytes.IndexByte(raw, '\\') >= 0 {
		u, _ := jsontext.AppendUnquote(nil, raw)
		s, exact = string(u), false
	}
	if s == "" {
		return nil
	}
	e.line += bytes.Count(e.src[e.pos:start], []byte{'\n'})
	e.pos = start
	gav := strings.ReplaceAll(s, "/", ":")
	d := dep.Dep{Manager: Name, Datasource: mavends.Name, Versioning: "maven", DepType: depType, File: e.file, Line: e.line, Name: gav}
	parts := strings.Split(gav, ":")
	switch v := parts[len(parts)-1]; {
	case len(parts) < 3 || len(parts) > 5:
		d.Skip = skipInvalid
	case strings.Contains(v, "${"):
		d.Name, d.Skip = parts[0]+":"+parts[1], skipVariable
	case v == "":
		d.Name, d.Skip = parts[0]+":"+parts[1], skipNoVer
	case !exact:
		d.Name, d.Version, d.Skip = parts[0]+":"+parts[1], v, skipLocate
	default:
		d.Name, d.Version = parts[0]+":"+parts[1], v
		d.RefSpan = dep.Span{Start: end - 1 - len(v), End: end - 1}
	}
	e.deps = append(e.deps, d)
	return nil
}
