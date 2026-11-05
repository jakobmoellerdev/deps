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
		{Name: "image", What: "`image` (string or `name:`) at top level, in `default` and in jobs", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "service", What: "`services[]` entries (string or `name:`) at top level, in `default` and in jobs", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "include", What: "`include` entries with `project` + `ref` (single mapping or list); Source `<instance URL>/<project>`", Versioning: "`tag`", Datasources: []string{"git-tags"}},
		{Name: "component", What: "`include` entries `component: <host>/<project>/<name>@<version>`; Source `<instance URL>/<project>` when `<host>` is the instance host or `$CI_SERVER_FQDN`/`$CI_SERVER_HOST`, else `https://<host>/<project>`", Versioning: "`tag`", Datasources: []string{"git-tags"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image, project, component or ref, other than a leading `$CI_SERVER_FQDN`/`$CI_SERVER_HOST` in a component"},
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
			"The instance URL is `gitlabci.url` from the config, else `CI_SERVER_URL` (set by GitLab CI), else the GitLab platform `deps run` detected from the remote, else `https://gitlab.com`; a path prefix such as `https://example.com/gitlab` is kept.",
			"A leading `$CI_SERVER_FQDN`, `${CI_SERVER_FQDN}`, `$CI_SERVER_HOST` or `${CI_SERVER_HOST}` in a component path is the instance host; the dep name keeps the variable as written.",
			"Private instances authenticate with `DEPS_GITLAB_TOKEN`, or `CI_JOB_TOKEN` when running in GitLab CI against the same instance.",
			"Every YAML document is scanned, so component templates with a `spec:` header work.",
		}},
	},
}
