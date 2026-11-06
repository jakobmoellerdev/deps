package actions

import (
	"bytes"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

// DepTypeSetup is the DepType of a version input of a well-known setup action.
const DepTypeSetup = "setup"

// setupInput is the version input of a setup action and the dep it names. partialRange
// reads a partial value (`8.0`) as the range of its line, for a datasource that lists only
// full versions (dotnet-version); java applies the setup-java rules (javaDep).
type setupInput struct {
	input, datasource, name, source string
	partialRange, java              bool
}

// setupActions maps lowercase action names to their version input.
var setupActions = map[string]setupInput{
	"actions/setup-node":            {input: "node-version", datasource: "node-version", name: "node"},
	"actions/setup-go":              {input: "go-version", datasource: "go-toolchain", name: "go"},
	"actions/setup-python":          {input: "python-version", datasource: "python-version", name: "python"},
	"actions/setup-java":            {input: "java-version", datasource: "java-version", name: "java-jdk", java: true},
	"actions/setup-dotnet":          {input: "dotnet-version", datasource: "dotnet-version", name: "dotnet-sdk", partialRange: true},
	"ruby/setup-ruby":               {input: "ruby-version", datasource: "ruby-version", name: "ruby"},
	"azure/setup-helm":              {input: "version", datasource: "github-releases", name: "helm/helm", source: "https://github.com/helm/helm"},
	"azure/setup-kubectl":           {input: "version", datasource: "github-releases", name: "kubernetes/kubernetes", source: "https://github.com/kubernetes/kubernetes"},
	"hashicorp/setup-terraform":     {input: "terraform_version", datasource: "github-releases", name: "hashicorp/terraform", source: "https://github.com/hashicorp/terraform"},
	"golangci/golangci-lint-action": {input: "version", datasource: "github-releases", name: "golangci/golangci-lint", source: "https://github.com/golangci/golangci-lint"},
}

// javaDistributions are the setup-java distributions the java-version datasource (Adoptium's
// Eclipse Temurin releases) versions, as in Renovate (temurin, adopt, and adopt-hotspot, which
// setup-java treats as adopt): other vendors number and ship their builds on their own schedule.
var javaDistributions = map[string]bool{"temurin": true, "adopt": true, "adopt-hotspot": true}

var (
	// fullRe is a complete MAJOR.MINOR.PATCH version, optionally with build metadata
	// (setup-java's `17.0.7+7`); partialRe has one or two parts.
	fullRe    = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:\+[0-9A-Za-z.-]+)?$`)
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
		var skipJava string
		if in.java {
			d.Name, skipJava = javaDep(with)
		}
		switch val := v.Value; {
		case strings.Contains(val, "${{"):
			d.Skip = "expression"
		case skipJava != "":
			d.Skip = skipJava
		case fullRe.MatchString(val):
			d.Versioning = "semver"
		case partialRe.MatchString(val) && !in.partialRange:
			d.Versioning = "tag" // precision match: 26 -> 27, 1.27 -> 1.28
		case val == "" || wordRe.MatchString(val) || strings.HasPrefix(val, "."):
			d.Skip = "non-version value"
		case in.java || strings.Contains(val, "\n"):
			// Renovate leaves java-version ranges alone too; a multi-line value is a list.
			d.Skip = "unsupported range"
		default:
			// An npm range: setup-node, setup-go and setup-python resolve the input with
			// node-semver, setup-dotnet's channels and x-ranges read the same (1.27.x, >=24,
			// ^3.12, 8.0).
			if a, ok := npm.RangeAnchor(val); ok {
				d.Versioning, d.Version, d.Range = "semver", a, val
			} else {
				d.Skip = "unsupported range"
			}
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

// javaDep returns the java-version dep name of a setup-java `with:` mapping (java-jre for a
// `java-package` starting with jre, else java-jdk, as in Renovate), and the skip reason when
// its `distribution` is missing or one the datasource does not version (javaDistributions).
func javaDep(with *yaml.Node) (name, skip string) {
	name = "java-jdk"
	dist := ""
	for i := 0; i+1 < len(with.Content); i += 2 {
		k, v := with.Content[i], with.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "distribution":
			dist = strings.ToLower(v.Value)
		case "java-package":
			if strings.HasPrefix(v.Value, "jre") {
				name = "java-jre"
			}
		}
	}
	if !javaDistributions[dist] {
		return name, "unsupported distribution"
	}
	return name, ""
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
