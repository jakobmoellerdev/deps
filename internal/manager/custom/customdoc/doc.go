// Package customdoc declares the identity of the custom manager (its name and dependency
// types) and its reference documentation. It is apart from package custom, which depends on
// config, so that the manager registry, and config through it, can import it.
package customdoc

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Name is the manager name shared by every custom manager.
const Name = "custom"

// Dependency types of custom deps.
const (
	DepTypeAnnotation = "annotation"
	DepTypeRegex      = "regex"
)

// Doc is the reference documentation of the custom manager (docs/reference/managers.md). Its
// dep types have no Datasources: a custom dep can use any datasource, custom.<name> included.
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "User-defined managers: each `custom[]` entry is one manager instance; see [`custom`](configuration.md#custom).",
	Files:   []string{"`custom[].files` globs"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeAnnotation, What: "`# deps: <datasource> <name>` or `# renovate: datasource=… depName=…` on the previous line", Datasource: "from the marker", Versioning: "from the marker or `custom[].versioning`"},
		{Name: DepTypeRegex, What: "`match` RE2 over the whole file", Datasource: "`custom[].datasource` or the `datasource` group", Versioning: "`custom[].versioning` or the `versioning` group"},
	},
	Skips: []refdoc.Skip{
		{Reason: "no version on next line", When: "an annotation is not followed by a line with a version"},
		{Reason: "renovate marker without datasource", When: "a `# renovate:` marker lacks `datasource=`"},
		{Reason: "renovate marker without depName", When: "a `# renovate:` marker lacks `depName=`"},
		{Reason: "empty version", When: "regex mode: the version is empty"},
		{Reason: "empty name", When: "regex mode: the name is empty"},
		{Reason: "empty datasource", When: "regex mode: the datasource is empty"},
		{Reason: "checksum is not 64 or 128 hex digits", When: "regex mode: the `checksum` group is not a SHA-256 or SHA-512 hex digest"},
		{Reason: "unknown versioning \"<v>\"", When: "the versioning is not a known scheme"},
		{Reason: "invalid git-tags name \"<name>\"", When: "a `git-tags` dep whose name is not a repository URL"},
		{Reason: "invalid github-releases name \"<name>\"", When: "a `github-releases` dep whose name is not `owner/repo` or a repository URL"},
		{Reason: "invalid helm name \"<name>\"", When: "a `helm` dep without an `https://`, `http://` or `oci://` repository and a chart name"},
		{Reason: "invalid extractVersion: <err>", When: "a Renovate marker's `extractVersion=` is not an RE2 regexp with a `version` group"},
		{Reason: "unknown datasource \"<ds>\"", When: "the datasource is neither built in nor `custom.<name>` of `customDatasources`"},
	},
	Sections: []refdoc.Section{
		{Title: "Edits", Body: "The version is replaced. A captured digest (`@sha256:` for docker markers, or a `digest` group) and a `checksum` group are replaced together with it."},
		{Title: "Helm charts", Items: []string{
			"`# deps: helm <repository>/<chart>` or `# deps: helm <repository> <chart>`, with an `https://` (index.yaml) or `oci://` repository: `# deps: helm https://charts.jetstack.io cert-manager`.",
			"Renovate markers give the repository as `registryUrl=`: `# renovate: datasource=helm depName=redis registryUrl=https://charts.bitnami.com/bitnami`.",
			"Regex mode takes `<repository>/<chart>` as the name.",
		}},
		{Title: "Registry URLs", Items: []string{
			"`helm` and `gitlab-packages` deps take their registry from `registryUrl`: a Renovate marker's `registryUrl=`, else a regex `(?P<registryUrl>…)` group, else the entry's `registryUrl` field. Other datasources ignore it.",
			"`# renovate: datasource=gitlab-packages packageName=gitlab-org/cli:glab registryUrl=https://gitlab.com` (`<project>:<package>`; without a registry URL the configured GitLab instance).",
		}},
		{Title: "Release names (extractVersion)", Items: []string{
			"A Renovate marker's `extractVersion=<RE2>` (or a rule's `extractVersion`, which overrides it) maps release names to versions: `# renovate: datasource=github-releases depName=jqlang/jq extractVersion=^jq-(?<version>.+)$`.",
			"Release names the expression does not match are ignored; the extracted version is compared and written. Digests, release times and checksum assets are looked up by the original release name.",
		}},
		{Title: "Precedence over native managers", Body: "When a custom dep and a native manager's dep (docker, helm, …) cover the same version bytes of a file, the custom dep wins: the native dep is skipped as `covered by annotation on line <n>` (regex mode: `covered by custom regex match on line <n>`), so the bytes are edited once and only one PR is opened. Between custom entries the first one in `custom` wins; a later entry matching the same version is skipped as `covered by custom match on line <n>`."},
	},
}
