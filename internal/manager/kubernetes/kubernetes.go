// Package kubernetes extracts the `apiVersion` of Kubernetes manifests whose kind and API
// version the kubernetes-api datasource lists, as Renovate's kubernetes manager does (the
// manifests' container images are the docker manager's `kubernetes` deps).
package kubernetes

import (
	"bytes"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/kubernetesapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "kubernetes"

// DepTypeAPIVersion is the dep type of a manifest's `apiVersion`.
const DepTypeAPIVersion = "apiVersion"

type k8sManager struct{}

// New returns the kubernetes manager.
func New() manager.Manager { return k8sManager{} }

func (k8sManager) Name() string { return Name }

// Match reports whether the slash path is a YAML file; Extract tells manifests by content.
func (k8sManager) Match(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

// Edits rewrites the apiVersion value.
func (k8sManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Extract returns one dep per YAML document whose top-level `apiVersion` and `kind` are a
// pair kubernetesapi.Known lists: the name is the kind, the version the apiVersion. The keys
// are read from the lines at column 0 of each document (`---` separates them), where only
// top-level keys of a block mapping can start, so nothing is parsed; a document with `{{` is a
// template (Helm charts pick the apiVersion by cluster version there) and has no deps.
func (k8sManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if !bytes.Contains(src, []byte("apiVersion:")) || !bytes.Contains(src, []byte("kind:")) {
		return nil, nil
	}
	var deps []dep.Dep
	var doc document
	line := 0
	off := 0
	if bytes.HasPrefix(src, bom) {
		off = len(bom) // the mark is not part of the first line's key
	}
	for off < len(src) {
		end := bytes.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += off
		}
		line++
		l := src[off:end]
		switch {
		case isSeparator(l):
			deps = doc.dep(file, deps)
			doc = document{}
		case bytes.Contains(l, []byte("{{")):
			doc.template = true
		default:
			doc.key(l, off, line)
		}
		off = end + 1
	}
	return doc.dep(file, deps), nil
}

// bom is the UTF-8 byte order mark, which editors on Windows write before a manifest's first
// byte; the YAML parsers skip it.
var bom = []byte("\ufeff")

// document collects the top-level apiVersion and kind of one YAML document.
type document struct {
	apiVersion, kind string
	start, line      int // offset and line of the apiVersion value
	template         bool
}

// key records l, a line starting at offset off, when it is a top-level apiVersion or kind.
func (d *document) key(l []byte, off, line int) {
	k, v, ok := bytes.Cut(l, []byte(":"))
	if !ok {
		return
	}
	val, at := scalar(v)
	switch string(k) {
	case "apiVersion":
		d.apiVersion, d.start, d.line = val, off+len(k)+1+at, line
	case "kind":
		d.kind = val
	}
}

// dep appends the document's dep to deps when its kind and apiVersion are listed.
func (d *document) dep(file string, deps []dep.Dep) []dep.Dep {
	if d.template || !kubernetesapi.Known(d.kind, d.apiVersion) {
		return deps
	}
	return append(deps, dep.Dep{
		Manager: Name, Datasource: kubernetesapi.Name, Versioning: kubernetesapi.Versioning, DepType: DepTypeAPIVersion,
		File: file, Line: d.line, Name: d.kind, Version: d.apiVersion,
		RefSpan: dep.Span{Start: d.start, End: d.start + len(d.apiVersion)},
	})
}

// scalar returns the plain or quoted scalar after a key's colon and its offset in v; a
// trailing comment and blanks are dropped. Anything else (escapes, flow values) is "".
func scalar(v []byte) (string, int) {
	at := len(v) - len(bytes.TrimLeft(v, " \t"))
	s := bytes.TrimRight(v[at:], " \t\r")
	if i := bytes.Index(s, []byte(" #")); i >= 0 {
		s = bytes.TrimRight(s[:i], " \t")
	}
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s, at = s[1:len(s)-1], at+1
	}
	if bytes.ContainsAny(s, "\"'\\{}[]") {
		return "", 0
	}
	return string(s), at
}

// isSeparator reports whether l is a YAML document marker (`---` or `...`, optionally
// followed by blanks or content after a space).
func isSeparator(l []byte) bool {
	if !bytes.HasPrefix(l, []byte("---")) && !bytes.HasPrefix(l, []byte("...")) {
		return false
	}
	return len(l) == 3 || l[3] == ' ' || l[3] == '\t' || l[3] == '\r'
}
