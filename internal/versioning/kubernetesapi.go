package versioning

// kubernetesAPIExpr is Renovate's kubernetes-api versioning, a regex scheme over
// `[<group>/]v<major>[alpha<n>|beta<n>]`: the API group is the compatibility, so an update stays
// within its group (`apps/v1beta2` → `apps/v1`, never `extensions/v1beta1` → `apps/v1`), and
// alpha and beta versions are prereleases of their major version.
const kubernetesAPIExpr = `^(?:(?<compatibility>\S+)/)?v(?<major>\d+)(?<prerelease>(?:alpha|beta)\d+)?$`

// mustRegexScheme compiles a built-in regex scheme.
func mustRegexScheme(expr string) Scheme {
	s, err := newRegexScheme(expr)
	if err != nil {
		panic(err)
	}
	return s
}
