package jenkins

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the jenkins manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates the Jenkins plugin versions of plugin lists in the input formats of the plugin installation manager tool (`jenkins-plugin-cli --plugin-file`, the Jenkins Docker image's `plugins.txt`). Only the version is rewritten; the file keeps its formatting.",
	Files: []string{
		"`plugins.txt` at any depth: `<artifactId>:<version>` lines, `#` comments",
		"`plugins.yml`, `plugins.yaml` at any depth: the `plugins` list (`artifactId`, `source.version`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypePlugin, What: "a plugins.txt line `git:5.10.1` (a line with only the artifact ID, the latest version, is no dep, as in Renovate); a plugins.yaml entry `{artifactId: git, source: {version: 5.10.1}}`", Versioning: "`maven`", Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipURL, When: "a download URL: `source.url`, or a plugins.txt line `<artifactId>:<version>:<url>`"},
		{Reason: skipUnsupported, When: "the version `latest` or `experimental`, an incrementals version (a `groupId`, or `incrementals;<groupId>;<version>`)"},
		{Reason: skipNoVersion, When: "a plugins.yaml entry without `source.version`"},
		{Reason: skipInvalid, When: "a plugins.yaml version that YAML reads as a number (`version: 2.10` is 2.1; quote it)"},
		{Reason: skipIgnored, When: "a plugins.txt line with a `# renovate:ignore` comment, or a plugins.yaml entry with `renovate: {ignore: true}` (Renovate's)"},
		{Reason: skipLocate, When: "the version bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Versions are looked up with the `jenkins-plugins` datasource on the Jenkins update center (`DEPS_JENKINS_PLUGINS_URL` for a mirror or another update site) and compared as Maven versions, so the incremental `1234.v<commit>` versions of newer plugins order by their build number.",
			"A plain YAML version is replaced as written; a new version that YAML would read as a number is written single-quoted (`'2.10'`).",
			"A plugin's `requiredCore` (the minimum Jenkins version) is not checked against the controller's version, as in Renovate without `constraints.jenkins`.",
		}},
	},
}
