package dotnetversion

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the dotnet-version datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "`dotnet-sdk` (the .NET SDK, every feature band) or `dotnet-runtime`",
	Endpoints: "`<DEPS_DOTNET_RELEASES_URL or https://builds.dotnet.microsoft.com/dotnet/release-metadata/releases-index.json>` once per run for the channels (`8.0`, `9.0`, …), then each channel's `releases.json` at most once, " +
		"only the channels at or above the dep's major.minor (older ones cannot hold an update); previews and release candidates are listed (versioning drops them for stable current versions)",
	Auth:         "none",
	ReleaseTimes: "`release-date` of the release that shipped the version",
}
