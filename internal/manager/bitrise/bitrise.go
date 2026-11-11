// Package bitrise extracts the step references of Bitrise configurations (`bitrise.y(a)ml`):
// steps of the step library (`<id>@<version>`, `<steplib>::<id>@<version>`) and git steps
// (`git::<url>@<tag>`).
package bitrise

import (
	"errors"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	bitriseds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/bitrise"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "bitrise"

// DepTypeStep is the dep type of a step reference.
const DepTypeStep = "step"

var (
	// fileRe is Renovate's pattern `(^|/)bitrise\.ya?ml$` for the base name.
	fileRe = regexp.MustCompile(`^bitrise\.ya?ml$`)
	// scpRe is an scp-like git URL `git@<host>:<path>`.
	scpRe = regexp.MustCompile(`^[\w.-]+@([\w.-]+):(.+)$`)
)

type bitriseManager struct{}

// New returns the bitrise manager.
func New() manager.Manager { return bitriseManager{} }

func (bitriseManager) Name() string { return Name }

// Match reports whether path is `bitrise.y(a)ml`, in any directory.
func (bitriseManager) Match(p string) bool { return fileRe.MatchString(path.Base(p)) }

// MatchHints lets discovery skip this manager for paths it cannot match.
func (bitriseManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{"bitrise.yml", "bitrise.yaml"}}
}

// Extract returns the steps of every workflow and step bundle of src, including the steps of
// `with` groups.
func (bitriseManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, nil
	}
	root := doc.Content[0]
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), steplib: yamlspan.Value(yamlspan.Get(root, "default_step_lib_source"))}
	for _, key := range []string{"workflows", "step_bundles"} {
		for _, w := range yamlspan.Items(yamlspan.Get(root, key)) {
			x.steps(yamlspan.Get(w, "steps"))
		}
	}
	return x.deps, nil
}

// Edits rewrites the version after `@`.
func (bitriseManager) Edits(u dep.Update) ([]patch.Edit, error) {
	if u.NewDigest != "" {
		return nil, errors.New("digest pinning of Bitrise steps is not supported")
	}
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

type extractor struct {
	file    string
	src     []byte
	lines   []int
	steplib string // default_step_lib_source, "" = the datasource's default
	deps    []dep.Dep
}

// steps extracts the step list v: every item is a mapping whose key is the step reference.
// `with` groups hold steps of their own; `bundle::<name>` references a step bundle, extracted
// where it is defined.
func (x *extractor) steps(v *yaml.Node) {
	for _, item := range yamlspan.Seq(v) {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			k := item.Content[i]
			switch {
			case k.Kind != yaml.ScalarNode, strings.HasPrefix(k.Value, "bundle::"):
			case k.Value == "with":
				x.steps(yamlspan.Get(item.Content[i+1], "steps"))
			default:
				x.step(k)
			}
		}
	}
}

// step parses the reference in k, as Renovate does: `path::<dir>` is local; `git::<url>@<tag>`
// is a git-tags dep of the repository; `<steplib>::<id>@<version>` and `<id>@<version>` are
// bitrise deps of the step library (default_step_lib_source for the latter).
func (x *extractor) step(k *yaml.Node) {
	ref := k.Value
	d := dep.Dep{Manager: Name, Datasource: bitriseds.Name, Versioning: "tag", DepType: DepTypeStep, File: x.file, Line: k.Line, Name: ref}
	// The version follows the last `@` that is not part of the location (`git@host:path`).
	at := strings.LastIndexByte(ref, '@')
	if at < strings.LastIndexAny(ref, "/:") {
		at = -1
	}
	loc, version := ref, ""
	if at >= 0 {
		loc, version = ref[:at], ref[at+1:]
	}
	switch lib, id, ok := strings.Cut(loc, "::"); {
	case strings.HasPrefix(loc, "path::"):
		d.Name, d.Datasource, d.Skip = strings.TrimPrefix(loc, "path::"), "", "local path"
	case strings.HasPrefix(loc, "git::"):
		d.Datasource = "git-tags"
		url := strings.TrimPrefix(loc, "git::")
		if m := scpRe.FindStringSubmatch(url); m != nil {
			url = "https://" + m[1] + "/" + m[2]
		}
		url = strings.TrimSuffix(url, ".git")
		name, isHTTP := strings.CutPrefix(url, "https://")
		if !isHTTP {
			name, isHTTP = strings.CutPrefix(url, "http://")
		}
		if !isHTTP || !strings.Contains(name, "/") {
			d.Name, d.Skip = url, "invalid step"
			break
		}
		d.Name, d.Source = name, url
	case ok:
		d.Name, d.Source = id, lib
	default:
		d.Name, d.Source = loc, x.steplib
	}
	if d.Skip != "" {
		x.deps = append(x.deps, d)
		return
	}
	off := yamlspan.Locate(x.src, x.lines, k)
	switch {
	case at < 0 || version == "":
		d.Skip = "no version"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		d.Version = version
		d.RefSpan = dep.Span{Start: off + at + 1, End: off + len(ref)}
	}
	if d.Skip != "" {
		d.Source = ""
	}
	x.deps = append(x.deps, d)
}
