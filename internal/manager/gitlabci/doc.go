package gitlabci

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gitlabci manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images, project includes and CI/CD components in GitLab CI configuration. Covers both Renovate managers `gitlabci` and `gitlabci-include`.",
	Files: []string{
		"`.gitlab-ci.y(a)ml`, `<x>.gitlab-ci.y(a)ml`",
		"`*.y(a)ml` under `.gitlab/ci/`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`image` (string or `name:`) at top level, in `default` and in jobs", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "service", What: "`services[]` entries (string or `name:`) at top level, in `default` and in jobs", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "include", What: "`include` entries with `project` + `ref` (single mapping or list); Source `https://gitlab.com/<project>`", Datasource: "`git-tags`", Versioning: "`tag`"},
		{Name: "component", What: "`include` entries `component: <host>/<project>/<name>@<version>`; Source `https://<host>/<project>`", Datasource: "`git-tags`", Versioning: "`tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image, project, component (e.g. `$CI_SERVER_FQDN`) or ref"},
		{Reason: "no ref", When: "a project include without `ref` (default branch)"},
		{Reason: "no version", When: "a component without `@<version>`"},
		{Reason: "invalid component", When: "a component path with fewer than three segments"},
		{Reason: "non-version ref", When: "a branch or `~latest` instead of a version tag"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "no tag", When: "an image without tag"},
		{Reason: "digest without tag", When: "an image pinned by digest only"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned.",
			"String includes and `local`, `remote` and `template` includes are not deps.",
			"Project includes always resolve against gitlab.com; self-managed instances are not detected.",
			"Every YAML document is scanned, so component templates with a `spec:` header work.",
		}},
	},
}
