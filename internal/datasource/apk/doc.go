package apk

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the apk datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "the package name (`P:` in APKINDEX, `bash`); custom managers only, with `registryUrl`",
	Endpoints: "`GET <index URL>` of every repository index the dep's `registryUrl` names, once per index and run: " +
		"Renovate's form `<base>?arch=<arch>[&branch=<branch>][&components=<c1>,<c2>]` expands to " +
		"`<base>[/<branch>]/<component>/<arch>/APKINDEX.tar.gz` per component " +
		"(`https://dl-cdn.alpinelinux.org/alpine?branch=v3.22&components=main,community&arch=x86_64`, `https://packages.wolfi.dev/os?arch=x86_64`); " +
		"a URL without query is the index or its directory. Other query parameters are errors; the versions of every index are merged. " +
		"No registry URL is an error (there is no default repository)",
	Auth:         "none",
	ReleaseTimes: "the build time (`t:`) of the version in the index",
}
