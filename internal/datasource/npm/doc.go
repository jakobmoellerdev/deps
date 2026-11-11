package npmds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the npm datasource.
var Doc = refdoc.Datasource{
	Name:         Name,
	NameForm:     "package name (`@scope/name` sent as `@scope%2fname`); yarn 2 and later as the npm manager's `packageManager`, `engines` or `volta` tool is `@yarnpkg/cli-dist` (the `yarn` package stops at 1.x)",
	Endpoints:    "`GET <registry>/<name>` with `Accept: application/vnd.npm.install-v1+json` (abbreviated metadata) once per registry and package, stream-decoded; deprecated versions dropped, their messages kept for the dashboard's deprecated dependencies (a deprecated `dist-tags.latest` deprecates the package, else the installed version's own message counts); 404 → `package <name> not found`. The full packument (`Accept: application/json`) is fetched at most once per registry and package, only for release times and the source repository",
	Auth:         "the `.npmrc` chain: `//host/path/:_authToken=` (longest prefix) as a bearer token, `//host/path/:_auth=` or `//host/path/:username=` with `:_password=` (base64) as basic auth, the top-level `_auth`, `username` and `_password` as a fallback; `${VAR}` expansion",
	ReleaseTimes: "packument `time`; not fetched for `minimumReleaseAge` when the abbreviated `modified` is already older",
	Notes: []refdoc.Section{{
		Title: "npm registry selection",
		Body:  "deps merges the `.npmrc` files of the `package.json` directory up to `--dir` and then the user's (`NPM_CONFIG_USERCONFIG`, else `$HOME/.npmrc`), the nearer file winning per key, as npm merges its configuration.",
		Items: []string{
			"A scoped name uses `@scope:registry=` from the merged configuration; any other name uses `registry=`.",
			"Without one, deps uses `NPM_CONFIG_REGISTRY` (or `npm_config_registry`), else `https://registry.npmjs.org/`.",
		},
	}, {
		Title: "npm majors and `dist-tags.latest`",
		Body:  "npm decides which major line is the legacy one from the abbreviated metadata's `dist-tags.latest` instead of release times, so a major update needs no full packument.",
		Items: []string{
			"A major candidate above the major of latest is rejected as `above latest` (a mistaken or abandoned line such as `9999.9999.9999`, or a major not released as latest yet); it counts against no retry bound.",
			"A major up to latest's is an upgrade even when the current release of an older line is newer (node-fetch 2.7.0 versus latest 3.3.2).",
			"When the current version is above latest (a prerelease or `next` line), or the registry names no latest, majors released before the current version are rejected as `older than current`, as for other datasources.",
		},
	}},
}
