package xcodegen

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const packages = "`packages:` entry `<name>: {url: … | github: owner/repo, "

const versioning = "`semver`; tags are read as SwiftPM reads them: full `MAJOR.MINOR.PATCH[-pre][+build]` versions with an optional leading `v`, which is dropped (the dep's `extractVersion`)"

// Doc is the reference documentation of the xcodegen manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates the Swift package requirements of [XcodeGen](https://github.com/yonaskolb/XcodeGen) project specs: the top-level `packages:` of `project.yml`. " +
		"Only the version string is replaced; the requirement key, quoting and comments are kept. The name is the URL path (`owner/repo`), the source its http(s) URL (https for `ssh://`, `git://` and `git@host:` URLs) without `.git`; `github: owner/repo` is `https://github.com/owner/repo`, and takes precedence over `url:`, as in XcodeGen.",
	Files: []string{"any `project.yml` (specs split with `include:` are read file by file; `project.json` is not read)"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeFrom, What: packages + "from: 1.2.3}`: up to the next major version (`>=1.2.3 <2.0.0`, also for 0.x versions, as SwiftPM)", Versioning: versioning, Datasources: []string{Datasource}},
		{Name: DepTypeMajorVersion, What: packages + "majorVersion: 1.2.3}`: as `from:`", Versioning: versioning, Datasources: []string{Datasource}},
		{Name: DepTypeMinorVersion, What: packages + "minorVersion: 1.2.3}`: up to the next minor version (`>=1.2.3 <1.3.0`)", Versioning: versioning, Datasources: []string{Datasource}},
		{Name: DepTypeExactVersion, What: packages + "exactVersion: 1.2.3}`: exactly that version", Versioning: versioning, Datasources: []string{Datasource}},
		{Name: DepTypeVersion, What: packages + "version: 1.2.3}`: as `exactVersion:`", Versioning: versioning, Datasources: []string{Datasource}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipLocal, When: "`path:` (a local package)"},
		{Reason: skipURL, When: "neither `github:` nor `url:` holds an https/http/ssh/git or `git@host:path` URL with an owner/repo path"},
		{Reason: skipBranch, When: "`branch:`"},
		{Reason: skipRevision, When: "`revision:`"},
		{Reason: skipRange, When: "`minVersion:` with `maxVersion:`, as Renovate"},
		{Reason: skipRequirement, When: "no requirement, or a version that is no full semantic version (`1.2`, which YAML also reads as a number)"},
		{Reason: skipLocate, When: "the version is written with YAML escapes, as a block scalar or over several lines"},
	},
	Sections: []refdoc.Section{
		{Title: "Requirements", Items: []string{
			"The requirement is the first key present in XcodeGen's order: `exactVersion`, `version`, `revision`, `branch`, `minVersion` (with `maxVersion`), `minorVersion`, `majorVersion`, `from`.",
			"`rangeStrategy` (default `auto` = `update-lockfile`, as for the swift manager without `Package.resolved`): only versions outside the range of `from:`, `majorVersion:` and `minorVersion:` are proposed, and replace its version, so a `from:` moves with new major versions. `bump` replaces the version with every newer one. `widen` and `pin` cannot be written without another key, so they are `replace`. " +
				"The `Package.resolved` of the generated project is not read or moved: XcodeGen regenerates the project, and Xcode resolves the packages again.",
		}},
	},
}
