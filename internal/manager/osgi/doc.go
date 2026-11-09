package osgi

import (
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// idWhat describes the artifact identifier forms.
const idWhat = "artifact identifiers `group:artifact[:type[:classifier]]:version` (`/` also separates), as a string or the `id` of an object, "

// Doc is the reference documentation of the osgi manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Maven artifacts of OSGi feature models (OSGi Compendium R8 159, Apache Sling's feature model), as Renovate's osgi manager. " +
		"Only the version inside the identifier string is rewritten; the file keeps its formatting and comments.",
	Files: []string{"`*.json` below a `src/main/features/` directory at any depth (only files whose `feature-resource-version` is absent or `1.x`)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeBundles, What: idWhat + "in `bundles`", Versioning: "`maven`", Datasources: []string{mavends.Name}},
		{Name: DepTypeFramework, What: idWhat + "in `framework` of `execution-environment:JSON|false`", Versioning: "`maven`", Datasources: []string{mavends.Name}},
		{Name: DepTypeArtifacts, What: idWhat + "in the array of an artifact list extension `<name>:ARTIFACTS|<kind>`", Versioning: "`maven`", Datasources: []string{mavends.Name}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipInvalid, When: "an identifier with fewer than 3 or more than 5 parts"},
		{Reason: skipVariable, When: "a version with a `${…}` variable"},
		{Reason: skipNoVer, When: "an identifier ending with `:`"},
		{Reason: skipLocate, When: "a JSON string with escapes"},
	},
	Sections: []refdoc.Section{
		{Title: "Repositories", Items: []string{"Artifacts are looked up in Maven Central (settings.xml mirrors and repositories apply through the maven datasource)."}},
	},
}
