package nuget

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the nuget datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "package id (case-insensitive, `Newtonsoft.Json`); the dep's source is its whitespace-separated package sources `<key>=<service index URL>` in nuget.config order (the key query-escaped; \"\" = nuget.org)",
	Endpoints: "the feed's v3 service index (`<DEPS_NUGET_URL or https://api.nuget.org/v3/index.json>` for nuget.org), once per feed, for its `RegistrationsBaseUrl` (the SemVer 2.0 `3.6.0` one first) and `PackageBaseAddress/3.0.0`; " +
		"then `GET <registration>/<id lower-case>/index.json`, pages that are not inlined fetched concurrently, once per (feed, package); unlisted versions (`listed: false`, nuget.org's publish year 1900) are left out and `+metadata` dropped. " +
		"A feed without registration is read through its flat container `GET <base>/<id>/index.json` (every version, unlisted ones included). " +
		"The versions of all of a dep's feeds are merged, as NuGet restores from all of them; none has the package → `package <id> not found`. v2 feeds (a URL not ending in `.json`, such as `https://www.powershellgallery.com/api/v2`) are not read; a dep with only v2 feeds is skipped. " +
		"Source repository (release notes): the `<repository url>` of `GET <flat container>/<id>/<version>/<id>.nuspec`, else the registration's forge `projectUrl`, only for versions in a pull request",
	Auth:         "userinfo of the feed URL as basic auth, else NuGet's `NuGetPackageSourceCredentials_<key>` variable of the nuget.config source (`Username=<user>;Password=<token>`), sent to the feed's host only; redacted in errors and logs. nuget.config `packageSourceCredentials` are not read",
	ReleaseTimes: "the registration's `published`; none from a flat container",
}
