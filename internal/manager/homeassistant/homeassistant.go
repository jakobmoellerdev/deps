// Package homeassistant extracts the Python requirements of Home Assistant integration
// manifests (`manifest.json` with `domain` and `name`): the PEP 508 strings of `requirements`.
package homeassistant

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

// Name is the manager name (Renovate's).
const Name = "homeassistant-manifest"

// DepTypeRequirements is the dep type of the `requirements` entries.
const DepTypeRequirements = "requirements"

// SkipInvalid is the skip reason of a requirement that is no PEP 508 string.
const SkipInvalid = "invalid dependency specification"

type haManager struct{}

// New returns the homeassistant-manifest manager.
func New() manager.Manager { return haManager{} }

func (haManager) Name() string { return Name }

// Match reports whether the slash path is a manifest.json; Extract tells Home Assistant
// manifests from other manifest.json files (web app, browser extension) by their content.
func (haManager) Match(p string) bool {
	return p == "manifest.json" || strings.HasSuffix(p, "/manifest.json")
}

// Edits rewrites the specifier (pyreq.Edits).
func (haManager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (haManager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (haManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return pyreq.NewRange(d, v, strategy)
}

// Extract returns one dep per `requirements` string of a top-level object that has string
// `domain` and `name` members (Renovate's schema); other manifest.json files have no deps.
// Non-string requirements are ignored.
func (haManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))
	if dec.PeekKind() != '{' {
		if _, err := dec.ReadToken(); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, nil
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	var (
		domain, name bool
		deps         []dep.Dep
		pos, line    = 0, 1
	)
	for dec.PeekKind() != '}' {
		k, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		key, kind := k.String(), dec.PeekKind()
		switch {
		case (key == "domain" || key == "name") && kind == '"':
			if key == "domain" {
				domain = true
			} else {
				name = true
			}
		case key == "requirements" && kind == '[':
			if _, err := dec.ReadToken(); err != nil {
				return nil, err
			}
			deps = deps[:0]
			for dec.PeekKind() != ']' {
				isStr := dec.PeekKind() == '"'
				raw, err := dec.ReadValue()
				if err != nil {
					return nil, err
				}
				if !isStr {
					continue
				}
				end := int(dec.InputOffset())
				start := end - len(raw)
				line += bytes.Count(src[pos:start], []byte{'\n'})
				pos = start
				deps = append(deps, requirement(file, raw, start+1, line))
			}
			if _, err := dec.ReadToken(); err != nil {
				return nil, err
			}
			continue
		}
		if err := dec.SkipValue(); err != nil {
			return nil, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	if !domain || !name {
		return nil, nil
	}
	return deps, nil
}

// requirement returns the dep of the JSON string raw (with quotes) whose content starts at
// offset at. A string with escapes is read but cannot be rewritten.
func requirement(file string, raw jsontext.Value, at, line int) dep.Dep {
	s, exact := string(raw[1:len(raw)-1]), true
	if bytes.IndexByte(raw, '\\') >= 0 {
		u, _ := jsontext.AppendUnquote(nil, raw)
		s, exact = string(u), false
	}
	r, ok := pyreq.Parse(s)
	if !ok {
		d := pyreq.NewDep(Name, DepTypeRequirements, file, strings.TrimSpace(s), line)
		d.Skip = SkipInvalid
		return d
	}
	d := pyreq.NewDep(Name, DepTypeRequirements, file, r.Name, line)
	if r.URL != "" {
		d.Skip = pyreq.URLSkip(r.URL)
		return d
	}
	pyreq.SetVersion(&d, r.Spec, at+r.SpecStart, exact, pyreq.PEP440)
	return d
}
