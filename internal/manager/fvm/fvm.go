// Package fvm extracts the Flutter SDK version FVM (Flutter Version Management) pins for a
// project: `flutter` of `.fvmrc`, or `flutterSdkVersion` of the legacy `.fvm/fvm_config.json`.
package fvm

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"path"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/flutterversion"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "fvm"

// Dep facts: the one dep of a file is the Flutter SDK.
const (
	DepName    = "flutter"
	DepType    = "toolchain"
	Versioning = "semver"
	Datasource = flutterversion.Name
)

// Skip reasons.
const (
	skipChannel     = "unpinned channel"
	skipUnsupported = "unsupported version"
	skipEscaped     = "cannot locate value"
)

// channels are Flutter's release channels, which FVM accepts in place of a version.
var channels = map[string]bool{"stable": true, "beta": true, "dev": true, "master": true, "main": true}

var scheme, _ = versioning.Get(Versioning)

type fvmManager struct{}

// New returns the fvm manager.
func New() manager.Manager { return fvmManager{} }

func (fvmManager) Name() string { return Name }

// Match reports whether the slash path is a .fvmrc or a .fvm/fvm_config.json.
func (fvmManager) Match(p string) bool {
	return path.Base(p) == ".fvmrc" || p == ".fvm/fvm_config.json" || strings.HasSuffix(p, "/.fvm/fvm_config.json")
}

// Extract returns the dep of the top-level `flutter` key, else `flutterSdkVersion` (as FVM
// and Renovate read it); none when neither is a string.
func (fvmManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	if tok.Kind() != '{' {
		return nil, errors.New("top level is not an object")
	}
	var found map[string]dep.Dep
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, eof(err)
		}
		k := key.String()
		if (k != "flutter" && k != "flutterSdkVersion") || dec.PeekKind() != '"' {
			if err := dec.SkipValue(); err != nil {
				return nil, eof(err)
			}
			continue
		}
		v, err := dec.ReadToken()
		if err != nil {
			return nil, eof(err)
		}
		if found == nil {
			found = map[string]dep.Dep{}
		}
		found[k] = value(file, src, int(dec.InputOffset()), v.String())
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, eof(err)
	}
	for _, k := range []string{"flutter", "flutterSdkVersion"} {
		if d, ok := found[k]; ok {
			return []dep.Dep{d}, nil
		}
	}
	return nil, nil
}

// eof turns a clean end of input inside the document into io.ErrUnexpectedEOF.
func eof(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// value returns the dep of the string s whose closing quote ends before offset end.
func value(file string, src []byte, end int, s string) dep.Dep {
	start := end - len(s) - 2
	d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepType, File: file, Name: DepName, Version: s,
		Line: 1 + bytes.Count(src[:max(start, 0)], []byte{'\n'})}
	switch {
	case start < 0 || src[start] != '"' || string(src[start+1:end-1]) != s:
		d.Skip = skipEscaped
	case channels[s]:
		d.Skip = skipChannel
	case !scheme.Valid(s):
		d.Skip = skipUnsupported
	default:
		d.RefSpan = dep.Span{Start: start + 1, End: end - 1}
	}
	return d
}

// Edits replaces the version.
func (fvmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
