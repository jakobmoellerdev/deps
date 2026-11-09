package ocb

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the ocb manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Go modules of [OpenTelemetry Collector Builder](https://github.com/open-telemetry/opentelemetry-collector/tree/main/cmd/builder) (ocb) configurations: " +
		"the component `gomod` entries and `dist.otelcol_version`.",
	Files: []string{"any `*.yaml` / `*.yml` with a top-level `dist` mapping (`builder-config.yaml`, `manifest.yaml`, …); other YAML files yield nothing. Renovate's ocb manager matches no file by default (`managerFilePatterns: []`)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeCollector, What: "`dist.otelcol_version` (older builders), the version of `go.opentelemetry.io/collector` without `v`", Datasource: "`go` with extractVersion `^v(?<version>.+)$`", Versioning: "`semver`", Datasources: []string{"go"}},
		{Name: DepTypeConnectors, What: "`connectors[].gomod: <module> <version>`", Versioning: "`go`", Datasources: []string{"go"}},
		{Name: DepTypeExporters, What: "`exporters[].gomod` (Renovate's dep type `exports`)", Versioning: "`go`", Datasources: []string{"go"}},
		{Name: DepTypeExtensions, What: "`extensions[].gomod`", Versioning: "`go`", Datasources: []string{"go"}},
		{Name: DepTypeProcessors, What: "`processors[].gomod`", Versioning: "`go`", Datasources: []string{"go"}},
		{Name: DepTypeProviders, What: "`providers[].gomod` (confmap providers)", Versioning: "`go`", Datasources: []string{"go"}},
		{Name: DepTypeReceivers, What: "`receivers[].gomod`", Versioning: "`go`", Datasources: []string{"go"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNoVersion, When: "a `gomod` value without a version"},
		{Reason: skipLocal, When: "the entry has a `path`: the builder replaces the module with that directory"},
		{Reason: skipReplaced, When: "the module is replaced in the `replaces` list (`<module> => ../local`), as the gomod manager skips replaced requirements"},
		{Reason: skipLocate, When: "the value is written with escapes or as a block scalar"},
	},
	Sections: []refdoc.Section{
		{Title: "Updates", Items: []string{
			"Only the version token of a `gomod` value or `otelcol_version` is replaced; the module path and the rest of the file are kept.",
			"The collector's core and contrib modules are released together; group them with a rule (`names: ['go.opentelemetry.io/collector**', 'github.com/open-telemetry/opentelemetry-collector-contrib/**'], group: otelcol`) to bump a distribution in one PR.",
			"`dist.version` (the distribution's own version) is no dependency; Renovate's `bumpVersion` for it has no deps equivalent.",
		}},
	},
}
