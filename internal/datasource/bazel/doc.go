package bazel

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the bazel datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "Bazel module name (`rules_go`); the dep's source is the registry of its `single_version_override` (\"\" = the workspace's registries)",
	Endpoints: "`GET <registry>/modules/<name>/metadata.json`, once per (registry, module); yanked versions (`yanked_versions`) are dropped. " +
		"The registries are the dep's source, else the `--registry` flags of the workspace's `.bazelrc` (the one next to the nearest `MODULE.bazel`; lines without a `:config`, except `startup`; `import`/`try-import` of files in the repository are followed; `%workspace%` is expanded), " +
		"else the Bazel Central Registry (`DEPS_BAZEL_REGISTRY_URL` or `https://bcr.bazel.build`, also where `.bazelrc` names `https://bcr.bazel.build`). " +
		"As in Bazel, the first registry that has the module answers; none → `bazel module <name> not found in <registries>`. " +
		"`file://` registries inside the repository (`file://%workspace%/registry`) are read from disk. " +
		"Source repository (release notes): the first `repository` entry (`github:<owner>/<repo>`, `gitlab:…`, a URL), else a forge `homepage`",
	Auth:         "userinfo of the registry URL as basic auth; registries other than the default get the platform token of their host (Basic `x-access-token`), as `git-tags`",
	ReleaseTimes: "none: registries publish no release times, so `minimumReleaseAge` treats every version as of unknown age (`minimumReleaseAgeBehaviour`)",
}
