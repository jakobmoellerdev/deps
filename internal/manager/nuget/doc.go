package nuget

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

const nugetDatasource = "`nuget` (nuget.org, or the nuget.config package sources)"

// Doc is the reference documentation of the nuget manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name: Name,
	Summary: "Updates NuGet package references, MSBuild project SDKs and .NET tools in MSBuild files, central package management (`Directory.Packages.props`), `global.json` (also the .NET SDK) and `dotnet-tools.json`. " +
		"MSBuild files are scanned with encoding/xml at byte offsets and only the version is rewritten, so attribute order, quotes, comments, whitespace and a UTF-8 byte order mark are kept. `packages.lock.json` files are refreshed by `dotnet restore` in `apply` and `run` (see Lockfile below).",
	Files: []string{
		"`*.csproj`, `*.fsproj`, `*.vbproj` and MSBuild imports `*.props`, `*.targets` (`Directory.Build.props`, `Directory.Build.targets`, `Directory.Packages.props`, any other; not the `*.nuget.g.props`/`*.nuget.g.targets` restore generates). A file whose root element is not `<Project>` yields no deps",
		"`global.json` (comments allowed, as the .NET SDK reads it)",
		"`dotnet-tools.json` (the local tool manifest, usually `.config/dotnet-tools.json`)",
		"`nuget.config` (any case: `NuGet.Config`, `NuGet.config`): package sources only, no deps",
		"`packages.config` (any case), the legacy per-project manifest",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNuGet, What: "`PackageReference`, `PackageVersion` (central package management), `GlobalPackageReference`, `PackageDownload` and `DotNetCliToolReference` items in an `<ItemGroup>` (conditions included) with `Include` or `Update` and a `Version` (or `VersionOverride`, which wins) attribute or child element; dotnet-tools.json `tools.<id>.version`; packages.config `<package id=\"…\" version=\"…\">`", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
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
		{Title: "packages.config", Items: []string{
			"Each `<package id version>` of a packages.config is a `nuget` dep; its `version` attribute is replaced (`targetFramework`, `allowedVersions` and `developmentDependency` are kept, `allowedVersions` is not read). The projects (`*.csproj`, `*.fsproj`, `*.vbproj`) in the same directory reference the installed package by its folder, `packages\\<id>.<version>\\`: every such path (`HintPath`, `<Import Project>`, the `EnsureNuGetPackageBuildImports` conditions) of the updated package moves to the new version in the same update, as `Update-Package` does. The assembly version of a `<Reference Include=\"…, Version=…\">` and paths below the package folder (`lib\\net45\\`) are not changed; run `nuget restore` and a build to check that the new version still ships them.",
		}},
		{Title: "Lockfile (`apply`, `run`)", Items: []string{
			"A `packages.lock.json` next to a project (`RestorePackagesWithLockFile`) is its lockfile. A version in any MSBuild file can reach every project below it (Directory.Packages.props, Directory.Build.props, imports), so when an update patches an MSBuild file and the repository holds a lockfile, the repository is mirrored to a temporary directory (every directory recreated, files symlinked, the patched files and the lockfiles copied; `.git`, `bin`, `obj` and `node_modules` left out) and `dotnet restore <project> --use-lock-file -p:RestoreLockedMode=false` runs for each project a patched file reaches: the project holding it, the projects reaching that one through a `<ProjectReference>`, the projects below the directory of a patched `Directory.Build.props`, `Directory.Build.targets` or `Directory.Packages.props`, and the projects importing a patched `.props` or `.targets` (`<Import Project>` paths are read and `$(MSBuildThisFileDirectory)` evaluated; a file whose imports or references deps cannot resolve is restored with every update, and a patched file no project evaluates restores all of them). The lockfiles of the other projects are kept as they are. NuGet re-resolves the projects whose references no longer match their lockfile and keeps the others; the changed lockfiles join the update. Updates of global.json and dotnet-tools.json run nothing.",
			"`dotnet` (an SDK that can restore the projects' target frameworks) must be on `PATH`, e.g. from `actions/setup-dotnet`; package sources and credentials are NuGet's own (nuget.config, `NuGetPackageSourceCredentials_<key>`). Without it the update fails with `dotnet not found (needed to update packages.lock.json)`; a failed restore reports `dotnet restore in <dir>: <last 20 output lines>` and the update's changes are dropped. A lockfile moved with `NuGetLockFilePath` is not found. `plan` does not run dotnet, and prints `note    packages.lock.json changes are computed by apply/run`.",
		}},
		{Title: "Lock file maintenance (`run`)", Body: "With [lockFileMaintenance](configuration.md#lockfilemaintenance) enabled, every `packages.lock.json` next to a scanned project is refreshed with `dotnet restore --use-lock-file --force-evaluate` (same mirror and options), which moves floating versions (`13.*`) and ranges to the newest versions they admit, transitive packages included. " +
			"All lockfiles share the PR on `<branchPrefix>lock-file-maintenance`; `minimumReleaseAge` does not apply to it."},
	},
}

// CakeDoc is the reference documentation of the cake manager (docs/reference/managers.md).
var CakeDoc = refdoc.Manager{
	Name: CakeName,
	Summary: "Updates the NuGet packages Cake scripts install: the package URIs of `#addin`, `#tool`, `#module` and `#load` directives and of `InstallTool`/`InstallTools` calls. " +
		"Only the `version` value of the URI is rewritten. The script is lexed as C# for comments and strings, so commented-out directives and URIs inside other strings are not deps; Cake is not run.",
	Files: []string{
		"`*.cake`",
		"`nuget.config` (any case): package sources only, no deps (shared with the nuget manager's reading of it)",
	},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeAddin, What: "`#addin nuget:?package=Cake.FileHelpers&version=6.1.3` (also quoted: `#addin \"nuget:…\"`)", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
		{Name: DepTypeTool, What: "`#tool nuget:…`, `#tool dotnet:…`, and every `\"nuget:…\"`/`\"dotnet:…\"` string argument of an `InstallTool(…)` or `InstallTools(…)` call", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
		{Name: DepTypeModule, What: "`#module nuget:…`", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
		{Name: DepTypeLoad, What: "`#load nuget:…` and its short form `#l nuget:…` (`#load` of a local `.cake` path is no dep)", Datasource: nugetDatasource, Versioning: "`nuget`", Datasources: []string{"nuget"}},
	},
	Skips: []refdoc.Skip{
		{Reason: skipNoVersion, When: "a URI without `version` (Cake installs the newest version) or with an empty one"},
		{Reason: skipInvalid + " \"<v>\"", When: "a `version` outside NuGet's version and range grammar"},
		{Reason: skipFloatAll + " <v>", When: "`version=*`"},
		{Reason: skipLocate, When: "a `version` written with URL escapes (`%2B`, `+`)"},
		{Reason: skipCakeSource, When: "a source that is not an http(s) URL (`nuget:file:///packages?package=…`, a local path)"},
		{Reason: skipNoSources, When: "a URI without source whose nuget.config files clear the package sources and add only local folders or `%VAR%` URLs"},
		{Reason: skipUnmapped, When: "a URI without source, with `packageSourceMapping` configured and no pattern matching the package id"},
	},
	Sections: []refdoc.Section{
		{Title: "Package URIs", Items: []string{
			"A package URI is `nuget:` or `dotnet:`, an optional source, then the query: `package` is the package id, `version` its version (other parameters such as `prerelease`, `include` or `loaddependencies` are kept). A directive counts at the start of a line, its argument unquoted (up to the first blank) or as one quoted string; `InstallTool(s)` arguments are plain C# string literals, at any nesting inside the call.",
			"Versions follow the [nuget manager](#nuget) (versioning `nuget`): a bare version is replaced, as Cake installs exactly that version; floating versions and intervals are ranges rewritten per [rangeStrategy](configuration.md#rangestrategy).",
			"A URI with an http(s) source (`nuget:https://api.nuget.org/v3/index.json?package=…`) is looked up on that feed alone (v2 feeds are not read, see [Datasources](#datasources)); without a source (`nuget:?package=…`) the package sources of the nuget.config files above the script apply as for the [nuget manager](#nuget), nuget.org by default.",
		}},
	},
}
