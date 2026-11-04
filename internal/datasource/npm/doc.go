package npmds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the npm datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "package name (`@scope/name` sent as `@scope%2fname`)",
	Endpoints:    "`GET <registry>/<name>` with `Accept: application/vnd.npm.install-v1+json`, stream-decoded; deprecated versions dropped; 404 → `package <name> not found`",
	Auth:         "`.npmrc` `//host/path/:_authToken=` (longest prefix), `${VAR}` expansion",
	ReleaseTimes: "packument `time`; not fetched for `minimumReleaseAge` when the abbreviated `modified` is already older",
	Notes: []refdoc.Section{{
		Title: "npm registry selection",
		Body:  "deps reads the nearest `.npmrc` per `package.json` directory, walking up to `--dir`.",
		Items: []string{
			"A scoped name uses `@scope:registry=` from that file; any other name uses `registry=`.",
			"Without one, deps uses `NPM_CONFIG_REGISTRY` (or `npm_config_registry`), else `https://registry.npmjs.org/`.",
		},
	}},
}
