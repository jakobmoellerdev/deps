package smithy

import (
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the smithy manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Maven dependencies of Smithy build files (`smithy-build.json`), as Renovate's smithy manager. " +
		"Only the version inside the coordinate string is rewritten; the file keeps its formatting.",
	Files: []string{"`smithy-build.json` at any depth"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeMaven, What: "every string `group:artifact:version` of `maven.dependencies`; non-string items are ignored", Versioning: "`maven`", Datasources: []string{mavends.Name}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipInvalid, When: "a coordinate without group or artifact, or with more than 3 parts"},
		{Reason: skipVariable, When: "a coordinate with a `${…}` variable"},
		{Reason: skipNoVer, When: "`group:artifact` without a version"},
		{Reason: skipLocate, When: "a JSON string with escapes"},
	},
	Sections: []refdoc.Section{
		{Title: "Repositories", Items: []string{
			"Dependencies are looked up in the `url`s of `maven.repositories` (in order, URLs with `${…}` ignored), then Maven Central; without repositories only in Maven Central.",
		}},
	},
}
