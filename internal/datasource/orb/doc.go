package orb

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the orb datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "orb name `<namespace>/<name>` (`circleci/node`)",
	Endpoints: "`GET <DEPS_CIRCLECI_URL or https://circleci.com>/api/v3/orb/packages?filter[name]=<name>` (Renovate's endpoint), once per orb; the entry whose name matches exactly answers. " +
		"After the listed versions a derived `X.Y` and `X` per release line, so partial versions (`@5`) are updated with their precision",
	Auth:         "`DEPS_CIRCLECI_TOKEN` (private orbs) as `Circle-Token` and bare `Authorization`, as Renovate's `Token-Only` host rule",
	ReleaseTimes: "`created_at` of the version; a derived `X.Y`/`X` has the time of the line's first release",
}
