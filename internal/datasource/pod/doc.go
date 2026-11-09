package pod

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the pod datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "pod name as in the Podfile (`Alamofire`; a subspec `Firebase/Analytics` is looked up as `Firebase`); the dep's source is its whitespace-separated spec repositories in lookup order (\"\" = trunk)",
	Endpoints: "trunk (`trunk`, `https://cdn.cocoapods.org/`, `https://github.com/CocoaPods/Specs.git`) and other http(s) spec repositories serving the CDN layout (an Artifactory `api/pods` repository): `GET <DEPS_POD_URL or https://cdn.cocoapods.org>/all_pods_versions_<a>_<b>_<c>.txt` (`<a><b><c>` the first hex digits of the MD5 of the pod name) once per shard file, a `<pod>/<version>/…` line per pod; " +
		"spec repositories on github.com (`https://github.com/<owner>/<repo>.git`, `git@github.com:<owner>/<repo>.git`): the GitHub contents API listing of `Specs/<a>/<b>/<c>/<pod>`, else `<a>/<b>/<c>/<pod>`, `Specs/<pod>`, `<pod>` (as Renovate), once per repository and pod. The first spec repository listing the pod answers; other git spec repositories are an error",
	Auth:         "the forge token for github.com spec repositories (GitHub API); credentials in a CDN URL (`https://user:token@host/…`) as Basic auth",
	ReleaseTimes: "none (neither layout records dates)",
}
