// Package ocb is the ocb manager: the Go modules of OpenTelemetry Collector Builder
// configurations (Renovate's ocb manager).
package ocb

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "ocb"

// Dep types: the collector version of `dist.otelcol_version`, and the component lists.
const (
	DepTypeCollector  = "collector"
	DepTypeConnectors = "connectors"
	DepTypeExporters  = "exporters"
	DepTypeExtensions = "extensions"
	DepTypeProcessors = "processors"
	DepTypeProviders  = "providers"
	DepTypeReceivers  = "receivers"
)

// componentLists are the top-level keys whose `gomod` entries are deps, in Renovate's order;
// the key is the dep type.
var componentLists = []string{DepTypeConnectors, DepTypeExporters, DepTypeExtensions, DepTypeProcessors, DepTypeProviders, DepTypeReceivers}

const (
	// collectorModule is the module whose versions `dist.otelcol_version` writes without "v".
	collectorModule = "go.opentelemetry.io/collector"
	// goExtract maps Go module versions to the versions otelcol_version writes.
	goExtract = `^v(?<version>.+)$`
)

// Skip reasons.
const (
	skipNoVersion = "no version"
	skipReplaced  = "replaced"
	skipLocal     = "local module"
	skipLocate    = "cannot locate value"
)

type ocbManager struct{}

// New returns the ocb manager.
func New() manager.Manager { return ocbManager{} }

func (ocbManager) Name() string { return Name }

// Match reports whether the slash path is a YAML file; Extract only reads those with a
// top-level `dist` mapping and component `gomod` entries, since builder configurations have
// no fixed name (`builder-config.yaml`, `manifest.yaml`, `otelcol-builder.yaml`).
func (ocbManager) Match(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (ocbManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yaml", ".yml"}}
}

// Extract returns the collector version and every component module of a builder
// configuration. Other YAML files yield nothing; they are not parsed unless their bytes
// contain both `dist:` and `gomod:`.
func (ocbManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	if !bytes.Contains(src, []byte("gomod:")) || !bytes.Contains(src, []byte("dist:")) {
		return nil, nil
	}
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, nil //nolint:nilerr // a YAML file that does not parse is no builder configuration
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	dist := yamlspan.Get(doc, "dist")
	if dist == nil || dist.Kind != yaml.MappingNode {
		return nil, nil
	}
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src), replaced: replaces(doc)}
	if v := yamlspan.Str(yamlspan.Get(dist, "otelcol_version")); v != nil && v.Value != "" {
		x.collector(v)
	}
	for _, list := range componentLists {
		for _, e := range yamlspan.Seq(yamlspan.Get(doc, list)) {
			if g := yamlspan.Str(yamlspan.Get(e, "gomod")); g != nil {
				x.component(list, g, yamlspan.Value(yamlspan.Get(e, "path")))
			}
		}
	}
	return x.deps, nil
}

// replaces returns the module paths the `replaces` list replaces (`<module>[ <version>] =>
// <target>`).
func replaces(doc *yaml.Node) map[string]bool {
	var out map[string]bool
	for _, r := range yamlspan.Seq(yamlspan.Get(doc, "replaces")) {
		from, _, ok := strings.Cut(yamlspan.Value(r), "=>")
		if f := strings.Fields(from); ok && len(f) > 0 {
			if out == nil {
				out = map[string]bool{}
			}
			out[f[0]] = true
		}
	}
	return out
}

type extractor struct {
	file     string
	src      []byte
	lines    []int
	replaced map[string]bool
	deps     []dep.Dep
}

// collector adds the dep of `dist.otelcol_version` v: the go.opentelemetry.io/collector
// module, written without "v".
func (x *extractor) collector(v *yaml.Node) {
	d := dep.Dep{
		Manager: Name, Datasource: "go", Versioning: "semver", DepType: DepTypeCollector, File: x.file, Line: v.Line,
		Name: collectorModule, Version: v.Value, ExtractVersion: goExtract,
	}
	if at := yamlspan.Locate(x.src, x.lines, v); at >= 0 {
		d.RefSpan = dep.Span{Start: at, End: at + len(v.Value)}
	} else {
		d.Skip = skipLocate
	}
	x.deps = append(x.deps, d)
}

// component adds the dep of a component entry's `gomod: <module> <version>` g; path is the
// entry's `path`, a local directory the builder replaces the module with.
func (x *extractor) component(depType string, g *yaml.Node, path string) {
	d := dep.Dep{Manager: Name, Datasource: "go", Versioning: "go", DepType: depType, File: x.file, Line: g.Line}
	f := strings.Fields(g.Value)
	if len(f) == 0 {
		return
	}
	d.Name = f[0]
	switch {
	case len(f) < 2:
		d.Skip = skipNoVersion
	case path != "":
		d.Version, d.Skip = f[1], skipLocal
	case x.replaced[d.Name]:
		d.Version, d.Skip = f[1], skipReplaced
	default:
		d.Version = f[1]
		at := yamlspan.Locate(x.src, x.lines, g)
		i := strings.Index(g.Value[len(d.Name):], d.Version)
		if at < 0 || i < 0 {
			d.Skip = skipLocate
			break
		}
		start := at + len(d.Name) + i
		d.RefSpan = dep.Span{Start: start, End: start + len(d.Version)}
	}
	x.deps = append(x.deps, d)
}

// Edits replaces the version token.
func (ocbManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
