package nuget

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const nugetDatasource = "`nuget` (nuget.org, or the nuget.config package sources)"

// Doc is the reference documentation of the nuget manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates NuGet package references, MSBuild project SDKs and .NET tools in MSBuild files, central package management (`Directory.Packages.props`), `global.json` (also the .NET SDK) and `dotnet-tools.json`. " +
		"MSBuild files are scanned with encoding/xml at byte offsets and only the version is rewritten, so attribute order, quotes, comments, whitespace and a UTF-8 byte order mark are kept. NuGet is not run; `packages.lock.json` files are not updated.",
	Files: []string{
		"`*.csproj`, `*.fsproj`, `*.vbproj` and MSBuild imports `*.props`, `*.targets` (`Directory.Build.props`, `Directory.Build.targets`, `Directory.Packages.props`, any other; not the `*.nuget.g.props`/`*.nuget.g.targets` restore generates). A file whose root element is not `<Project>` yields no deps",
		"`global.json` (comments allowed, as the .NET SDK reads it)",
		"`dotnet-tools.json` (the local tool manifest, usually `.config/dotnet-tools.json`)",
		"`nuget.config` (any case: `NuGet.Config`, `NuGet.config`): package sources only, no deps",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNuGet, What: "`PackageReference`, `PackageVersion` (central package management), `GlobalPackageReference`, `PackageDownload` and `DotNetCliToolReference` items in an `<ItemGroup>` (conditions included) with `Include` or `Update` and a `Version` (or `VersionOverride`, which wins) attribute or child element; dotnet-tools.json `tools.<id>.version`", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
		{Name: DepTypeMSBuildSDK, What: "`<Project Sdk=\"Microsoft.Build.NoTargets/3.7.134\">` (each `;`-separated `Name/Version`), `<Sdk Name=\"…\" Version=\"…\" />`, `<Import Project=\"…\" Sdk=\"…\" Version=\"…\" />`, global.json `msbuild-sdks` entries; SDKs without a version ship with the .NET SDK and are not deps", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
		{Name: DepTypeDotnetSDK, What: "global.json `sdk.version` (name `dotnet-sdk`); `rollForward` is kept", Versioning: "`nuget`", Datasources: []string{"dotnet-version"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNoVersion, When: "an empty version (`Version=\"\"`)"},
		{Reason: skipInvalid + " \"<v>\"", When: "a version outside NuGet's version and range grammar (`latest`, `1.0-*-*`, a floating bound inside an interval)"},
		{Reason: skipFloatAll + " <v>", When: "`*` or `*-*`: every version is admitted, so there is nothing to update"},
		{Reason: skipNonLiteral + " <v>", When: "a version that combines properties or text (`$(Major).1.0`, `$(A)$(B)`)"},
		{Reason: skipUndefined + "<name>)", When: "a `$(Name)` version no unconditional `<PropertyGroup>` of the file, of a `Directory.Packages.props` or of a `Directory.Build.props` above it defines"},
		{Reason: skipCycle + "<name>)", When: "a chain of property references deeper than 8"},
		{Reason: skipLocate, When: "a version written with XML entities, CDATA or a comment inside the element"},
		{Reason: skipID, When: "an `Include` that is not one literal package id (`$(Prefix).Core`, `A;B`)"},
		{Reason: skipNoSources, When: "the nuget.config files clear the package sources and add only local folders or `%VAR%` URLs"},
		{Reason: skipUnmapped, When: "`packageSourceMapping` is configured and no pattern matches the package id"},
	},
	Sections: []refdoc.Section{
		{Title: "Versions and ranges", Items: []string{
			"Versioning `nuget` is NuGetVersion: one to four numeric parts (missing ones are 0, `1.2` = `1.2.0.0`), a `-` prerelease compared as SemVer 2.0 (case-insensitive), `+metadata` ignored. Update types follow the first two parts (a third or fourth one is a patch). Prereleases are only proposed from a prerelease.",
			"A bare version (`Version=\"13.0.3\"`, NuGet's minimum version) and an exact range (`[13.0.3]`) are updated as the pinned version they are in practice: the new version replaces the old one (`[13.0.3]` → `[13.0.4]`), as Renovate does.",
			"Floating versions and intervals are ranges, rewritten per [rangeStrategy](configuration.md#rangestrategy) (`auto` is `update-lockfile`; deps does not read `packages.lock.json`, so versions a range admits are not proposed): a floating version moves at its precision when a version leaves it (`13.*` → `14.*`, `1.2.*` → `1.3.*`, `1.0.0-rc*` → `1.1.0-rc*`); an interval gets a new upper bound, the new version when inclusive, else the next value at the position where the bounds differ (`[1.0,2.0)` → `[1.0,3.0)` for 2.5.1, `(,2.0)` → `(,3.0)`), with its lower bound kept. `bump` raises an interval's lower bound to every newer version it admits (`[1.0,2.0)` → `[1.5.0,2.0)`); `pin` writes the bare version. `*` and `*-*` are skipped.",
		}},
		{Title: "Properties", Items: []string{
			"A version written as `$(Name)` (`<PackageVersion Include=\"Serilog\" Version=\"$(SerilogVersion)\" />`) is read from the last definition of `<Name>` in an unconditional top-level `<PropertyGroup>` of the file itself, else of the `Directory.Packages.props`, else of the `Directory.Build.props` of its directory, then of each directory above (MSBuild property names are case-insensitive). The update edits the definition, and deps sharing it are grouped into one PR with one version. Properties inside a `Condition`, `<Choose>` or `<Target>` are not read.",
		}},
		{Title: "Package sources", Items: []string{
			"Packages are looked up on nuget.org unless `nuget.config` files say otherwise: the files of the dep's directory and every directory above it up to the repository root are applied from the root down, as NuGet merges them (nearer files win): `<packageSources>` `<add key value>`, `<remove key>` and `<clear/>` (dropping nuget.org and the sources of files above), `<disabledPackageSources>`, and `<packageSourceMapping>`, under which a package is looked up only on the sources whose patterns hold its most specific match (an exact id over the longest `Prefix.*`, `*` last). The machine and user configurations of the runner are not read.",
			"Local folder sources (`./packages`, `C:\\feed`) and URLs with `%VAR%` references are left out. The versions of all remaining sources are merged, as NuGet restores from all of them; v2 feeds (`…/api/v2`) are not read. Credentials come from `NuGetPackageSourceCredentials_<key>` or the nuget.config `<packageSourceCredentials>` (see [Datasources](#datasources)).",
		}},
		{Title: "global.json and dotnet-tools.json", Items: []string{
			"global.json's `sdk.version` is the .NET SDK (`dotnet-sdk`, datasource `dotnet-version`): updates move it to a newer SDK, feature bands included (`8.0.100` → `8.0.404`, a patch), and to newer majors as major updates; previews only from a preview. Its `msbuild-sdks` (`\"Microsoft.Build.Traversal\": \"4.1.0\"`) are NuGet packages of dep type `msbuild-sdk`.",
			"dotnet-tools.json `tools.<id>.version` entries are NuGet packages of dep type `nuget` (`dotnet-ef`, `csharpier`); the `commands` are kept.",
		}},
	},
}
