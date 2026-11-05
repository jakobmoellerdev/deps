package actions

import (
	"bytes"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// DepTypeSetup is the DepType of a version input of a well-known setup action.
const DepTypeSetup = "setup"

// setupInput is the version input of a setup action and the dep it names.
type setupInput struct{ input, datasource, name, source string }

// setupActions maps lowercase action names to their version input. Actions without a
// datasource for their tool (setup-python, setup-java) are not listed.
var setupActions = map[string]setupInput{
	"actions/setup-node":            {"node-version", "node-version", "node", ""},
	"actions/setup-go":              {"go-version", "go-toolchain", "go", ""},
	"azure/setup-helm":              {"version", "github-releases", "helm/helm", "https://github.com/helm/helm"},
	"azure/setup-kubectl":           {"version", "github-releases", "kubernetes/kubernetes", "https://github.com/kubernetes/kubernetes"},
	"hashicorp/setup-terraform":     {"terraform_version", "github-releases", "hashicorp/terraform", "https://github.com/hashicorp/terraform"},
	"golangci/golangci-lint-action": {"version", "github-releases", "golangci/golangci-lint", "https://github.com/golangci/golangci-lint"},
}

var (
	// fullRe is a complete MAJOR.MINOR.PATCH version; partialRe has one or two parts.
	fullRe    = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	partialRe = regexp.MustCompile(`^v?\d+(?:\.\d+)?$`)
	// wordRe is a letter-led value that is not a v-prefixed version (lts/*, stable, latest).
	wordRe = regexp.MustCompile(`^(?:[A-Za-uw-z]|v(?:$|\D))`)
	// markerRe is a custom-manager marker comment line (`# deps: …` or `# renovate: …`).
	markerRe = regexp.MustCompile(`^[ \t]*#[ \t]*(?:deps|renovate):`)
)

// setup extracts the version input of a known setup action from its `with:` mapping.
func (x *extractor) setup(uses string, with *yaml.Node) {
	in, ok := setupActions[strings.ToLower(ParseUses(uses, x.host).Name)]
	if !ok || with.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(with.Content); i += 2 {
		k, v := with.Content[i], with.Content[i+1]
		if k.Value != in.input || v.Kind != yaml.ScalarNode {
			continue
		}
		// A marker above the line hands the value to the custom manager.
		if ls, le := x.line(k.Line - 1); ls >= 0 && markerRe.Match(x.src[ls:le]) {
			return
		}
		d := dep.Dep{
			Manager: Name, Datasource: in.datasource, DepType: DepTypeSetup,
			File: x.file, Line: v.Line, Name: in.name, Source: in.source, Version: v.Value,
		}
		switch val := v.Value; {
		case strings.Contains(val, "${{"):
			d.Skip = "expression"
		case fullRe.MatchString(val):
			d.Versioning = "semver"
		case partialRe.MatchString(val):
			d.Versioning = "tag" // precision match: 26 -> 27, 1.27 -> 1.28
		case val == "" || wordRe.MatchString(val):
			d.Skip = "non-version value"
		default:
			d.Skip = "unsupported range"
		}
		if d.Skip == "" {
			if sp, ok := x.locate(k, v); ok {
				d.RefSpan = sp
			} else {
				d.Skip = "cannot locate value"
			}
		}
		x.deps = append(x.deps, d)
		return
	}
}

// locate finds the bytes of scalar v after its key k on v's line.
func (x *extractor) locate(k, v *yaml.Node) (dep.Span, bool) {
	if v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || k.Line != v.Line {
		return dep.Span{}, false
	}
	ls, le := x.line(v.Line)
	if ls < 0 {
		return dep.Span{}, false
	}
	line := x.src[ls:le]
	ki := bytes.Index(line, []byte(k.Value))
	if ki < 0 {
		return dep.Span{}, false
	}
	off := ki + len(k.Value)
	i := bytes.Index(line[off:], []byte(v.Value))
	if i < 0 {
		return dep.Span{}, false
	}
	s := ls + off + i
	return dep.Span{Start: s, End: s + len(v.Value)}, true
}

// versionInput records the `with: version:` value of the action dep at idx when it equals the
// dep's current version, so the edit bumps it along with the ref. Setup actions own their
// version input, and a marker comment hands the value to the custom manager.
func (x *extractor) versionInput(idx int, with *yaml.Node) {
	if idx < 0 || with.Kind != yaml.MappingNode {
		return
	}
	d := &x.deps[idx]
	if d.Skip != "" || d.Version == "" {
		return
	}
	if _, ok := setupActions[strings.ToLower(d.Name)]; ok {
		return
	}
	for i := 0; i+1 < len(with.Content); i += 2 {
		k, v := with.Content[i], with.Content[i+1]
		if k.Value != "version" || v.Kind != yaml.ScalarNode || v.Value != d.Version {
			continue
		}
		if ls, le := x.line(k.Line - 1); ls >= 0 && markerRe.Match(x.src[ls:le]) {
			return
		}
		if sp, ok := x.locate(k, v); ok {
			d.InputSpan = sp
		}
		return
	}
}
