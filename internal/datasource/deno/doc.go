package deno

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the deno datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`https://deno.land/x/<module>` for a third-party module, `https://deno.land/std` for the standard library (Renovate's form)",
	Endpoints: "`GET <DEPS_DENO_URL or https://cdn.deno.land>/<module>/meta/versions.json` once per module (`std` for the standard library). " +
		"Renovate reads apiland.deno.dev, which no longer resolves; the registry's CDN serves the same version lists. " +
		"Release times and the source repository cost one `GET <base>/<module>/versions/<version>/meta/meta.json` per version, made only when asked: the version's `uploaded_at`, and the GitHub repository the latest version was uploaded from (release notes)",
	Auth:         "none",
	ReleaseTimes: "the `uploaded_at` time of the version's metadata",
}
