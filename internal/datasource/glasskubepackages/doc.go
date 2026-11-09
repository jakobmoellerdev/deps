package glasskubepackages

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the glasskube-packages datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "the Glasskube package name (`cert-manager`); the dep's source is the package repository URL of its `PackageRepository` " +
		"(glasskube manager) or `registryUrl` (custom managers; whitespace-separated URLs are tried in order, the first that has the package wins), " +
		"default `" + DefaultURL + "`. Versioning `glasskube` (`v1.17.0+2`, the revision after the version)",
	Endpoints: "`GET <repository>/<name>/versions.yaml` once per repository and package (redirects are followed: the default repository answers from glasskube.github.io): " +
		"every `versions[].version` is a version, `latestVersion` the release the repository recommends (majors above it are not proposed, as npm's `latest`); " +
		"a repository answering HTTP 404 does not have the package. Source repository (release notes): " +
		"`GET <repository>/<name>/<latestVersion>/package.yaml` once per package, only for versions in a pull request: its `references` entry labeled `GitHub`, else one labeled `Website` on a forge",
	Auth:         "userinfo of the repository URL (`https://user:token@host/…`) as basic auth; redacted in errors and logs",
	ReleaseTimes: "none: versions.yaml has no release times, so `minimumReleaseAge` treats every version as of unknown age (`minimumReleaseAgeBehaviour`)",
}
