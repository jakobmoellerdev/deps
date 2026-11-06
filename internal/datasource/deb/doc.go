package deb

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the deb datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "binary package name (`curl`); the dep's source is one or more whitespace-separated registry URLs " +
		"`<repository>?suite=<suite>&components=<c1,c2>&binaryArch=<arch>` (Renovate's; `release` is an alias of `suite`), " +
		"default `" + DefaultRegistry + "`. Versioning `deb` (dpkg ordering)",
	Endpoints: "`GET <repository>/dists/<suite>/<component>/binary-<arch>/Packages.gz` (a 404 tries `Packages.xz`, then `Packages`), " +
		"once per component index and run, stream-scanned so only the asked-for packages are kept; every paragraph's `Version` is a release. " +
		"Versions of all components and registry URLs are merged; a failing component is skipped when another lists the package",
	Auth:         "userinfo of the registry URL, else the forge token (Basic) on the forge's host, else anonymous",
	ReleaseTimes: "none (package indexes record no dates)",
}
