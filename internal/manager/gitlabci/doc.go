package gitlabci

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gitlabci manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container images, project includes, versioned remote includes and CI/CD components in GitLab CI configuration. Covers both Renovate managers `gitlabci` and `gitlabci-include`.",
	Files: []string{
		"`.gitlab-ci.y(a)ml`, `<x>.gitlab-ci.y(a)ml`",
		"`*.y(a)ml` under `.gitlab/ci/`",
	},
	DepTypes: []refdoc.DepType{
		{Name: "image", What: "`image` (string or `name:`) at top level, in `default` and in jobs", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "service", What: "`services[]` entries (string or `name:`) at top level, in `default` and in jobs", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "include", What: "`include` entries with `project` + `ref` (single mapping or list); Source `<instance URL>/<project>`", Versioning: "`tag`", Datasource: "`git-tags`; `git-refs` when `ref` is a branch", Datasources: []string{"git-tags", "git-refs"}},
		{Name: "remote", What: "`include` entries `remote: <url>` or URL strings naming a ref: GitLab raw files `https://<host>/<project>/-/raw/<ref>/<file>` (Source `https://<host>/<project>`) and `https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<file>` (Source `https://github.com/<owner>/<repo>`); the name is the Source without scheme", Versioning: "`tag`", Datasource: "`git-tags`; `git-refs` when the ref is a branch", Datasources: []string{"git-tags", "git-refs"}},
		{Name: "component", What: "`include` entries `component: <host>/<project>/<name>@<version>`; Source `<instance URL>/<project>` when `<host>` is the instance host or `$CI_SERVER_FQDN`/`$CI_SERVER_HOST`, else `https://<host>/<project>`", Versioning: "`tag`", Datasource: "`git-tags`; `git-refs` when the version is a branch", Datasources: []string{"git-tags", "git-refs"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the image, project, component, remote repository or ref, other than a leading `$CI_SERVER_FQDN`/`$CI_SERVER_HOST` in a component, a Dependency Proxy prefix or a [`registryAliases`](configuration.md#registryaliases) key in an image"},
		{Reason: "no ref", When: "a project include without `ref` (default branch)"},
		{Reason: "no version", When: "a component without `@<version>`"},
		{Reason: "invalid component", When: "a component path with fewer than three segments"},
		{Reason: "latest version", When: "a `~latest` component: GitLab resolves it to the newest catalog release on every pipeline, so there is nothing to bump or pin"},
		{Reason: "pinned without version comment", When: "a 40-hex commit ref not followed by a `# <version or branch>` comment"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned; an image without a tag is its implicit `latest` (see [docker](#docker)), and [`registryAliases`](configuration.md#registryaliases) resolve registry prefixes. The Dependency Proxy prefixes `$CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX/` and `$CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX/` (also `${…}`) are Docker Hub, as in Renovate; a `registryAliases` entry of the same key overrides them.",
			"Local and `template` includes and remote URLs that name no repository ref are not deps.",
			"Include, remote and component refs can be pinned to commits (`pin: digest`): `ref: <commit> # v1.2.3`, `@<commit> # v1.2.3`, `/-/raw/<commit>/… # v1.2.3`. A pinned commit is refreshed from the comment's version tag (git-tags) or branch (`# main`, git-refs); version updates rewrite both. A branch ref (`ref: main`) is pinned to the branch head with `# main` and its digest kept current; unpinned and without `pin: digest` it is skipped as `non-version tag`. A ref inside a flow mapping (`{project: p, ref: v1}`) cannot be pinned: there is no place for the comment.",
			"The instance URL is `gitlabci.url` from the config, else `CI_SERVER_URL` (set by GitLab CI), else the GitLab platform `deps run` detected from the remote, else `https://gitlab.com`; a path prefix such as `https://example.com/gitlab` is kept.",
			"A leading `$CI_SERVER_FQDN`, `${CI_SERVER_FQDN}`, `$CI_SERVER_HOST` or `${CI_SERVER_HOST}` in a component path is the instance host; the dep name keeps the variable as written.",
			"Private instances authenticate with `DEPS_GITLAB_TOKEN`, or `CI_JOB_TOKEN` when running in GitLab CI against the same instance.",
			"Every YAML document is scanned, so component templates with a `spec:` header work.",
		}},
	},
}
