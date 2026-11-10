// Package customdoc declares the identity of the custom manager (its name and dependency
// types) and its reference documentation. It is apart from package custom, which depends on
// config, so that the manager registry, and config through it, can import it.
package customdoc

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Name is the manager name shared by every custom manager.
const Name = "custom"

// Dependency types of custom deps.
const (
	DepTypeAnnotation = "annotation"
	DepTypeRegex      = "regex"
	DepTypeJSONata    = "jsonata"
)

// Doc is the reference documentation of the custom manager (docs/reference/managers.md). Its
// dep types have no Datasources: a custom dep can use any datasource, custom.<name> included.
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "User-defined managers: each `custom[]` entry is one manager instance; see [`custom`](configuration.md#custom).",
	Files:   []string{"`custom[].files` globs"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeAnnotation, What: "`# deps: <datasource> <name>` or `# renovate: datasource=… depName=…` on the previous line", Datasource: "from the marker", Versioning: "from the marker or `custom[].versioning`"},
		{Name: DepTypeRegex, What: "`match` RE2 over the whole file", Datasource: "`custom[].datasource` or the `datasource` group", Versioning: "`custom[].versioning` or the `versioning` group"},
		{Name: DepTypeJSONata, What: "`jsonata` query over the file parsed as `fileFormat` (json, yaml, toml)", Datasource: "`custom[].datasource` or the result's `datasource`", Versioning: "`custom[].versioning` or the result's `versioning`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "no version on next line", When: "an annotation is not followed by a line with a version"},
		{Reason: "renovate marker without datasource", When: "a `# renovate:` marker lacks `datasource=`"},
		{Reason: "renovate marker without depName", When: "a `# renovate:` marker lacks `depName=`"},
		{Reason: "empty version", When: "regex and JSONata mode: the version is empty"},
		{Reason: "empty name", When: "regex and JSONata mode: the name is empty"},
		{Reason: "empty datasource", When: "regex and JSONata mode: the datasource is empty"},
		{Reason: "not a string: <field>", When: "JSONata mode: a result field deps reads is a number, boolean, object or list"},
		{Reason: "version not found in file", When: "JSONata mode: the version is not a substring of the file (escaped, or computed by the query) or the query does not keep it apart from other occurrences"},
		{Reason: "digest not found in file", When: "JSONata mode: as `version not found in file`, for the result's `currentDigest`"},
		{Reason: "checksum is not 64 or 128 hex digits", When: "regex mode: the `checksum` group is not a SHA-256 or SHA-512 hex digest"},
		{Reason: "unknown versioning \"<v>\"", When: "the versioning is not a known scheme"},
		{Reason: "invalid git-tags name \"<name>\"", When: "a `git-tags` dep (also `github-tags`, `bitbucket-tags`, `azure-tags`, `bitbucket-server-tags`) whose name is not a repository URL"},
		{Reason: "invalid git-refs name \"<name>\"", When: "a `git-refs` (`github-digest`) dep whose name is not `owner/repo` or a repository URL"},
		{Reason: "invalid github-releases name \"<name>\"", When: "a `github-releases` dep whose name is not `owner/repo` or a repository URL"},
		{Reason: "invalid helm name \"<name>\"", When: "a `helm` dep without an `https://`, `http://` or `oci://` repository and a chart name"},
		{Reason: "invalid deb name \"<name>\"", When: "a `deb` dep whose package name is empty or holds `/` or blanks"},
		{Reason: "invalid deb registry URL \"<url>\": <why>", When: "a `deb` registry URL that is not `http(s)` or lacks `suite`, `components` or `binaryArch`"},
		{Reason: "invalid artifactory name \"<name>\"", When: "an `artifactory` dep whose registry URL joined with its name (or the name alone, without a registry URL) is no `https://` or `http://` URL"},
		{Reason: "invalid <datasource> name \"<name>\"", When: "a `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases` or `forgejo-tags` dep whose name is not a project path (`owner/repo`; GitLab also `group/subgroup/project`) or a repository URL"},
		{Reason: "invalid <datasource> registry URL \"<url>\"", When: "such a dep or an `endoflife-date`, `galaxy-collection`, `bitrise`, `terraform-provider`, `terraform-module` or `vcpkg` dep whose (first) `registryUrl` is not an `http(s)://` URL, a `maven`, `sbt-package`, `sbt-plugin`, `clojure`, `bazel`, `packagist`, `rubygems` or `nuget` dep with a `registryUrl` that is not one (sbt: also `ivy=<url>`; nuget: also `<key>=<url>`), or a `conda` dep with a `registryUrl` that is no anaconda.org channel"},
		{Reason: "invalid java-version name \"<name>\"", When: "a `java-version` dep whose name is not `java`, `java-jdk` or `java-jre` (optionally with `?os=…&architecture=…`)"},
		{Reason: "invalid dotnet-version name \"<name>\"", When: "a `dotnet-version` dep whose name is not `dotnet-sdk` or `dotnet-runtime`"},
		{Reason: "invalid extractVersion: <err>", When: "a Renovate marker's `extractVersion=` or a JSONata result's `extractVersion` is not an RE2 regexp with a `version` group"},
		{Reason: "unknown datasource \"<ds>\"", When: "the datasource is neither built in nor `custom.<name>` of `customDatasources`"},
	},
	Sections: []refdoc.Section{
		{Title: "Edits", Body: "The version is replaced. A captured digest (`@sha256:` for docker markers, a `digest` group or a JSONata result's `currentDigest`) and a `checksum` group are replaced together with it."},
		{Title: "JSONata mode", Items: []string{
			"The query runs over the parsed file, so its results carry no byte positions. deps finds each version (and digest) where the file spells it: it appends a distinct numeric marker to every occurrence of the value, queries the marked file again and keeps the occurrence whose marker comes back on that result. Values the query takes from a longer string (`$split(\"foo@1.2.3\", \"@\")[1]`, `$substringAfter`) are found too.",
			"JSON allows comments and trailing commas; YAML is the list of the file's documents (as in Renovate: query `packages` maps over every document); TOML is its root table. Keys keep their order, so `$each`, `$keys` and `*` list deps in file order.",
			"A query that returns nothing yields no deps; one that fails, or returns anything but objects, is a file error. Each evaluation is limited to 10 seconds.",
		}},
		{Title: "Helm charts", Items: []string{
			"`# deps: helm <repository>/<chart>` or `# deps: helm <repository> <chart>`, with an `https://` (index.yaml) or `oci://` repository: `# deps: helm https://charts.jetstack.io cert-manager`.",
			"Renovate markers give the repository as `registryUrl=`: `# renovate: datasource=helm depName=redis registryUrl=https://charts.bitnami.com/bitnami`.",
			"Regex mode takes `<repository>/<chart>` as the name; JSONata results may give the repository as `registryUrl` and the chart as the name.",
		}},
		{Title: "Registry URLs", Items: []string{
			"`helm`, `gitlab-packages`, `rpm`, `apk`, `deb`, `artifactory`, `endoflife-date`, `galaxy-collection`, `maven`, `sbt-package`, `sbt-plugin`, `glasskube-packages`, `pod`, `bazel`, `bitrise`, `clojure`, `conda`, `crate`, `nuget`, `packagist`, `rubygems`, `terraform-provider`, `terraform-module`, `vcpkg` and the `gitlab-*`, `gitea-*` and `forgejo-*` releases and tags deps take their registry from `registryUrl`: a Renovate marker's `registryUrl=`, else a regex `(?P<registryUrl>…)` group or a JSONata result's `registryUrl`, else the entry's `registryUrl` field. Other datasources ignore it.",
			"`# renovate: datasource=gitlab-packages packageName=gitlab-org/cli:glab registryUrl=https://gitlab.com` (`<project>:<package>`; without a registry URL the configured GitLab instance).",
			"`# renovate: datasource=rpm depName=golang registryUrl=https://packages.microsoft.com/azurelinux/3.0/prod/base/x86_64/repodata/` (several whitespace-separated repositories are merged); rpm deps use the `rpm` versioning unless the marker, the regex group or the entry sets one.",
			"`# renovate: datasource=apk depName=bash registryUrl=https://dl-cdn.alpinelinux.org/alpine?branch=v3.22&components=main&arch=x86_64` above `RUN apk add bash=5.2.37-r0` (one index per component); apk deps use the `apk` versioning unless the marker, the regex group or the entry sets one.",
			"Artifactory folders: `# renovate: datasource=artifactory depName=tools/cli registryUrl=https://example.jfrog.io/artifactory/generic-local` lists `https://example.jfrog.io/artifactory/generic-local/tools/cli/`; `# deps: artifactory https://example.jfrog.io/artifactory/generic-local/tools/cli` names the folder URL directly.",
		}},
		{Title: "Debian packages (deb)", Items: []string{
			"The registry is Renovate's `<repository>?suite=<suite>&components=<c1,c2>&binaryArch=<arch>`: a marker's `registryUrl=`, a regex `registryUrl` group, `# deps: deb <registry> <package>` or the entry's `registryUrl`, else `https://deb.debian.org/debian?suite=stable&components=main,contrib,non-free&binaryArch=amd64`.",
			"Renovate's Dockerfile example works as is: `# renovate: datasource=deb depName=curl registryUrl=https://deb.debian.org/debian?suite=bookworm&components=main&binaryArch=amd64` above `RUN apt-get install -y curl=7.88.1-10+deb12u12`; the first Debian version on the next line (epoch, `~` and `+` suffixes included) is the version.",
			"Versioning defaults to `deb` unless the marker, the `versioning` group or the entry sets one.",
		}},
		{Title: "GitLab, Gitea and Forgejo releases and tags", Items: []string{
			"`gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases` and `forgejo-tags` take the project path as name and the instance as `registryUrl` (marker field, `custom[].registryUrl` or a `registryUrl` group): `# renovate: datasource=gitlab-releases depName=gitlab-org/cli registryUrl=https://gitlab.example.com`.",
			"Without `registryUrl` they use Renovate's defaults: `https://gitlab.com`, `https://gitea.com` and `https://code.forgejo.org`. A repository URL as name (`https://codeberg.org/forgejo/forgejo`) names the instance too.",
		}},
		{Title: "Galaxy, Maven, sbt and GitHub runners", Items: []string{
			"The datasources of the ansible-galaxy, maven, sbt and github-actions managers work from custom managers too, with the versioning of those managers unless the marker, the `versioning` group or the entry sets one: `galaxy` (`tag`), `galaxy-collection` (`pep440`), `maven` (`maven`), `sbt-package` and `sbt-plugin` (`ivy`), `github-runners` (`docker`).",
			"`galaxy-collection`: `# renovate: datasource=galaxy-collection depName=community.general`; a `registryUrl` is the Galaxy server (only the first is used), else `DEPS_GALAXY_URL` or https://galaxy.ansible.com.",
			"`maven`: `# renovate: datasource=maven depName=org.apache.maven:maven-core`; the `registryUrl`s are the repositories, in order, else Maven Central. settings.xml mirrors and credentials apply to maven and maven-wrapper deps only.",
			"`sbt-package` (`org.typelevel:cats-core_2.13`, with the Scala suffix) and `sbt-plugin` (`org.scalameta:sbt-scalafmt`, looked up for sbt 1.x): `registryUrl`s are Maven repositories, `ivy=<url>` and the sbt plugin releases repository Ivy-layout ones. Without one, `sbt-package` uses Maven Central and `sbt-plugin` the sbt plugin releases repository, then Maven Central, as sbt does.",
			"`github-runners`: the runner name (`ubuntu`, `macos`, `windows`, `xcode`) and its version (`24.04`).",
		}},
		{Title: "Datasources of other managers", Items: []string{
			"Every built-in datasource works from custom managers. Without a versioning from the marker, the `versioning` group or the entry, a dep gets the scheme its native manager uses: `crate` (`cargo`), `conda` (`conda`), `nuget` and `dotnet-version` (`nuget`), `rubygems` and `ruby-version` (`ruby`), `packagist` (`composer`), `julia-registry` (`julia`), `terraform-provider` and `terraform-module` (`hashicorp`), `gradle-version`, `jenkins-plugins` and `clojure` (`maven`), `bazel` (`bazel-module`), `azure-pipelines-tasks`, `orb` and `bitrise` (`tag`), `vcpkg` (the vcpkg manager's port regex, `1.3.1#2`); the others `semver`.",
			"`crate`: `# renovate: datasource=crate depName=serde`; a `registryUrl` is a Cargo registry name (`CARGO_REGISTRIES_<NAME>_INDEX` or `.cargo/config.toml`) or an index URL (`sparse+https://…`), else crates.io.",
			"`rubygems`, `packagist`, `bazel` and `clojure`: the `registryUrl`s are the gem servers, Composer repositories, Bazel registries and Maven repositories in lookup order, else rubygems.org, Packagist, the Bazel Central Registry and Clojars with Maven Central.",
			"`nuget`: `# renovate: datasource=nuget depName=Newtonsoft.Json registryUrl=https://pkgs.dev.azure.com/org/_packaging/feed/nuget/v3/index.json`; the feeds (`<url>` or `<key>=<url>`) are merged, else nuget.org.",
			"`conda`: `registryUrl`s are anaconda.org channels (`https://conda.anaconda.org/bioconda`), else the name's `<channel>/` prefix or conda-forge.",
			"`terraform-provider` (`hashicorp/aws`) and `terraform-module` (`terraform-aws-modules/vpc/aws`): the first `registryUrl` is the registry base URL, else https://registry.terraform.io.",
			"`bitrise`: the first `registryUrl` is the step library, else Bitrise's public steplib. `vcpkg`: the first `registryUrl` is the raw base of a registry tree (its `versions/` database), else microsoft/vcpkg's `master`.",
			"`java-version` takes `java`, `java-jdk` or `java-jre` (with `?os=…&architecture=…`), `dotnet-version` `dotnet-sdk` or `dotnet-runtime`. `azure-pipelines-tasks`, `orb`, `elm-package`, `gradle-version`, `jenkins-plugins`, `julia-registry`, `kubernetes-api`, `ruby-version` and `rust-version` take no `registryUrl`; their `DEPS_*_URL` overrides apply.",
		}},
		{Title: "Release names (extractVersion)", Items: []string{
			"A Renovate marker's `extractVersion=<RE2>` (or a rule's `extractVersion`, which overrides it) maps release names to versions: `# renovate: datasource=github-releases depName=jqlang/jq extractVersion=^jq-(?<version>.+)$`.",
			"Release names the expression does not match are ignored; the extracted version is compared and written. Digests, release times and checksum assets are looked up by the original release name.",
		}},
		{Title: "Precedence over native managers", Body: "When a custom dep and a native manager's dep (docker, helm, …) cover the same version bytes of a file, the custom dep wins: the native dep is skipped as `covered by annotation on line <n>` (regex mode: `covered by custom regex match on line <n>`, JSONata mode: `covered by custom jsonata match on line <n>`), so the bytes are edited once and only one PR is opened. Between custom entries the first one in `custom` wins; a later entry matching the same version is skipped as `covered by custom match on line <n>`, unless it shares the version (below)."},
		{Title: "Shared versions", Body: "A later custom entry whose match covers exactly the same version bytes, with the same datasource and name, and that writes a digest or checksum no earlier entry of that version writes, shares the version: one version pinned with a checksum per platform (`NFPM_VERSION=…` followed by `NFPM_SHA256_LINUX_X86_64=…` and `NFPM_SHA256_LINUX_ARM64=…`, one regex entry per checksum). Both deps are kept, the version is written once and each dep's digest or checksum moves with it. Their updates form one PR with one version (branch `<prefix>group/<name>` unless a rule's `group` applies), and move only together: when one checksum cannot be fetched, none of them moves and the run reports `<k> of <n> deps of <name> in <file> resolved to <version>, the others did not; updated none` besides the failed checksum. A different datasource or name on the same bytes, or an entry writing nothing besides the version, is a duplicate: `covered by custom match on line <n>`."},
	},
}
