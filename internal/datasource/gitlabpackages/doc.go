package gitlabpackages

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the gitlab-packages datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`<project>:<package>`: the project path or ID and the package name (`gitlab-org/cli:glab`); the registry is the dep's `registryUrl` (whitespace-separated GitLab instance URLs, the first listing the package wins), else the GitLab instance (`gitlabci.url`, `CI_SERVER_URL`, the GitLab platform, default `https://gitlab.com`)",
	Endpoints: "`GET <instance>/api/v4/projects/<project>/packages?package_name=<package>&per_page=100`, every page (`X-Next-Page`), once per registry and package; " +
		"`package_name` is a fuzzy match, so only packages named exactly `<package>` (Conan: `conan_package_name`) count. A registry URL ending in `/api/v4` is used as is",
	Auth:         "the GitLab instance token (`DEPS_GITLAB_TOKEN` as `Authorization: Bearer`, `CI_JOB_TOKEN` as `JOB-TOKEN` on the CI server's host, or the platform token), else the git credential helper after a 401, 403 or 404; public projects need none",
	ReleaseTimes: "the package's `created_at`",
}
