# deps design

`deps` is a dependency updater written in Go and intended to replace Renovate. This document holds the complete design. It covers GitHub/Gitea/Forgejo Actions `uses:` references, Go modules (requirements and `go`/`toolchain` directives), container images, npm `package.json` dependencies, Helm charts, Rust crates in `Cargo.toml`, Python requirements (`requirements*.txt`, `pyproject.toml`, `Pipfile`), Maven poms, Gradle builds and version catalogs, the Gradle and Maven wrappers, custom annotation/regex dependencies, OSV security updates, and a PR bot for GitHub, Gitea and Forgejo with groups and schedules.

## 1. Goals

- **One static Go binary.** GitHub, Gitea and Forgejo are first-class platforms and are detected automatically.
- **A small, strict config (`.deps.yaml`).** Unknown keys are errors. Rules are evaluated top-to-bottom; each later matching rule overrides only the fields it sets.
- **Files are never reformatted.** Managers extract byte spans and deps replaces only those bytes. The only exceptions are generated files: `go mod tidy` output (go.mod, go.sum), the `package-lock.json` npm writes the `Cargo.lock` cargo writes, the regenerated `uv.lock`, `poetry.lock` and `Pipfile.lock`, and pip-compile/`uv pip compile` output regenerated from its header command.
- **Same behavior locally and in CI.** `deps plan`, `deps apply` and `deps run` behave identically on a laptop and as an Action.
- **Fast and memory-bounded.** Memory grows with the number of dependencies, not the size of the repo (see section 3).

## 2. Pipeline

```mermaid
flowchart LR
    D[discover<br/>one WalkDir, pruned] --> E[extract<br/>managers emit Dep + byte spans]
    E --> R[resolve<br/>datasources, deduped, concurrent]
    R --> P[policy<br/>rules top-to-bottom]
    P --> B[build changes<br/>byte patch, then post-processors<br/>go mod tidy, npm/Python lockfile, Chart.lock, Cargo.lock]
    B --> O{output}
    O -->|plan| O1[unified diff]
    O -->|apply| O2[write files]
    O -->|run| O3[branch + PR per dependency or group]
```

### Managers

Every discovered file goes to every manager whose `Match` is true. Each dep carries `Manager`, `DepType`, `Datasource` and `Versioning`.

| Manager | Files | DepTypes | Datasource / versioning |
|---|---|---|---|
| `github-actions` | `.github`/`.gitea`/`.forgejo` `workflows`/`actions` and `workflow-templates` YAML, `action.y(a)ml` | `action`, `setup` | `git-tags` / `tag`; setup inputs per action (below) |
| `gomod` | `go.mod` | `direct`, `indirect`, `go`, `toolchain` | `go` / `go`; directives `go-toolchain` / `tag` |
| `docker` | `Dockerfile`, `Containerfile` (also `Dockerfile.x`, `x.Dockerfile`), Compose files (`(docker-)compose[.<x>].y(a)ml`, `<x>.compose.y(a)ml`, other YAML shaped like Compose), workflows, `action.y(a)ml`, other `*.y(a)ml` (Kubernetes, Helm values) | `dockerfile`, `compose`, `workflow`, `action`, `kubernetes`, `helm` | `docker` / `docker` |
| `npm` | `package.json` | `dependencies`, `devDependencies`, `optionalDependencies`, `peerDependencies`, `packageManager`, `engines`, `volta` | `npm` / `semver`; node in `engines`/`volta` `node-version` |
| `pip_requirements` | `requirements*.txt` (Renovate's pattern) | `requirements` | `pypi` / `pep440` |
| `pep621` | `pyproject.toml` | `project.dependencies`, `project.optional-dependencies`, `dependency-groups`, `tool.uv.dev-dependencies`, `tool.pdm.dev-dependencies` | `pypi` / `pep440` |
| `poetry` | `pyproject.toml` | `dependencies`, `dev-dependencies`, `group` | `pypi` / `pep440` |
| `pipenv` | `Pipfile` | `packages`, `dev-packages`, `category` | `pypi` / `pep440` |
| `helm` | `Chart.yaml`, `helmfile.y(a)ml`, `helmfile.d/*.y(a)ml` (each optionally `.gotmpl`), other `*.y(a)ml` (chart resources) | `chart`, `helmfile`, `k0s`, `flux`, `argocd` | `helm` / `semver` |
| `cargo` | `Cargo.toml` | `dependencies`, `dev-dependencies`, `build-dependencies`, `workspace.dependencies`, `patch`, `replace` | `crate` / `cargo`; `git` + `tag` `git-tags` / `tag` |
| `terraform` | `*.tf`, `*.tofu`, `*.tf.json`, `*.tofu.json` (not below `.terraform/`) | `required_provider`, `provider`, `module`, `required_version`, `helm_release`, `tfe_workspace`, `docker_*`, `kubernetes_*` | `terraform-provider` / `hashicorp`; registry modules `terraform-module` / `hashicorp`, git modules `git-tags` / `semver`; `required_version`, `tfe_workspace` `github-releases` / `hashicorp`; `helm_release` `helm` / `semver`; images `docker` / `docker` |
| `terraform-version` | `.terraform-version`, `.opentofu-version` | `terraform-version`, `opentofu-version` | `github-releases` / `hashicorp` |
| `tflint-plugin` | `.tflint.hcl` | `plugin` | `github-releases` / `semver` |
| `terragrunt` | `terragrunt.hcl` (not below `.terragrunt-cache/`) | `github`, `gitTags`, `terragrunt` | git `git-tags` / `semver`; `tfr://` `terraform-module` / `hashicorp` |
| `maven` | `pom.xml`, `*.pom.xml`, `.mvn/extensions.xml`, `.mvn/maven.config` | `compile`, `provided`, `runtime`, `test`, `system`, `import`, `parent`, `build` | `maven` / `maven` |
| `gradle` | `*.versions.toml`, `*.gradle`, `*.gradle.kts`, `gradle.properties`, `buildSrc`/convention-plugin `.kt`/`.groovy` sources | `dependencies`, `plugin` | `maven` / `maven` |
| `gradle-wrapper` | `gradle/wrapper/gradle-wrapper.properties` | `wrapper` | `gradle-version` / `maven` |
| `maven-wrapper` | `.mvn/wrapper/maven-wrapper.properties` | `maven`, `wrapper` | `maven` / `maven` |
| `custom` | `custom[].files` globs | `annotation`, `regex` | per entry or per match |

**github-actions.** `uses:` values outside `with:`/`env:`. Skips: `local action` (`./`, `$/`), `docker reference (docker manager)`, `unrecognized uses`, `non-version ref`, `pinned without version comment`, `cannot locate value`.

**Actions setup inputs.** For `uses:` of a well-known setup action the version input in `with:` is a dep (DepType `setup`, manager github-actions): actions/setup-node `node-version` (node-version datasource, name node), actions/setup-go `go-version` (go-toolchain, go), azure/setup-helm `version` (github-releases helm/helm), azure/setup-kubectl `version` (kubernetes/kubernetes), hashicorp/setup-terraform `terraform_version` (hashicorp/terraform), golangci/golangci-lint-action `version` (golangci/golangci-lint). setup-python/setup-java are not extracted (no datasource). Only literal values: full `[v]X.Y.Z` uses semver versioning (the `v` style is kept: `version: 3.16.0` becomes `3.17.0`), partial `26`/`1.27`/`v2.1` uses tag versioning (precision kept: 26 → 27). Skips: `expression` (`${{ }}`), `non-version value` (lts/*, stable, latest), `unsupported range` (1.27.x, >=24, block scalars), `cannot locate value`. The span is the value bytes (quoted or plain, flow mappings too). If the line above the input is a `# deps:`/`# renovate:` marker, the input is left to the custom manager (no double extraction).

**gomod.** Every `require` plus the `go` and `toolchain` directives (the `toolchain` span covers only the version after `go`; a require also records the module path span). Skips: `replaced`, `vendored module (not supported)`, `prerelease toolchain`. By default `indirect` requirements and the `go` directive are ignored; a rule with `ignore: false` opts in. `toolchain` is updated by default. `go mod tidy` runs after any gomod change.
- **Pseudo-versions** (`v0.0.0-<timestamp>-<commit>`) track a commit (Renovate's gomod digest updates). A release newer than the pseudo-version (above its base: `v1.2.4` for `v1.2.4-0.<ts>-<rev>`) is a version update; otherwise the module's `@latest`, when it is a newer pseudo-version of the same major, is a `digest` update (`NewDigest` = the commit). `updates: [digest]`, `ignore` and the digest type's `minimumReleaseAge` (against the commit time) apply. A module with releases whose pseudo-version is ahead of them gets none, since `@latest` is the release.
- **Major module paths** (`gomodUpdateImportPaths`, default on; Renovate's postUpdateOption of that name). For `<path>/v<N>` (also unsuffixed v0/v1 modules, `+incompatible` majors and `gopkg.in/x.vN`) the successive paths `/v<N+1>`, … are listed from the GOPROXY URLs until one has no releases (at most 5, deduped per path, only when major updates are allowed for the dep). The newest accepted, old-enough stable release per path is a `major` update in the major PR bucket (`Update.NewName` = the new path). Its edits rewrite the require's path and version and, through `manager.FileEditor`, the import path literals of the old module and its packages in the module's `.go` files (vendor, testdata, `.`/`_` dirs and nested modules skipped). The PR body warns that the new API may break the build.

**docker.**
- Dockerfile: every `FROM` (case-insensitive, `--flag`s skipped, line continuations and the `# escape=` directive honored). `scratch` and earlier stage names (`AS <name>`) are not dependencies.
- Compose: `services.*.image` (through `<<:` merges and aliases; anchored values once, at the anchor), `image` in `x-*` extension fields, `build.additional_contexts` `docker-image://` entries; `${VAR:-default}`/`${VAR-default}` defaults are updated in place, other interpolation is skipped as `interpolated image`.
- Workflows: `jobs.*.container` (string or `.image`), `jobs.*.services.*.image`, `uses: docker://…` steps. Actions: `runs.image: docker://…` (when `runs.using: docker`) and `uses: docker://…` in `runs.steps`.
- References are parsed by `internal/imageref` (docker.io/library defaults, oras-go `properties.NewReference` for splitting and validation); `Name` is the familiar name (`postgres`), the datasource uses the canonical one. Skips: `variable reference` (`$`), `interpolated image` (Compose), `cannot locate value`, `invalid reference`, `no tag`, `digest without tag`.
- Digests: `image:tag@sha256:…` is refreshed in place (tag and digest). With `pin: digest`, `@sha256:<index digest>` is appended after the tag. Digests come from a HEAD of the tag's manifest, so multi-platform images get the index digest. Credentials come from the Docker config and credential helpers.
- Non-version tags (`latest`, `bookworm`, `ubuntu-26.04`, `stable`, commit hashes such as `3a22eba`) are extracted too; the engine only maintains their digest (section 4, Resolve order). This also covers Helm `tag: latest@sha256:…`.

**Dockerfile stage references and ARGs.** Besides `FROM`, the docker manager extracts `COPY --from=<image>` and `RUN --mount=…,from=<image>`; stage names, numeric stage indexes and `scratch` are not images. A reference that is exactly `$X`/`${X}` resolves to `ARG X=<image>` declared before the first `FROM` or in the current stage; the dep sits at the ARG default (once per ARG), so the update rewrites the ARG line. Defaults that are themselves variable or quoted with escapes are skipped as `variable reference`.

**Kubernetes and Helm YAML.** Every other `*.ya?ml` file is a candidate but is only parsed when its bytes contain `image:` or `repository:`; documents are decoded one by one and a parse error (typically Helm templates) yields no deps and no error.
- `kubernetes`: scalar `image` in `containers`/`initContainers`/`ephemeralContainers` items anywhere.
- `helm`: `*image` mappings with `repository` + `tag` (optional `registry` prefix; digest from `tag: x@sha256:…` or a sibling `digest:`); the tag scalar is rewritten in place.
- Values containing `{{` are skipped as `template value`.

**npm.** `package.json` is streamed with `encoding/json/jsontext` tokens (duplicate names are accepted, as npm's `JSON.parse` does); `InputOffset` after each string value gives its byte span, which must equal the quoted value in the file (otherwise, e.g. with `\u` escapes, `cannot locate value`). Sections: `dependencies`, `devDependencies`, `optionalDependencies`, `peerDependencies` (the section name is the `DepType`) and `packageManager` (`npm@x`, `pnpm@x`, `yarn@x`; `Name` is the tool). `overrides`, `resolutions` and `bundleDependencies` are not extracted; `node_modules` is pruned by discovery.
- **Range grammar:** an optional operator `^`, `~`, `>=` or `=`, then a full `MAJOR.MINOR.PATCH[-prerelease]`. `Version` is the bare version, `Range` the value as written (dependency sections only), and `RefSpan` covers only the version bytes, so the operator is kept when a range changes.
- **rangeStrategy** (Renovate's `auto`, `bump`, `update-lockfile`; top level and per rule, a pre-policy field): `auto` = `update-lockfile`, as Renovate's npm manager resolves it for non-peer simple ranges.
  - `update-lockfile`: candidates are ranked against `Locked`; one the range admits (Masterminds constraint of `Range`) is a lockfile-only update (`Update.Lockfile`, no manifest edit, `FromTo` starts at the locked version), one outside it replaces the range's version (`^5.2.8` → `^6.0.0`, Renovate's `replace`). Without a lock, in-range candidates are rejected (`admitted by range <r> (no lockfile update)`), as Renovate's current version is then the highest in-range release. peerDependencies never get lockfile-only updates (Renovate widens them; deps leaves in-range versions alone and replaces otherwise). `LockUnread` (pnpm, yarn, v1 package-lock) falls back to `bump`, except for peers.
  - `bump`: candidates are ranked against the range's version, which every update raises (`^1.2.3` → `^1.4.0`).
- **Locked versions** (`manager.LockReader`, after `Link`): for each lock root (the post-processor's scope, below) whose lockfile is a `package-lock.json` v2/v3, the file is stream-decoded once, keeping only the `packages` entries of the extracted names (`<member>/node_modules/<name>`, then the hoisted `node_modules/<name>`; links skipped) as `Dep.Locked`. Other lockfiles set `Dep.LockUnread`; a parse error is a run error and also sets `LockUnread`. OSV is queried with `Locked` when set, and security fixes are computed from it (section 5).
- **Skips:** `npm alias` (`npm:`), `local dependency` (`file:`, `link:`, `workspace:`, `./`, `../`, `/`, `~/`), `git dependency` (`github:`, `gitlab:`, `bitbucket:`, `gist:`, `git+`, `git://`, `git@`, `owner/repo[#ref]`), `url dependency` (`http(s)://` tarballs), `tag` (dist-tags such as `latest`, `next`), `unsupported range` (`||`, spaces, `x`/`*` wildcards, partial versions, `<`, hyphen ranges, `v1.2.3`, empty), `cannot locate value`; for `packageManager` also `packageManager with hash` (`+sha512…` suffix) and `unsupported packageManager`.
- **engines and volta:** `engines.{node,npm,pnpm,yarn}` (DepType `engines`, ignored by default; enable with a rule `depTypes: [engines], ignore: false`) and `volta.{node,npm,pnpm,yarn}` (DepType `volta`, updated). node uses the node-version datasource, the others npm. The range grammar is that of dependencies; additionally an optional ^/~/>=/= plus a MAJOR[.MINOR] partial (`^26`, `10.2`) uses tag versioning on the numeric part (^26 → ^27). Other keys and non-string values are ignored.
- **peerDependencies** are ignored by default, like indirect Go requirements; a rule with `depTypes: [peerDependencies]` and `ignore: false` opts in.
- **Lockfile:** after a package.json change, `apply`/`run` regenerate the workspace's `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock`; lockfile-only updates install the new versions into package-lock.json alone (see *Post-processors* below). Dependencies naming a member of the manifest's own workspace are never updated (any range form).

**Python.** `pip_requirements` reads `requirements*.txt`; `pep621` reads `pyproject.toml` `[project] dependencies`, `[project.optional-dependencies]`, PEP 735 `[dependency-groups]`, `[tool.uv] dev-dependencies` and `[tool.pdm.dev-dependencies]`, honoring `[tool.uv.sources]`/`[[tool.uv.index]]` and `[[tool.pdm.source]]` (the `pypi`-named source replaces PyPI, the others become `ExtraSources`; `include_packages`/`exclude_packages` select per dep; `find_links` ignored); `poetry` reads `[tool.poetry.dependencies]`, `dev-dependencies` and `group.<name>.dependencies` with `[[tool.poetry.source]]`; `pipenv` reads `Pipfile` `[packages]`, `[dev-packages]`, custom categories (every other top-level table but `source`, `requires`, `pipenv`, `scripts`; DepType `category`, since rule `depTypes` is a fixed enum) and `[[source]]`. Every specifier is a range (`pyreq`): PEP 440 clauses separated by commas (Poetry also `^`, `~`, `=`, bare versions, space-separated clauses and `||` alternatives; Pipenv bare versions). `Range` is the specifier as written, `RefSpan` its bytes and `Version` its anchor (the first clause that is not `<`, `<=` or `!=`, else the first upper bound); only unparseable specifiers are skipped as `unsupported range`, and only-`!=` ones as `unconstrained`. The managers implement `manager.RangeUpdater` (admission via `versioning.PEP440Admits`, rewrite via `pyreq.NewRange`) and `manager.LockReader` (`pytool.ReadLocks`: `uv.lock`, `poetry.lock`, `pdm.lock` `[[package]]` and the dep's `Pipfile.lock` section), so rangeStrategy works as for npm: under `update-lockfile`/`auto` admitted versions are lockfile-only updates (none without a lock, e.g. requirements files), others rewrite the range with Renovate's pep440 `replace` rules; `bump` raises the lower bound (Renovate's `bump` rules), and an admitted version a range cannot be raised for is handled as under `update-lockfile`. Edits rewrite only the changed clause versions (and `>`→`>=`) or append a `||` alternative, so operators, spacing, extras and markers are kept. `--hash` requirements are updated like the others; their hashes are post-processed (see *Post-processors*). `requires-python` and Poetry's `python` are not extracted. After a change, `apply`/`run` regenerate `uv.lock`, `poetry.lock`, `pdm.lock` or `Pipfile.lock` (see *Post-processors*).

**helm.** Deps have `Name` = chart, `Source` = repository URL (`https://…` index repository or `oci://host/path`), `RefSpan` = the version scalar.
- `Chart.yaml` (DepType `chart`): `dependencies[]` `name`/`version`/`repository`. Skips: `local chart` (empty or `file://` repository), `repository alias` (`@name`, `alias:name`: resolved from the user's Helm repository config, which deps does not read), `unknown chart repository <repo>`.
- helmfile (DepType `helmfile`): `releases[].chart` is `<repo>/<name>`, resolved through `repositories[]` of any document of the file (`oci: true` prefixes `oci://`), or `oci://host/path/name`. Skips: `local chart` (`./x`, `../x`, `/x`, no `/`), `unknown chart repository <repo>`, `invalid chart reference`. In `.gotmpl` files every line containing `{{` is blanked (bytes replaced by spaces, offsets unchanged) before parsing, so templated values are not deps.
- Only exact versions (`[v]MAJOR.MINOR.PATCH[-pre][+build]`) are updated; Helm treats anything else as a constraint (`1.2` means `1.2.x`), skipped as `version range`. Also `no version`, `template value`, `cannot locate value`.
- `Chart.lock` is kept in sync by the helm post-processor (below).

**cargo.** `Cargo.toml` is parsed with `internal/tomlspan` (shared with the python managers): a TOML 1.0 tree (plus TOML 1.1 multi-line inline tables) that keeps every value's byte span and line and whether the bytes equal the decoded string (`Exact`).
- **Tables:** `[dependencies]`, `[dev-dependencies]`, `[build-dependencies]` (plus the legacy `dev_dependencies`/`build_dependencies`), the same below `[target.<spec>]`, `[workspace.dependencies]`, every `[patch.<source>]` (DepType `patch`) and `[replace]` (DepType `replace`; the `name:version`/`name@version` key names the crate). An entry may be a string, an inline table, dotted keys (`serde.version = "1"`) or a `[dependencies.serde]` table; entries are ordered by line, so the same crate under several targets is several deps. `package = "<crate>"` renames are looked up by the crate name. `[features]` is not extracted.
- **Requirements** (`req.go`): Cargo's grammar of comma-separated comparators (`^`, `~`, `=`, `>`, `>=`, `<`, `<=` or none, spaces allowed after the operator) on `MAJOR[.MINOR[.PATCH[-pre][+build]]]`, plus wildcards (`*`, `1.*`, `1.2.x`). `Range` is the requirement as written and `RefSpan` covers all of it; `Version` is the first lower bound's version (the wildcard's prefix for `1.*`). `Dep.Constraint` is the Masterminds form when it differs (a bare `1.2` is `^1.2`; Masterminds reads it as `1.2.x`), which the engine uses for `rangeStrategy`. `Edits` re-renders the requirement: lower bounds take the new version at their written precision, an upper bound `<B` the new version reaches moves to the next compatibility boundary above it (with enough parts to be exact: `<0.6` for 0.5.1), `<=B` to the new version; spacing and operators are kept.
- **Versioning `cargo`** (`internal/versioning/cargo.go`): 1–3 numeric parts, prerelease/build only after three. Candidates need the current precision, so a partial requirement is compared against the release lines the `crate` datasource derives (`0.4` → `0.5`, `1` → `2`), a locked full version against full versions. Update types follow Cargo: leaving the compatibility range of the leftmost non-zero part is `major` (`0.4.1` → `0.5.0`, `0.0.3` → `0.0.4`), a minor change of `1.x+` is `minor`, anything else `patch`; the major label (separateMultipleMajor buckets) is that range (`1`, `0.5`, `0.0.3`).
- **rangeStrategy** reuses npm's mechanism: `ReadLocks` (`manager.LockReader`) reads the Cargo.lock of each manifest's workspace root once and sets `Dep.Locked` to the highest registry package of the crate the requirement admits (an unreadable lock is a run error and sets `LockUnread`). Under `update-lockfile`/`auto` in-range versions are lockfile-only updates from `Locked`, newer ones rewrite the requirement; `bump` rewrites for every newer version. A requirement without a floor (`*`, `<2`, `<=1.5`) takes `Locked` as its `Version` (skipped as `no Cargo.lock version for a requirement without a floor` without one) and is treated as `update-lockfile` under `bump` too, since the engine finds that its constraint admits 0.0.0.
- **Registries:** `registry = "<name>"` and `registry-index = "<url>"` set `Source`; the datasource resolves the index (below).
- **Skips:** `workspace dependency` (`workspace = true`, inherited; the `[workspace.dependencies]` entry is updated), `path dependency` (also with `version`), `git dependency` (no `tag`, or a non-`http(s)` URL), `no version`, `unsupported requirement` (outside the grammar: `~>1`, `1.2 - 2.0`, `v1`, `>=1.*`, empty), `cannot locate value` (escaped strings).
- **git dependencies** with `git = "https://…"` and `tag` are `git-tags` deps (`Source` = the URL without query, fragment, trailing `/` and `.git`; `tag` versioning), also in `[patch]` and `[replace]`.
- **Monorepos:** crates of one repository (tokio, serde, tracing, clap, wasm-bindgen, futures-rs, tonic (grpc-rust), prost, opentelemetry-rust, axum, aws-sdk-rust, smithy-rs, rand; checked against each crate's crates.io `repository`) are grouped like the npm and Go monorepos.
- **Lockfile:** `apply`/`run` update the workspace's `Cargo.lock` with cargo (see *Post-processors* below).
**maven.** The pom is decoded with `encoding/xml` tokens into an element tree; `InputOffset` around each character-data token gives the span of an element's text, which must equal the decoded text (otherwise, with entities, CDATA or text split by a comment, `cannot locate value`). Deps are the versioned `<dependency>` (in any `<dependencies>`, incl. `<dependencyManagement>` and `<profiles>`; DepType = `<scope>`, default `compile`; below a `<plugin>` `build`), `<plugin>` (default groupId `org.apache.maven.plugins`), `<extension>`, `<annotationProcessorPaths><path>` (all `build`) and `<project><parent>` (`parent`); elements without `<version>` are managed by a parent or BOM and not deps. `Name` is `groupId:artifactId` (with `${…}` from the properties and `project.groupId`/`project.artifactId`).
- **Properties:** a version `${name}` follows `${a}` → `${b}` chains. Lookup order: `.mvn/maven.config` `-D` defines of the multi-module root (they override pom properties, as on the command line), the properties of the active profiles (`-P` in maven.config, `<activation>` by `<property>` against the config defines and by `<file><exists>/<missing>` via `manager.Env.Exists`; `activeByDefault` when none is active; JDK/OS activation is never evaluated), the pom's `<properties>`, then the in-repository parent chain (`<relativePath>`, default `../pom.xml`, else matching coordinates). `RefSpan`, `File` and `Line` point at the definition, `VersionRef` names it. Skips: `unresolved property ${x}`, `property ${x} not defined in this repository` (it lives in a remote parent POM), `property ${x} only defined in profiles`, `unresolved property in <name>`, `unsupported version <v>`, `version range`, `cannot locate value`, `m2e lifecycle mapping (not a published plugin)`.
- **Repositories:** `Source` is `[<id>=]<url>/` per repository: `<repositories>` (`<pluginRepositories>` for plugins and extensions) of the pom and its in-repository parents, plus `central=https://repo.maven.apache.org/maven2/` unless a repository is Central or has id `central`; "" = Central. The maven datasource applies settings.xml.
- **Modules:** the manager is a `Linker`: every pom's own `groupId:artifactId` is recorded, and deps naming one are skipped as `internal module` (multi-module SNAPSHOTs).

**gradle.** Version catalogs are scanned with a hand-written TOML subset scanner that keeps byte offsets (`[versions]`, `[libraries]`, `[plugins]`, `version.ref`; rich versions are `rich version`), and catalogs created in settings (`versionCatalogs { create("libs") { version(…); library(…).versionRef(…) } }`) the same way. Build scripts, `buildSrc` and convention-plugin sources are scanned outside comments: string notation in the common configurations and `platform(…)`, map notation (`group: 'g', name: 'n', version: 'v'`, Kotlin named arguments), `plugins { id("x") version "y" }`/`kotlin("jvm")`, and `g:a:v` literals in `buildSrc`. Variables: literal definitions in `gradle.properties`, `def`/`ext`/`ext {}`/`extra[…]`/`val`/`const val`/`by extra(…)`, Groovy and Kotlin maps; references `$x`, `${x}`, `${project.x}`, `${rootProject.extra["x"]}`, `${property("x")}`, `${Versions.x}` and bare Kotlin identifiers resolve in Link (same file, then same and parent directories up to the build root, then root `gradle/*.gradle`, then `buildSrc`); the dep moves to the definition with `VersionRef`, else `unresolved variable <x>` / `version variable`. Repositories come from `pluginManagement`/`dependencyResolutionManagement` in settings and `buildscript`/project/`allprojects` blocks (with `content {}` and `exclusiveContent` filters), else Central, Google Maven and the plugin portal (plugins: the portal, then Google Maven). Plugins are named by their id; the maven datasource looks up the marker `<id>:<id>.gradle.plugin`. Locks: a `manager.LockReader` reads `gradle.lockfile` (also legacy `gradle/dependency-locks/*.lockfile`, `buildscript-gradle.lockfile`, `settings-gradle.lockfile`) into `Dep.Locked`; dynamic versions (`1.+`, `[1.0,2.0)`, `latest.release`) get a `Dep.Range` and use the npm rangeStrategy machinery (in-range candidates are lockfile-only; bump acts like update-lockfile because the selector has no floor; edits keep the form: `1.+` → `2.+`). Without a lock they are `dynamic version <v>`.

**gradle-wrapper.** `distributionUrl` in `gradle/wrapper/gradle-wrapper.properties` (`https\://…/gradle-<v>-(bin|all).zip`, any host); with `distributionSha256Sum` the checksum source is `<url with {{version}}>.sha256`, so version and sum change together. `apply`/`run` regenerate the wrapper jar and scripts (post-processors below).

**maven-wrapper.** `.mvn/wrapper/maven-wrapper.properties`: `distributionUrl` (`org.apache.maven:apache-maven`, DepType `maven`) and `wrapperVersion`/`wrapperUrl` (`org.apache.maven.wrapper:maven-wrapper`, DepType `wrapper`). Pinned `distributionSha256Sum`/`wrapperSha256Sum` use a checksum source with `Artifact`: Maven Central publishes only `.sha1`/`.sha512`, so the new artifact is downloaded and hashed (sha256 or sha512 by the pinned length, 256 MiB cap, once per URL).

**terraform.** A hand-written HCL scanner (`internal/manager/terraform/hcl.go`) parses bodies into attributes and blocks; it skips every expression token by token (quoted templates with `${}`/`%{}` nesting and `$${` escapes, heredocs, `#`/`//`/`/* */` comments, brackets) and only interprets string literals (with their byte span; escaped literals have none) and object constructors. No HCL library: `hashicorp/hcl/v2` would pull in go-cty and friends for three attributes. JSON configuration (`*.tf.json`, `*.tofu.json`) is streamed with `jsontext` into the same tree (label counts per block type, object members as attrs and nested blocks, template strings non-literal), so every extraction below applies to it too.
- `terraform { required_version }` (DepType `required_version`, github-releases `hashicorp/terraform`, `opentofu/opentofu` in `*.tofu`) and `required_providers` entries (`name = { source, version }` or legacy `name = "<constraint>"` = `hashicorp/<name>`; DepType `required_provider`, `Name` `namespace/type`, `Source` `https://<host>`). Hostless addresses use `terraform.defaultRegistry`, else `registry.terraform.io` (`registry.opentofu.org` in `*.tofu`), passed as `manager.Env.TerraformRegistry`.
- `module` blocks (DepType `module`): registry sources `[host/]ns/name/provider[//subdir]` with `version` (terraform-module); git sources (`git::https|ssh://…`, `git@host:path`, `github.com/…`, `bitbucket.org/…`, each with `//subdir` and `?ref=`) via git-tags over `https://host/path`, the `ref` being the version (semver, partial refs tag).
- Legacy `provider "<name>" { version }` (DepType `provider`); `helm_release` (`helm`; Name chart, Source repository or the `oci://` path); `tfe_workspace` `terraform_version`; images of `docker_image`/`docker_container`/`docker_service`/data `docker_registry_image` and of the kubernetes workload resources' `container`/`init_container` blocks (DepType = resource type), parsed and edited through `docker.ParseRef` and the docker manager's edits (digest-capable).
- **Constraints:** every constraint of the Terraform grammar is `Dep.Range`; `Version` is its first version. Exact, `~>`, `>=` and `>` single clauses get a `RefSpan` on that version; upper bounds, exclusions and comma lists are `RangeFixed` (never rewritten). `rangeStrategy` applies through `versioning.RangeMatcher` (the `hashicorp` scheme's go-version semantics): `auto` is `replace` for terraform and terragrunt (`engine.autoRange`); `update-lockfile` yields lockfile-only updates from `Dep.Locked`, which `ReadLocks` (`manager.LockReader`) fills from the sibling `.terraform.lock.hcl` (entries matched by `namespace/type`); fixed ranges only get lockfile-only updates (`bump` acts as `update-lockfile` for them). Edits write the new version at the old token's precision (`versioning.FitHashicorp`).
- **terraform-version** reads the first non-empty line of `.terraform-version` (`hashicorp/terraform`) or `.opentofu-version` (`opentofu/opentofu`); tfenv/tofuenv keywords are `non-version value`. **tflint-plugin** reads `plugin "<name>" { source = "github.com/o/r", version }` (github-releases `o/r`, other hosts `https://host/o/r`); plugins without `source` are bundled. **terragrunt** reads `terraform { source }`: git sources like modules (DepType `github` on github.com, `gitTags` elsewhere), `tfr://[host]/ns/name/provider?version=` (DepType `terragrunt`, terraform-module; `tfr:///` uses the default registry); Terragrunt's `version` attribute is a constraint.
- `.terraform.lock.hcl` is kept in sync by the terraform post-processor (below).

**custom, annotation mode** (no `match`). A comment `deps: <datasource> <name> [versioning=<scheme>]` after `#`, `//`, `--`, `;` or `<!--` annotates the next line; its first version token (`v?N.N[.N…][-suffix]`, not glued to other word characters) is the version. Otherwise `no version on next line`. Helm charts are named `<repository>/<chart>` (`deps: helm https://charts.jetstack.io/cert-manager`) or given as two tokens, `deps: helm <repository> <chart>`; anything but an `https://`, `http://` or `oci://` repository with a chart is `invalid helm name "<name>"`.

**Renovate markers.** Annotation mode also accepts `renovate: datasource=<ds> depName=<name> [packageName=<p>] [versioning=<v>] [registryUrl=<url>]` (fields in any order; `packageName` wins over `depName`; `registryUrl` is the chart repository of `helm` deps and ignored otherwise, as are unknown keys); it annotates the next line like `deps:`. Skips: `renovate marker without datasource`, `renovate marker without depName`. For the docker datasource the next line's first tagged image reference is used instead: the tag after the last `:` is the version and an adjacent `@sha256:` digest is captured, so tag and digest are rewritten together. Renovate datasource names are aliased: `golang-version` → `go-toolchain`, `github-tags` → `git-tags` (`owner/repo` on github.com), `github-release-attachments` → `github-releases`, `node` → `node-version`. `custom.<name>` names a datasource of `customDatasources`; undefined ones, like other unknown names, are skipped as `unknown datasource "<ds>"`.

```dockerfile
# deps: github-releases golangci/golangci-lint
ARG GOLANGCI_VERSION=v2.5.0
# deps: go golang.org/x/tools versioning=go
RUN go install golang.org/x/tools/cmd/goimports@v0.38.0
```

**custom, regex mode.** `match` is an RE2 run over the whole file; every match is one dep. Named groups: `version` (required; alias `currentValue`), `name` (aliases `depName`, `packageName`; `packageName` wins over `depName`), `datasource`, `versioning`, `digest` (alias `currentDigest`); groups override or supply the entry's `name`/`datasource`. Empty groups skip the match (`empty version`, `empty name`, `empty datasource`). A `digest` group is replaced together with the version.

```yaml
custom:
  - files: ["**/*.mk"]
    match: '(?P<name>[a-z0-9-]+/[a-z0-9-]+)@(?P<version>v\d+\.\d+\.\d+)'
    datasource: github-releases
```

**Release checksums.** A regex entry can pin a checksum next to the version (`ARG GH_VERSION=…` / `ARG GH_SHA256=…`): a `(?P<checksum>…)` group in `match` plus a `checksum:` block, required together. Exactly one source:
- `asset` (github-releases only): the release asset's API `digest` from the already-fetched release list (no extra request);
- `url`: a checksum document downloaded once per URL (streamed, 1 MiB cap); with `file`, the entry is looked up in a sha256sum-style list (`<hex>  <name>` / `<hex> *<name>`), else the first hex token is taken.

The only template variable is `{{version}}` (the new version as written into the file), validated at load. sha256 (64 hex) and sha512 (128 hex) are accepted; the new checksum must have the current length and keeps its letter case. The checksum is fetched only after an update is chosen; a failure is that dependency's error (no update), so version and checksum always change together.

```yaml
custom:
  - files: [Dockerfile]
    match: 'ARG GH_VERSION=(?P<version>\S+)\nARG GH_SHA256=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: cli/cli
    checksum:
      url: 'https://github.com/cli/cli/releases/download/v{{version}}/gh_{{version}}_checksums.txt'
      file: 'gh_{{version}}_linux_amd64.tar.gz'
```

### Datasources

| Datasource | Name form | Source |
|---|---|---|
| `git-tags` | repository URL (`https://host/path` or `host/path`) | git smart-HTTP `ls-refs`, tags with peeled commits |
| `go` | module path | GOPROXY `@v/list`, or `go list -m -versions` for GONOPROXY/direct |
| `go-toolchain` | `go` / `toolchain` | `https://go.dev/dl/?mode=json&include=all`; release dates from Go's release history `https://raw.githubusercontent.com/golang/website/HEAD/internal/history/release.go` (`Date{y, m, d}, Version{1, N, P}` entries, as Renovate's golang-version), fetched once per run when a release time is first needed; a derived `X.Y` has the date of `X.Y.0` |
| `docker` | image reference | registry `tags/list` and manifest HEAD (oras-go) |
| `github-releases` | `owner/repo` (github.com) or `https://host/owner/repo` (GHES) | non-draft non-prerelease tag names, up to 5 pages of 100: GraphQL `releases` with a token for the host, else `GET /repos/{repo}/releases` (section 3) |

| `npm` | package name (`@scope/name` requested as `@scope%2fname`) | `GET <registry>/<name>` with `Accept: application/vnd.npm.install-v1+json` (abbreviated metadata), streamed: only the `versions` keys and each version's `deprecated`; deprecated versions are dropped |
| `pypi` | PEP 503-normalized name | PyPI JSON API `GET <base>/pypi/<name>/json` (`DEPS_PYPI_URL`, default `https://pypi.org`); custom indexes via the JSON Simple API (PEP 691/700). Yanked releases are dropped |
| `node-version` | `node` (any name) | `GET https://nodejs.org/dist/index.json` once per run (base URL overridable with `DEPS_NODE_DIST_URL` for mirrors), streamed element by element: `version` and `date` (release time) |
| `helm` | chart name; `Source` is the repository (`https://…` or `oci://host/path`) | classic: `GET <repo>/index.yaml` once per repository, stream-scanned (section 3); OCI: `tags/list` of `<host>/<path>/<chart>` through the docker datasource, tag `_` read as `+` |
| `crate` | crate name (case-insensitive); `Source` = registry name or index URL, "" = crates.io | sparse index `GET <index>/<prefix>/<name>` (crates.io: `https://index.crates.io`, `DEPS_CRATES_INDEX_URL` for mirrors), or a git index (shallow blob-less clone, `git cat-file`); newline-delimited JSON stream-decoded per line: `vers`, `yanked` (dropped), `pubtime` (release time) |
| `terraform-provider` | `namespace/type`; `Source` is the registry (`https://host`, default `https://registry.terraform.io`) | service discovery `/.well-known/terraform.json` once per registry, then `GET <providers.v1>/<ns>/<type>/versions` |
| `terraform-module` | `namespace/name/provider`; `Source` as above | service discovery, then `GET <modules.v1>/<ns>/<name>/<provider>/versions` |
| `custom.<name>` | free-form (all deps share the list) | `customDatasources.<name>`: one `GET <url>` per run (unauthenticated, at most 10 MiB), decoded as json, yaml or plain text, selected and optionally regex-filtered (below) |
| `maven` | `groupId:artifactId`, or a Gradle plugin id (its marker `<id>:<id>.gradle.plugin`); `Source` lists the repositories ("" = Central) | `GET <repo>/<group/path>/<artifact>/maven-metadata.xml` per repository (once per repository and artifact per run), versions merged in repository order; 404 contributes nothing, other failures only fail the lookup when no repository has the artifact; `DEPS_MAVEN_CENTRAL_URL` replaces Central; release time: `HEAD` of the version's POM, `Last-Modified` |
| `gradle-version` | `gradle` (any name) | `GET https://services.gradle.org/versions/all` once per run (`DEPS_GRADLE_VERSIONS_URL`), snapshots, nightlies and broken releases dropped; `buildTime` as release time |

**maven repositories and auth.** For maven and maven-wrapper deps the datasource reads the user settings (`$DEPS_MAVEN_SETTINGS`, else `~/.m2/settings.xml`) and the nearest in-repository `.mvn/settings.xml`, `.m2/settings.xml` or `settings.xml` (walking up from the dep's directory to the scanned root; in-repo elements win by id): `<mirrors>` with Maven's `mirrorOf` syntax rewrite repositories by id, `<servers>` give Basic auth or `httpHeaders` to the repository whose effective id matches (encrypted passwords are decrypted with `settings-security.xml`, plexus-cipher 2.0), and repositories of active profiles are appended before Central. `${env.X}` expands from the environment. Gradle deps get credentials for a repository named `x` from `ORG_GRADLE_PROJECT_xUsername`/`xPassword` or `~/.gradle/gradle.properties`. Requests run in their own pool (`httpx.Limits.Maven`, 2× `--concurrency`). `SourceRepo` reads the version's POM `<scm>` (following a plugin marker to its plugin and up to 3 parents), so release notes are fetched; `<artifactId>-<version>` tags count as versions.

Custom entries may use `git-tags`, `github-releases`, `go`, `go-toolchain`, `docker`, `npm`, `node-version`, `pypi`, `helm`, `crate`, `terraform-provider`, `terraform-module`, `maven` and `gradle-version`, the Renovate aliases `golang-version`, `github-tags`, `github-release-attachments` and `node`, and `custom.<name>` of a defined custom datasource.

**helm.** Classic index versions without `urls` are dropped (Helm ignores them); release times are the index's `created`. A classic request carries the forge token as Basic `x-access-token` only on the forge's own host (a Gitea/Forgejo package registry), else it is anonymous. OCI charts share the docker datasource's credentials, token cache and Docker Hub handling (`registry-1.docker.io`/`index.docker.io` are looked up as `docker.io`); their release time is the manifest annotation `org.opencontainers.image.created`, which `helm push` sets (the docker datasource reads manifest annotations instead of fetching a config blob for any non-image artifact). A chart missing from its index is `chart <name> not found in <repo>`. No digests.

**node-version.** Releases are returned as published (`v26.13.0`, prereleases included; the schemes drop them unless the current version is one), followed by a derived `X.Y` and `X` without `v` per stable release line, so partial values (`node-version: 26`, engines `^26`) have candidates of their precision under `tag` versioning. The derived entries come last so that semver, which reads `27` as 27.0.0, prefers the published `v27.0.0` on a tie. Semver keeps the file's `v` style (`NODE_VERSION = "26.5.0"` → `26.13.0`). A derived entry's release time is that of the line's first release. No digests. Renovate's `node` versioning (LTS-aware stability) is not reproduced; use `allow` to restrict majors.

**crate.** The index path follows Cargo's sparse protocol on the lowercase name: `1/<n>`, `2/<n>`, `3/<first char>/<n>`, else `<n[0:2]>/<n[2:4]>/<n>`; names outside `[a-z0-9_-]{1,64}` are `invalid crate name`. The crates.io index is used instead of the crates.io API because it is CDN-served and not rate-limited; one file per crate and registry per run, shared by every dep, under its own request pool (`httpx.Limits.Crates`, 2n). Yanked versions are dropped; a 404/410 is `crate <name> not found` (`… in registry <name>`), a crate with only yanked versions `crate <name> has no releases`. For a partial current version (`1`, `0.4`) the published versions are followed by a derived `X.Y` and `X` per stable release line (as for node-version), so the `cargo` scheme has candidates of the same precision; full versions get only the published list. Release times are the index `pubtime` (a derived line: its first release). The source repository for release notes is the `repository` of `GET <api>/api/v1/crates/<name>/<version>` (crates.io: `https://crates.io`, `DEPS_CRATES_API_URL`; other registries: the `api` of their `config.json`; a derived line asks for its newest version), fetched lazily, only for versions that end up in a PR. No digests.
- **Registry resolution** (per manifest directory, once): the `.cargo/config.toml` (or legacy `.cargo/config`) files from the manifest directory up to `--dir`, nearest first, then `$CARGO_HOME/config.toml` (`CARGO_HOME`, else `~/.cargo`), and `$CARGO_HOME/credentials.toml`, all parsed with tomlspan. A registry name's index is `CARGO_REGISTRIES_<NAME>_INDEX`, else `[registries.<name>] index`; an unknown name is a `*datasource.SkipError` (`unknown registry <name>`), which the engine reports as a skip (`Result.Skipped`, `Dep.Skip` in `Result.Deps` and the trace) instead of an error. Its token is `CARGO_REGISTRIES_<NAME>_TOKEN`, else `[registries.<name>] token` of the credentials, else of the config files, sent as `Authorization` (Cargo's format) only to requests on the index host: a `config.json` `api` on another host gets no credentials. Without a token, requests to the index host carry `httpx.Auth`'s static token for that host (Basic), and after a 401 the git credential helper's, once. crates.io follows `[source.crates-io] replace-with` (chains of up to 8) to a source with `registry = "…"`; `directory`/`local-registry` replacements vendor crates.io, which remains the lookup. `sparse+` indexes are HTTP; any other URL is a git index, cloned with `git clone --depth=1 --filter=blob:none --no-checkout` once per run (`GIT_TERMINAL_PROMPT=0`, git credential helpers) and read with `git cat-file blob HEAD:<path>`; the clones are removed when the command ends (`io.Closer`, called by the CLI session).

**terraform-provider, terraform-module** (`internal/datasource/tfregistry`, one `Registry` shared by both). Discovery results, version lists and version documents are cached per run; service URLs may be relative or absolute. Credentials are resolved once per host as the Terraform CLI does (Terraform `cliconfig` `ForHost` order): `TF_TOKEN_<host>` (dots `_`, hyphens `__` or kept, punycode hosts verbatim), then `credentials "<host>" { token }` of the CLI config (`TF_CLI_CONFIG_FILE`/`TERRAFORM_CONFIG`, else `~/.terraformrc` plus the `*.tfrc`/`*.tfrc.json` files of `~/.terraform.d`, e.g. `credentials.tfrc.json`; `%APPDATA%` on Windows; parsed by `terraform.ParseCLIConfig`), then the `credentials_helper` (`terraform-credentials-<name> <args…> get <host>`, found in `~/.terraform.d/plugins[/<os>_<arch>]` or PATH; missing → `terraform-credentials-<name> not found (needed for <host> credentials)`), sent as `Authorization: Bearer`. Prereleases are returned; the schemes drop them. Release times and the source repository (release notes) come from the version document `<service><name>/<version>` (`published_at`, `source`; registry.terraform.io serves it, a 404 is unknown); a partial version (`5.31`) uses its line's first release. A missing name is `provider <name> not found in <registry>` / `module …`. No digests.

**Custom datasources.** The document is fetched once per run and shared by every dep of that datasource. json is decoded with numbers kept as written; yaml is decoded to nodes so scalars keep their text (`1.20` is not the number 1.2). The `versions` selector (a jq subset, below) yields the values; nulls are dropped, non-scalars are errors. With `versionRegex` a value must match and its `version` group (else the whole match) is the version. Duplicates are removed, document order is kept; an empty result is `no versions`. No release times, no digests. Errors read `custom.<name>: GET <url>: HTTP 404`, `custom.<name>: <url>: selector ".a[]": cannot iterate over null`.

Selector grammar: stages joined by `|`; a stage is `keys` (sorted keys of an object) or a path of steps `.` (identity), `.field` (identifier `[A-Za-z_][A-Za-z0-9_-]*`) and `[]` / `.[]` (array elements or object values), which may start with `[]`. Like jq, a missing field or a field of null is null, and iterating null or indexing/iterating the wrong type is an error. Examples: `.releases[].version` (the default, Renovate's custom datasource shape), `.releases | keys` (controller-tools' envtest-releases.yaml), `[].tag_name` (a GitHub releases list). JSONata `transformTemplates` are not supported; `versionRegex` covers the common `$replace(…, /^v/, "")` case.

**npm registry and auth.** Per package.json directory, the nearest `.npmrc` walking up to the scanned root is read (once per directory per run): `@scope:registry=` for scoped names, then `registry=`. Without one, `NPM_CONFIG_REGISTRY` (or `npm_config_registry`), else `https://registry.npmjs.org/`. A `//host/path/:_authToken=` entry whose key is the longest prefix of the registry URL (without scheme) sends `Authorization: Bearer <token>`. `${VAR}` and `${VAR?}` in keys and values expand from the environment (empty when unset); values may be quoted. Lookups are deduped per (registry, name). A 404 is `package <name> not found`. npm has no digests.

**PyPI index and auth.** Without an index from the manifest (requirements `--index-url`, `[[tool.uv.index]]`/`[tool.uv.sources]`, `[[tool.poetry.source]]`, Pipfile `[[source]]`, `[[tool.pdm.source]]`), `PIP_INDEX_URL` names one; otherwise the PyPI JSON API is used. Extra indexes (`Dep.ExtraSources`: requirements `--extra-index-url`, non-`explicit` uv indexes, Poetry supplemental sources, the other Pipfile sources with `install_search_all_sources`, other pdm sources; then `PIP_EXTRA_INDEX_URL`) are fetched too and their releases merged, pip-style: each version once, the earliest upload time, a 404 on some index tolerated. Each (index, name) is fetched once per run; merged and filtered views are cached per (indexes, name, Python constraint). Custom indexes are asked for the JSON Simple API (PEP 691/700) and may answer HTML (PEP 503). Credentials come from the index URL's userinfo (`https://user:token@host/simple`).

**requires-python.** Managers set `Dep.PythonConstraint` from `requires-python`, Poetry's `python` or Pipfile `[requires]`. A release is dropped when the `requires-python` shared by its non-yanked files does not admit every final Python release the constraint admits (`versioning.PEP440Subset`: both specifier sets become unions of version intervals, and the project's set minus the release's must be empty), like Renovate, Poetry and pdm resolve for the whole supported range. Releases without, with differing or with invalid `requires-python` are kept.

### Post-processors

`engine.Build` patches every file, then asks the post-processor registered for the file's manager for the file's **scope**: the directory owning its derived files (`plan` passes only the in-process helm post-processor). Manifests of one manager sharing a scope are post-processed together, once per scope, under a global bound of 4 concurrent runs. The engine reads the scope dir's existing siblings; a result under a manifest's path (relative to the scope) replaces that manifest; every sibling whose content differs becomes a change (mode kept, `0644` for new files). A manifest without a scope is only patched. A failing scope drops all its manifests and siblings (`<file>[, <file>…]: <err>`).

- **gomod** (`gotool.Tidier`, scope = module dir (for a `.go` file: the nearest go.mod at or above it), siblings `go.sum`): `go mod tidy -modfile=<tmp>/go.mod` on copies; `.go` files rewritten by a major module path update are passed with `-overlay`, so tidy sees the new imports without touching the working tree.
- **npm** (`nodetool.Locker`, siblings `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock` + read-only `.npmrc`, `pnpm-workspace.yaml`, `.yarnrc.yml`).
  - Scope (lock root): nearest dir at/above the package.json (within `--dir`) holding one of the lockfiles, accepted if it is the manifest's own dir or its package.json `workspaces` (array or `{packages}`) / `pnpm-workspace.yaml` `packages` doublestar globs match the manifest dir (`!glob` excludes); otherwise no lockfile (none is created).
  - Temp copy: root + all member package.json (patched where changed) at relative paths, the lockfile, config files, and local files referenced by `.yarnrc.yml` `yarnPath`/`plugins` (member discovery walks each glob's literal base, skipping node_modules/.git).
  - Commands: npm `install --package-lock-only --ignore-scripts --no-audit --no-fund`; pnpm `install --lockfile-only --ignore-scripts --no-frozen-lockfile`; yarn berry (`.yarnrc.yml` or `__metadata` in the lock) `install --mode=update-lockfile` with `YARN_ENABLE_SCRIPTS=0 YARN_ENABLE_IMMUTABLE_INSTALLS=0`; yarn classic `install --ignore-scripts --ignore-engines --non-interactive --no-progress`. Env `npm_config_update_notifier=false`, `COREPACK_ENABLE_DOWNLOAD_PROMPT=0`.
  - Tool resolution: binary on PATH; else `npx --yes <tool>@<version>` when root `packageManager` names it (berry: `npx --yes --package=@yarnpkg/cli-dist@<version> -- yarn`); else `npx --yes <tool>`; else `<tool> not found (needed to update <lock>)`.
  - Several lockfiles in one root: the `packageManager` tool's lockfile wins, else package-lock, pnpm, yarn. Only the lockfile is read back and returned when changed; failure `<tool> install in <dir>: <last 20 output lines>`. npm-shrinkwrap.json is not handled.
  - Lockfile-only updates (`engine.LockfileUpdater.ProcessLocked`, which the engine calls instead of `Process` when a scope has any; package-lock.json only): the plain install runs first only if a manifest of the root changed; then `npm install … <name>@<version>…` for the root manifest and `npm install … --workspace=<dir> <name>@<version>…` per member (Renovate's commands). npm saves `^<version>` into `packages[""]`/`packages["<dir>"]`; deps restores the unchanged range there by byte span, so `npm ci` accepts the lock. A manifest whose only updates are lockfile-only yields no manifest change, and `plan` (no post-processor) shows no diff for it.
- **cargo** (`cargo.Locker`, sibling `Cargo.lock`; an `engine.UpdatePinner`, also an `engine.LockfileUpdater`).
  - Scope (workspace root): nearest dir at/above the Cargo.toml (within `--dir`) holding `Cargo.lock`, accepted if it is the manifest's own dir or its Cargo.toml `[workspace]` `members` doublestar globs include the manifest dir and `exclude` does not; otherwise no lockfile (none is created). `ReadLocks` uses the same scope.
  - Temp mirror of the root: every `Cargo.toml` copied (patched where changed) plus `Cargo.lock`, every other file symlinked (cargo needs the target files to exist, never their content), `.git`/`target`/`node_modules` left out. A root below the repository top (nearest ancestor with `.git`) is mirrored at the same relative path, with the other entries of every directory above it symlinked, so path dependencies outside the root resolve (ripgrep's `fuzz/` workspace uses `../crates/globset`).
  - Commands: `cargo update --workspace` (re-resolves only requirements the lock no longer satisfies), then for each crate whose rewritten requirement has a full version and whose lock entry in that compatibility range is newer and was not locked before: `cargo update --package <name>@<locked> --precise <version>`, so the lock holds the PR's version even when a policy (release age) chose an older one. Already locked versions are never downgraded. A manifest update whose crate cargo left alone (its entries unchanged, `Dep.Locked` still there and older than `NewVersion`, `NewVersion` not locked: the rewritten requirement still admits the locked version, `<0.10` → `<2.0`) is resolved with its requirement written as `=<NewVersion>` in the mirror for one more `cargo update --workspace`, then the patched manifests are restored and resolved again; unlike `--precise` on the shared entry, this keeps the old version locked for other dependents that still need it (rustc_version 0.2 requires semver ^0.9). Each lockfile-only update runs `cargo update --package <name>@<Locked> --precise <NewVersion>` (deduped). Env `CARGO_TERM_COLOR=never`, `CARGO_TERM_PROGRESS_WHEN=never`.
  - Tool: `cargo` on PATH, else `cargo not found (needed to update Cargo.lock)`; failure `cargo update in <dir>: <last 20 output lines>` (temp paths shown as the root's).
- **python** (`pytool.Locker`, shared by pep621, poetry and pipenv; managers sharing one file, e.g. pep621 and poetry in one pyproject.toml, share its post-processor). Scope: the manifest's dir when it holds `uv.lock`, `poetry.lock`, `pdm.lock` or `Pipfile.lock`; for uv workspaces the nearest `uv.lock` above whose `[tool.uv.workspace]` members match. Temp copy: the scope dir's top-level files (≤4 MiB each). Commands: `uv lock --no-progress`; `poetry lock` (Poetry 2) / `poetry lock --no-update` (Poetry 1), fallback `uvx --from poetry poetry lock`; `pdm lock --update-reuse` (keeps the pins the manifest still admits; plain `pdm lock` would re-resolve everything to the newest), fallback `uvx --from pdm pdm lock --update-reuse`; `pipenv lock`, fallback `uvx --from pipenv pipenv lock`. Errors `<tool> not found (needed to update <lock>)`, `<tool> <args> in <dir>: <last 20 lines>`.
  - Lockfile-only updates (`ProcessLocked`): the plain lock runs first only if a manifest of the scope changed; then `uv lock --no-progress --upgrade-package <name>==<version>…`, `poetry update --lock <name>…`, `pdm update --update-reuse --no-sync -G <group> <name>…` per pdm.lock group (pdm accepts one group with names), `pipenv upgrade --lock-only --categories <category> <name>==<version>…` per Pipfile table. Poetry and pdm take the newest version the range admits (they cannot pin one); uv and pipenv pin the proposed one. Verified with the real tools (`TestProcessLockedReal`).
- **pip_requirements** (`pytool.Compiler`; scope = the dir of a requirements file containing `--hash`). Only files in which a hashed requirement changed version (matched by position and name against the file on disk) are touched. A pip-compile (`# This file is autogenerated by pip-compile` … `#    pip-compile <args>`) or uv (`# This file was autogenerated by uv via the following command:` / `#    uv pip compile <args>`) header is parsed shell-style; the command's cwd is the scope dir minus the `--output-file` path's directory. Temp copy, relative to that cwd: the patched file, the header's inputs with the `-r`/`-c` files and local projects (`-e ./dir`, top-level files) they name, `-c`/`--override`/`--build-constraint` files, and its `uv.toml`/`pyproject.toml` (uv configuration). The command is re-run without `--upgrade`/`--upgrade-package` plus `--upgrade-package <name>==<version>` per changed requirement (`uv pip compile … --no-progress`; `pip-compile`, fallback `uvx --from pip-tools pip-compile`), and the file's header up to the command line is kept byte for byte (the tools write the temp paths and their own version into it). Without such a header, or when the command cannot be re-run (`--directory`/`--project`, an absolute or different `--output-file`, a missing input or one outside the cwd), the hashes of each changed requirement are replaced by `pypi.Source.Hashes` of its new version (the sha256 of every file of the release: JSON API `urls[].digests.sha256`, simple API `hashes.sha256` / `#sha256=`), sorted, one `--hash=sha256:` option each with the separator between the first two options (`" \\\n    "`). `plan` runs neither and prints a note.
- **helm** (`helm.Locker`, scope = the dir of a `Chart.yaml`, sibling `Chart.lock`; helmfiles and resources have no scope). In process, no network, so `plan` shows the lock diff too. Without a `Chart.lock` nothing happens.
  - Every dependency whose patched Chart.yaml version is exact and differs from the locked one (matched by position and name) gets the new version in the lock, in the lock's `v` style.
  - `digest` is Helm's `HashReq`: `sha256:` + hex sha256 of `json.Marshal([2][]*chart.Dependency{chartYamlDeps, lockDeps})`, with `chart.Dependency` mirrored field for field. `@name`/`alias:name` repositories are replaced by the URL locked at the same position, as `helm dependency build` resolves them before hashing. Verified against Helm-written locks and accepted by `helm dependency build`.
  - The old digest is recomputed from the unpatched Chart.yaml first; a mismatch fails the chart (`Chart.lock is out of sync with Chart.yaml …; run helm dependency update`) rather than writing a lock Helm would reject.
  - Only the version and digest bytes change; `generated` is kept, so the same update always yields the same commit.
- **terraform** (`terraform.Locker`, scope = the dir of a configuration file with a sibling `.terraform.lock.hcl`; also a `LockfileUpdater`). Only for `apply`/`run`.
  - The original and patched files are extracted; providers whose version constraint changed, and providers with lockfile-only updates, that the lock lists (matched by `namespace/type`) are refreshed, otherwise nothing runs.
  - Their `provider` blocks are cut from the lock, then `terraform init -backend=false -input=false -no-color` (`tofu` for `*.tofu` files or `registry.opentofu.org` entries) runs in a temporary mirror of the directory with `TF_IN_AUTOMATION=1 TF_INPUT=0 CHECKPOINT_DISABLE=1` and `TF_DATA_DIR` inside the mirror: init locks the newest version matching the constraint and records `zh:` hashes for all platforms and `h1:` for the runner's; other entries keep their version.
  - Lockfile-only updates: an override file `deps_lock_override.tf` in the mirror constrains each provider (by its local name and source from `required_providers`) to `= <version>`; afterwards the entry's `constraints` are reset to the old lock's (the configuration did not change) and a different locked version fails the update.
  - Mirror: the directory's files are copied (manifests and lock replaced), its subdirectories symlinked (`.terraform` left out), and for the deepest `../` reached by local module sources (followed through local modules) each ancestor's other entries are symlinked, so relative paths resolve as in the working tree.
  - Without the CLI: `terraform not found (needed to update .terraform.lock.hcl)` and the directory's changes are dropped. A failed init reports `terraform init in <dir>: <last 20 output lines>`.
- **gradle** (`jvmtool.GradleLocker`, scope = the build root: nearest dir with `settings.gradle(.kts)`; only when the build has a `gradle.lockfile`/`gradle/dependency-locks/` or `gradle/verification-metadata.xml`; siblings listed per scope via `engine.ScopeSiblingLister`, so subproject lockfiles are read back). Works on a temp copy of the build (without `.git`, `.gradle`, `build/`) with the patched manifests overlaid, running `./gradlew` (else `gradle`) `--console=plain --no-daemon -q :<project>:dependencies …` per lockfile project with `--write-locks` (manifest changes) or `--update-locks g:a,…` (lockfile-only updates, `engine.LockfileUpdater`), plus `--write-verification-metadata <kinds found in the file> --dependency-verification lenient`. Needs `java` on PATH or `JAVA_HOME`, else `java not found (needed to update <lockfile>)`; failure `gradle in <dir>: <last 20 output lines>`.
- **gradle-wrapper** (`jvmtool.GradleWrapper`, scope = the project of `gradle/wrapper/gradle-wrapper.properties`): `./gradlew wrapper --gradle-version <v> --distribution-type bin|all [--gradle-distribution-sha256-sum <sum>] [--gradle-distribution-url <mirror>]` on a temp copy; returns `gradle-wrapper.jar`, `gradlew`, `gradlew.bat` (the byte-edited properties are kept unless Gradle changes a value). Without Java: `java not found (needed to regenerate the Gradle wrapper)`.
- **maven-wrapper** (`jvmtool.MavenWrapper`, scope = the project of `.mvn/wrapper/maven-wrapper.properties`): `./mvnw` (else `mvn`) `-B -q org.apache.maven.plugins:maven-wrapper-plugin:<wrapperVersion>:wrapper -Dmaven=<v> -Dtype=<type>` on a temp copy; returns `mvnw`, `mvnw.cmd`, `.mvn/wrapper/maven-wrapper.jar`, `MavenWrapperDownloader.java`. Without Java: `java not found (needed to regenerate the Maven wrapper)`.

## 3. Performance and memory design

The following is implemented:

- **Discovery.** A single `filepath.WalkDir`. It prunes `.git`, `node_modules`, `vendor`, `testdata` and directories matching `ignorePaths` via `fs.SkipDir`. When `ignorePaths` is absent the default is `**/{node_modules,bower_components,vendor,examples,__tests__,test,tests,__fixtures__}/**` (Renovate's `:ignoreModulesAndTests`). Paths are matched before any file is opened, and only matched files are read.
- **Extraction.** Runs on `runtime.GOMAXPROCS(0)` workers. Each worker keeps only `[]Dep` (spans and strings); parse trees and file bytes are dropped immediately. Files are re-read when changes are built, so memory is O(deps), not O(repo). References across files (a k0s chart's repository alias, a Flux HelmRelease's HelmRepository) are resolved in one sequential `Link` pass per manager after extraction; until then the manager keeps only the repository names and URLs it saw.
- **HTTP.** One shared `http.Client` (HTTP/2, keep-alive) handles every network call.
- **Retries and rate limits.** The shared transport retries idempotent requests: GET/HEAD/OPTIONS, and read-only POSTs marked with `httpx.Idempotent` (git ls-refs, OSV querybatch, GitHub GraphQL queries). It retries connection errors and 429/500/502/503/504, at most 3 times, with full-jitter exponential backoff (500ms base, 10s cap), honouring `Retry-After`. Platform writes are never retried. A 429, or a 403 with `X-RateLimit-Remaining: 0` (or `RateLimit-Remaining: 0;w=<s>`) or `Retry-After`, is a rate limit: if it resets within 60s deps waits (info log); otherwise the request fails fast with `rate limited by <host> until <time>` (`httpx.ErrRateLimited`) instead of stalling the run. The reset is `Retry-After`, else `X-RateLimit-Reset`, else the end of the quota window of `RateLimit-Remaining: 0;w=<seconds>`, which is all Docker Hub sends with an exhausted pull quota (`0;w=3600`), so such a 429 fails at the first attempt. The registry client gets the plain transport, not oras-go's retrying client, so attempts are not multiplied. Each attempt is logged at debug, with `X-RateLimit-Remaining` when present.
- **Lookup dedupe.** Lookups are deduped within a run per key (one `sync.Once` per cache entry).
- **Request pools.** Concurrent requests are bounded per upstream by semaphores sized from `--concurrency` n (default 32; `httpx.Limits`): the npm registry gets 2n slots, OSV 2n, container registries (the docker datasource, also serving Helm OCI charts) n/2, and everything else (Go proxy and toolchains, GitHub, git tags, Helm repositories, Node.js releases, custom datasources, checksums, release notes) shares n. Every host deps talks to speaks HTTP/2, so requests multiplex over a few connections. With one global pool, a busy upstream starved the rest: on backstage (754 npm packuments, 168 advisories) npm held most of the 32 slots until 3.6–4.8 s, and the advisory fetches queued behind it until 5.7–10.2 s. With the pools npm finishes at 1.4 s and the advisories at 1.4–1.6 s, and wall time dropped from 8.9 s to 6.3 s (median of 3, interleaved; the rest is Docker Hub rate limiting). Container registries get half, not more: Docker Hub answers bursts with 429s, and with n/2 it sees no more concurrent requests than under the shared pool (backstage 5–7, istio 13–15 before and 9–10 after). On open-component-model, a shared pool of 32 instead of 16 cut wall time by about a third; 64 gained nothing further.
- **Candidate selection.** Each scheme parses a dependency's candidates once (`Scheme.Rank`); Actions/tag versions use a hand-written scanner instead of a regexp, and Masterminds conversion happens only when an `allow` constraint needs it. Go module versions are parsed once into a 32-byte struct (end offsets of major/minor/patch/prerelease into the original string) by a parser that mirrors `golang.org/x/mod/semver`; compatibility, ordering and kind compare substrings without re-parsing, and a test checks validity, `Compare`, kind and compatibility against `x/mod` over a generated corpus (shorthand, prereleases, `+incompatible`, pseudo-versions, leading zeros). The Masterminds conversion of a full `vX.Y.Z` uses the regexp-free `StrictNewVersion`. On `BenchmarkResolve` this cut 32.6 → 23.9 ms/op, 34.4 → 29.6 MiB/op and 199.8k → 176.9k allocs/op. Release-time guards (minimum release age, majors older than current) walk the ranking instead of re-selecting. After the first too-new rejection the next 8 candidates' release times are fetched concurrently.
- **GitHub releases.** One release list per repository and run (`internal/githubapi`) serves the `github-releases` datasource (versions, release times, checksum assets) and the release times of `git-tags` on github.com. With a token for the host it is listed over GraphQL (`POST https://api.github.com/graphql`, GHES `<host>/api/graphql`; `releases(first:100, orderBy: CREATED_AT DESC)` with only `tagName isDraft isPrerelease publishedAt`, cursor-paged); without one over REST (GraphQL requires auth), whose pages always carry every body and asset. Pages are fetched only until the dependency's current release is listed, since everything newer precedes it (at most 5 pages); `git-tags` release times use the first page and look up tags missing from an incomplete list individually (`release(tagName:)`, REST `/releases/tags/<tag>`). A git tag without a release of its own (a floating `v25` next to `v25.0.0`, or a repository that only tags) takes the earliest publication time of an already listed release whose tag peels to the same commit (no further pages), else the tag date: over GraphQL `ref(qualifiedName: "refs/tags/<tag>")`, the tagger date of an annotated tag, else the committer date of its commit (Renovate's github-tags uses the same two dates); over REST `/git/commits/<sha>` of the peeled commit, its committer date (one request instead of resolving the tag object). Each tag costs at most these two extra requests, once per run. Over GraphQL, checksum asset digests are fetched on demand (`release(tagName:){releaseAssets(first:1,name:)}`), once per (repository, tag, asset). GraphQL queries are marked idempotent so they are retried like GETs; `NOT_FOUND` errors behave like a REST 404 and `RATE_LIMITED` like an exhausted rate limit. On open-component-model (`docs/examples-ocm.deps.yaml`, identical plan output) the 17 GitHub API requests went from 31.2 MB to 0.13 MB of response bodies, their summed time to first byte from 16.4 s to 8.1 s and the slowest release page from 2.8 s to 0.95 s; max RSS dropped from about 62 MB to 52 MB.
- **Profiling.** Hidden flags `--cpuprofile` and `--memprofile` write pprof profiles; `BenchmarkResolve` (`internal/engine`) resolves a synthetic OCM-sized repository against in-memory datasources (900 deps, 600/80 versions, 28-day age) to track engine CPU and allocations.
- **Actions tags.** Fetched with a native git smart-HTTP protocol v2 `ls-refs` call using `ref-prefix refs/tags/` and `peel`. No `git` process is spawned per action, only tag refs travel over the wire, and the pkt-line response is parsed as a stream.
- **Go versions.** Fetched from the GOPROXY `@v/list` endpoint over HTTP. `go list` is used only for GOPRIVATE/direct modules. Release times (`@v/<v>.info`, immutable) are read from the local module download cache (`$(go env GOMODCACHE)/cache/download/<module>/@v/<v>.info`) when the go command already fetched that version, also for private modules; only misses go to the proxy. On open-component-model with a warm cache this serves 84 of 258 lookups. Version lists are never taken from the cache, since it only knows locally fetched versions. A pseudo-version dep without a newer release costs one `@latest` per module (its time is kept as that version's release time); major module path probes cost one `@v/list` per successor path (usually a 404 for `/v<N+1>`), deduped per path and only made when major updates are allowed for the dep. Go toolchain releases come from one `go.dev/dl/?mode=json&include=all` fetch per run.
- **Registries.** Docker tags come from `tags/list` (Docker Hub with page size 10000, other registries 1000, since ghcr.io defaults to 100 and AWS ECR refuses more than 1000; pages up to 64 MiB, since gcr.io returns every tag in one ~10 MB page), digests from a manifest HEAD. As Renovate's `dockerMaxPages`, a listing stops after 20 pages (warning `image tag listing truncated`) and resolves with the tags seen, so a per-commit CI image cannot dominate a run; ghcr.io, quay.io, cgr.dev and the Red Hat registries, which list oldest first or publish a tag per build so that a truncated listing would miss the newest tags, get 1000 pages. Docker Hub's own tag API (`ordering=last_updated`, which Renovate uses so that its cap keeps the newest tags) is not used: it pages at most 100 tags, so library/node (9270 tags) would take 93 requests instead of one. deps pages and stream-decodes `tags/list` itself rather than through oras-go, because gcr.io and Artifact Registry (also behind registry.k8s.io) add a `manifest` map of digest → tags, `timeCreatedMs` and `timeUploadedMs`: from it every tag's digest and release time come with the listing, with no HEAD, manifest or config request (the kubernetes repository with `minimumReleaseAge` and `pin: digest`: 52 → 10 gcr.io requests). The release time is `timeCreatedMs`, the image config's `created` that other registries' times come from too, else `timeUploadedMs`: creation is 0 for indexes and 1970 builds and negative for Go's zero time, and the upload, being later, never makes a release pass `minimumReleaseAge` early. The source label still needs the config, fetched only when asked for. Credentials come from the Docker config and its credential helpers. One oras-go auth client with a shared token cache per run, so each token exchange happens once per registry and scope. Registries on localhost, loopback and private addresses are reached over plain HTTP.
- **Docker Hub pulls.** Docker Hub limits manifest GETs, by tag and by digest, per IP address for anonymous clients (`ratelimit-limit: 100;w=3600`); HEADs are not counted. Digests come from HEADs. Release times need the manifest (for an index also its first platform manifest) and config; on Docker Hub the tag is first resolved with the HEAD that `Digest` shares, and the manifests are fetched by digest once per digest. Tags of one image (`postgres:18`, `postgres:18.6`) then cost one pull, and a pinned tag whose release time is also asked costs no second request for its digest. On backstage `plan` (shallow clone, 2026-11-04, measured through a proxy serving Hub manifests from mirror.gcr.io), manifest GETs to `registry-1.docker.io` went from 42 (21 by tag, 21 by digest; 4 HEADs) to 38 (all by digest; 21 HEADs), with an identical plan. With an exhausted quota (429, `ratelimit-remaining: 0;w=3600`, no `Retry-After`) the run sent 6 manifest requests and no retries instead of 36 requests and 27 retries; the errors from Docker Hub point to `docker login`.
- **npm metadata.** Abbreviated documents are stream-decoded; only one version object is buffered at a time.
- **Chart indexes.** A classic repository's `index.yaml` (27.5 MB for charts.bitnami.com) is fetched once per run and scanned line by line from the response stream: chart keys sit at one indentation under `entries:`, so the lines of every chart nobody asked for are skipped without parsing or buffering, and only a wanted chart's block is decoded (into version, appVersion, created, digest, urls). The wanted set comes from `datasource.Planner`: before the first lookup the engine hands each planning datasource all of its deps, so one scan serves every chart of a repository; a chart asked for later costs one more scan. A JSON index is decoded whole. Measured: plan of 5 charts over the bitnami, prometheus-community and grafana indexes (38 MB) peaks at 62 MB RSS (an empty run: 12 MB); decoding only the bitnami index whole with yaml.v3 takes 292 MB.
- **PyPI metadata.** One JSON document per package and index, deduped per run.
- **Security.** OSV is queried in batches of up to 1000 packages; each advisory document is fetched once.
- **Child processes.** Spawned only where unavoidable: post-processors (`go mod tidy`, npm/pnpm/yarn lockfile installs, `cargo update`, uv/poetry/pipenv locks; at most 4 concurrently in total) and `git` for commit and push.
- **Commits.** Built with git plumbing on a temporary index. `run` does no checkout, creates no worktree, and never mutates the working tree. With `--signing-key`, `commit-tree -S` signs them: an OpenSSH key is written once to a `0600` temporary file (`-c gpg.format=ssh -c user.signingkey=<file>`), an armored OpenPGP key is imported once into a temporary `GNUPGHOME` and selected by fingerprint. Both are validated up front (a passphrase-protected key fails before any lookup) and removed when `run` returns.
- **Platform commits.** With `platformCommit` on GitHub (`auto`: an App installation token and no signing key), `run` creates commits through the API instead (`internal/platform.GitHubCommits`, behind the same `bot.Git` interface): one `POST /git/blobs` per changed file (base64, so binary content is exact), `POST /git/trees` on the parent's tree (`sha: null` deletes, mode `100755` for executables), `POST /git/commits` with the parent, then GraphQL `updateRefs` with `beforeOid` = the tip listed at the start of the run (all zeros for a new branch) and `force`. The compare and the update are one atomic step on GitHub, so the semantics equal `git push --force-with-lease`, and a failed step leaves the branch untouched: the objects created before are unreferenced and need no cleanup. GitHub rejects a stale `beforeOid` with a generic error; deps then reads the ref to report `stale info`. Branch tips are read with `GET /commits/refs/heads/<branch>`: a tip whose author GitHub links to the token's account reports the account's noreply email (`<id>+<login>@users.noreply.<host>`, the bot author), so the ownership check needs no guess of which email GitHub wrote. The configured `gitAuthor` is ignored for these commits and added to the ignored authors, so branches deps pushed with git before stay its own. Listing and deleting branches stay with git. Renovate takes a different route: it pushes the commit to `refs/renovate/branches/<branch>` so the blobs reach GitHub by git, recreates tree and commit by REST, and moves the branch with a forced `PATCH /git/refs`, without a compare.
- **No persistent cache.** Runs are infrequent (scheduled), so in-run dedupe already removes the repeated lookups.

## 4. Core types

```go
// internal/dep
type UpdateType string // "major" | "minor" | "patch" | "digest" | "pin"
const (Major UpdateType = "major"; Minor = "minor"; Patch = "patch"; Digest = "digest"; Pin = "pin")

type Span struct{ Start, End int } // byte offsets, End exclusive

type Dep struct {
    Manager     string // "github-actions" | "gomod" | "docker" | "npm" | "pip_requirements" | "pep621" | "poetry" | "pipenv" | "helm" | "cargo" | "terraform" | "terraform-version" | "tflint-plugin" | "terragrunt" | "maven" | "gradle" | "gradle-wrapper" | "maven-wrapper" | "custom"
    Datasource  string // "git-tags" | "go" | "go-toolchain" | "docker" | "github-releases" | "npm" | "node-version" | "pypi" | "helm" | "crate" | "terraform-provider" | "terraform-module" | "maven" | "gradle-version" | "custom.<name>"
    Versioning  string // "tag" | "go" | "docker" | "semver" | "pep440" | "loose" | "cargo" | "hashicorp" | "maven" | "regex:<RE2>"; a rule's versioning replaces the manager's
    DepType     string // gomod: direct|indirect|go|toolchain; actions: action|setup; docker: dockerfile|compose|workflow|action|kubernetes|helm; npm: dependencies|devDependencies|optionalDependencies|peerDependencies|packageManager|engines|volta; pip_requirements: requirements; pep621: project.dependencies|project.optional-dependencies|dependency-groups|tool.uv.dev-dependencies|tool.pdm.dev-dependencies; poetry: dependencies|dev-dependencies|group; pipenv: packages|dev-packages|category; helm: chart|helmfile|k0s|flux|argocd; cargo: dependencies|dev-dependencies|build-dependencies|workspace.dependencies|patch|replace; terraform: required_provider|provider|module|required_version|helm_release|tfe_workspace|<resource type>; terraform-version: terraform-version|opentofu-version; tflint-plugin: plugin; terragrunt: github|gitTags|terragrunt; maven: compile|provided|runtime|test|system|import|parent|build; gradle: dependencies|plugin; gradle-wrapper: wrapper; maven-wrapper: maven|wrapper; custom: annotation|regex
    File        string // slash path relative to --dir
    Line        int    // 1-based
    Name        string // actions: owner/repo[/sub]; gomod: module path; helm: chart
    Source      string // actions: https://<host>/<owner>/<repo>; gomod: ""; helm: repository URL (https:// or oci://)
    Version     string // current version (ref tag, comment version, or module version)
    Digest      string // actions: full SHA when pinned, else ""
    RefSpan     Span   // actions: ref after '@'; gomod: version token
    NameSpan    Span   // gomod: module path token of a require
    VersionSpan Span   // actions: version token in trailing comment (pinned only)
    DigestSpan  Span   // docker/custom: digest token stored apart from RefSpan
    InsertAt    int    // manager-defined insert offset (actions: after the uses value incl. closing quote)
    Skip        string // non-empty = not updatable, with the reason

    Checksum       string          // custom: current sha256/sha512 hex pinned next to the version
    ChecksumSpan   Span            // custom: checksum token
    ChecksumSource *ChecksumSource // custom: where the checksum of a new version comes from; nil = none

    Range      string // npm dependency sections, cargo and Python specifiers: the range as written (^1.2.3, ">=1.2, <2", >=1.2,<2); rangeStrategy applies when set
    Constraint string // cargo: Range as a Masterminds constraint when it differs ("1.2" is "^1.2"); "" = Range
    Locked     string // npm/cargo/Python: version the lock root's lockfile (package-lock.json, Cargo.lock, uv.lock, poetry.lock, pdm.lock, Pipfile.lock) resolves the dep to; "" = none
    LockUnread bool   // npm: the lock root's lockfile is not read (pnpm-lock.yaml, yarn.lock, package-lock.json v1); cargo: Cargo.lock is broken
}

// Asset and URL/File are templates over {{version}}; exactly one of Asset and URL is set.
type ChecksumSource struct {
    Asset string // github-releases: release asset whose API digest is used
    URL   string // checksums document to download
    File  string // entry name in a sha256sum-style URL document; "" = first hex token
}

type Advisory struct{ ID, Summary, URL string }

type Update struct {
    Dep         Dep
    Type        UpdateType
    NewVersion  string
    NewDigest   string        // "" unless the result is digest-pinned; gomod pseudo-versions: the new commit
    NewName     string        // gomod major module path update: the new module path; "" = unchanged
    Group       string        // policy group, "" = own PR
    Schedule    *schedule.Set // workflow crons that put it in schedule; nil = every run
    Advisories  []Advisory    // non-empty = security update
    NewChecksum string        // "" unless Dep.ChecksumSource is set and the version changes
    CommitType  string        // policy commit type (fix, chore, ...)
    CommitScope string        // policy commit scope; "" = none
    Bucket      string        // PR bucket among the dep's updates: "" (only or non-major), "major", "v<N>", "minor", "patch"
    Automerge   bool          // policy automerge of the update's type
    AutomergeStrategy string  // policy automergeStrategy: auto, merge, squash, rebase
    AutomergeType string      // policy automergeType: pr, branch
    Lockfile    bool          // lockfile-only update: the manifest keeps its range, the lockfile moves from Dep.Locked
}
```

```go
// internal/manager
type Env struct {
    DefaultHost string                // actions default host
    GitLabURL   string                // GitLab instance base URL; "" means https://gitlab.com
    Vendored    func(dir string) bool // slash dir relative to root; engine supplies a stat check when nil
}
type Manager interface {
    Name() string
    Match(path string) bool
    Extract(file string, src []byte, env Env) ([]dep.Dep, error)
    Edits(u dep.Update) ([]patch.Edit, error) // byte edits for one update; the engine merges them per file
}
// Linker is optional: managers whose deps refer to definitions in other files (helm) record
// them during Extract; the engine calls Link once after every file was extracted, with the
// deps of each file, to resolve references in place, drop non-deps and reset.
type Linker interface{ Link(files [][]dep.Dep) }
// FileEditor is optional: updates that also edit other files (gomod major module path updates
// rewrite import paths in the module's .go files). Build applies them with the update, puts
// them in the post-processing scope of their own path, and drops all of an update's files
// when one fails.
type FileEditor interface {
    FileEdits(root string, u dep.Update) (map[string][]patch.Edit, error) // by slash path relative to root
}
// LockReader is optional: after Link, the engine calls ReadLocks once with the absolute root and
// the deps of each file; it sets Dep.Locked/LockUnread in place (npm, cargo, Python) and returns lockfile errors.
type LockReader interface{ ReadLocks(root string, files [][]dep.Dep) []error }
// RangeUpdater is optional: managers whose Edits rewrite a whole range (Python specifiers) report
// whether it admits a version and the range Edits would write (false: unchanged or not admitting it).
type RangeUpdater interface {
    InRange(d dep.Dep, v string) bool
    NewRange(d dep.Dep, v string) (string, bool)
}
// actions.New(), gomod.New(), docker.New(), npm.New(), piprequirements.New(), pep621.New(), poetry.New(), pipenv.New(), helm.New(), …, cargo.New(), maven.New(), gradle.New(), gradlewrapper.New(), mavenwrapper.New() return the built-in managers; custom.New(cfg) one per cfg.Custom entry (accepting custom.<name> of cfg.CustomDatasources).
```

```go
// internal/datasource (leaf package; aliased as engine.Release, engine.Datasource, engine.Vuln, engine.SecuritySource, engine.ChecksumSource)
type Release struct{ Version, Digest string }
type Datasource interface {
    Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) // *SkipError: skip d, not an error
    Digest(ctx context.Context, d dep.Dep, version string) (string, error) // when Release.Digest == ""
}
type Vuln struct{ Advisory dep.Advisory; Fixed []string }
type SecuritySource interface {
    Advisories(ctx context.Context, deps []dep.Dep) ([][]Vuln, error)
}
type ChecksumSource interface {
    Checksum(ctx context.Context, d dep.Dep, version string) (string, error) // lowercase hex
}
type Timer interface { // optional: release times for ages and the older-than-current check
    ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error)
}
type SourceRepo interface { // optional: source repository for release notes
    SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error)
}
type Planner interface { // optional: all updatable deps of the datasource, once, before the first lookup
    Plan(deps []dep.Dep)
}
type Revision struct{ Version, Digest string; Time time.Time }
type HeadSource interface { // optional: newest revision of a commit-tracking dep (Go pseudo-versions: @latest)
    Head(ctx context.Context, d dep.Dep, dir string) (Revision, error)
}
type ModulePath struct{ Name string; Releases []Release }
type MajorPathSource interface { // optional: successive major module paths (/v<N+1>, …) that exist, lowest first
    MajorPaths(ctx context.Context, d dep.Dep, dir string) ([]ModulePath, error)
}
// gittags.Lister ("git-tags"), goproxy.Source ("go"), gotoolchain ("go-toolchain"),
// dockerds ("docker"), githubreleases ("github-releases"), npmds ("npm"), nodeversion ("node-version"),
// pypi ("pypi"),
// helmds ("helm", a Planner), crate ("crate", an io.Closer for its git index clones), tfregistry.Source ("terraform-provider", "terraform-module"), mavends ("maven", also a SourceRepo and checksum.ArtifactOpener), gradleversion ("gradle-version"); goproxy.Source is also a HeadSource and a MajorPathSource
// and customds ("custom.<name>", one per customDatasources entry) implement Datasource; osv implements SecuritySource;
// checksum.Source implements ChecksumSource.
```

```go
// internal/versioning
type Scheme interface {
    Valid(v string) bool
    Compare(a, b string) int
    Kind(cur, next string) dep.UpdateType
    Semver(v string) *semver.Version
    Best(cur string, cands []string, accept func(v string, kind dep.UpdateType) (bool, string), reject func(v, reason string)) (string, bool)
    Rank(cur string, cands []string, accept func(v string, kind dep.UpdateType) (bool, string), reject func(v, reason string)) Ranking
    Compatible(cur, next string) bool
}
type Ranking interface{ Len() int; At(i int) Candidate } // best first; candidates are built on access
type Candidate struct{ Version string; Kind dep.UpdateType; Major string } // Major: decimal major of a major candidate
func Get(name string) (Scheme, bool)    // "tag", "go", "docker", "semver", "loose", "maven", "regex:<RE2>"
func Parse(name string) (Scheme, error) // Get with the reason; regex schemes are compiled once per name and cached
```

```go
// internal/engine
type PostProcessor interface {
    Scope(root, manifest string) (dir string, ok bool) // slash dir relative to root owning the derived files; !ok = patch only
    Siblings() []string                                // file names in the scope dir read or regenerated
    // manifests: patched content by path relative to dir; files: existing siblings by name.
    // A result under a manifest's path replaces it, one under a sibling's name the sibling.
    Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error)
}
// LockfileUpdater is optional on a PostProcessor: a scope with lockfile-only updates calls
// ProcessLocked instead of Process (locks by manifest path relative to dir; their manifests unchanged).
type LockfileUpdater interface {
    ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error)
}
// UpdatePinner is optional on a PostProcessor and preferred over Process and ProcessLocked: it
// also gets the scope's manifest updates (ups), so cargo can resolve an update whose rewritten
// requirement still admits the locked version to NewVersion.
type UpdatePinner interface {
    ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error)
}
type Options struct {
    Managers    []manager.Manager
    Datasources map[string]Datasource // by Dep.Datasource
    Security    SecuritySource        // nil = off
    Checksums   ChecksumSource        // nil = deps with a checksum source cannot be updated
    Filter      func(dep.Dep) bool    // nil = all
    Trace       func(Trace)           // nil = off
    Now         time.Time             // reference time of minimumReleaseAge; zero = time.Now()
}
type Trace struct {
    Dep        dep.Dep
    Rules      []int          // matching rules of the pre-policy
    Policy     config.Policy  // pre-policy
    Candidates int
    Rejected   map[string][]string // reason -> versions
    Updates    []TraceUpdate       // one per PR bucket, lowest version first; nil = none
    Err        error
}
type TraceUpdate struct{ Update dep.Update; Policy *config.Policy } // Policy: of Update.Type (updateTypes rules included)
type Result     struct{ Updates []dep.Update; Skipped []dep.Dep; Errors []error; Deps []dep.Dep } // Deps: every extracted dep, Skipped included (dashboard)
type FileChange struct{ Path string; Mode fs.FileMode; Old, New []byte }
type Group      struct{ Manager, Name, Branch, Title string; Updates []dep.Update; Automerge bool; AutomergeStrategy string; AutomergeBranch bool } // Automerge: all updates automerge; strategy of the first; AutomergeBranch: all of them with automergeType branch

func Managers(cfg *config.Config) ([]manager.Manager, error)
func Resolve(ctx context.Context, root string, cfg *config.Config, env manager.Env, o Options) (*Result, error)
func Build(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, post map[string]PostProcessor) ([]FileChange, []error) // post by manager name; nil = none
func GroupUpdates(updates []dep.Update, branchPrefix string) []Group // "" = config.DefaultBranchPrefix
func Local(updates []dep.Update) (local []dep.Update, separate []bool) // the one update per dep that plan/apply write
```

**Resolve order per dep:** pre-policy (its `versioning` replaces the dep's scheme before the current value is validated) → datasource releases → `Scheme.Rank` under the per-kind policy → one candidate per PR bucket, with the release-time guards below → digest pin/refresh when no bucket has an update (digest-capable managers github-actions and docker, whatever their versioning, and custom deps that carry a digest) → security. With advisories, the minimum fix F is the highest, over all vulns, of each vuln's lowest compatible fixed version. The lowest update at or above F carries the advisories. If there is none, the smallest compatible release ≥ F is chosen regardless of `allow`/`updates`, `minimumReleaseAge` and of rules with `updateTypes` (a pre-policy `ignore` still wins); it replaces the update of its bucket (which is below F) and any digest or pin update. The advisories the chosen version fixes are attached to that one update; such security updates get no group and no schedule. Every file goes to every manager that matches it.
- **Per-kind policy.** `config.Policy(d)` is the pre-policy (rules without `updateTypes`); it decides `ignore` for the dep, `pin`, `versioning`, and whether the dep is looked up at all. `config.PolicyFor(d, kind)` evaluates all rules in file order, those with `updateTypes` only when they list `kind`. The engine caches the five per-kind policies of a dep (evaluated lazily, and only when the config has `updateTypes` rules). Each candidate is accepted under the policy of its own kind: an ignoring per-kind policy rejects it as `ignored by rule #i for <kind>` (or the default-ignore reason), otherwise `updates`/`allow` decide as before; the bucket's next candidate is considered instead, so ignoring indirect majors keeps their minor and patch updates. Digest refresh and pin need `digest`/`pin` enabled and not ignored in their per-kind policy.
- **PR buckets** (Renovate's `separateMajorMinor`, `separateMultipleMajor`, `separateMinorPatch`; its `getBucket`). The ranking is split into buckets and each bucket yields at most one update, its best candidate that passes the policy and the release-time guards (the guards and their bounds run per bucket). The bucket of a candidate comes from the policy of its kind, i.e. the pre-policy unless an `updateTypes` rule listing that kind sets the flags (Renovate evaluates these options before the update type is known, so it ignores them on `matchUpdateTypes` rules; OCM therefore sets them on manager rules, which behaves the same here):
  - `separateMajorMinor: false`: one bucket `""`, the newest update of any kind.
  - otherwise majors go to `major` (the newest major), or with `separateMultipleMajor` to `v<N>` per newer major `N` (the newest release of each); minors and patches share `""` (the newest), or with `separateMinorPatch` get `minor` (the newest minor) and `patch` (the newest patch of the current minor).
  - Digest refreshes and pins are the bucket `""` (they only exist when no version update does). A dep's updates are ordered by version, lowest first, and all share the dep's other spans, so each bucket is built on its own branch (`Update.Bucket` becomes a branch suffix, section 5). `plan`/`apply` write one update per dep (`engine.Local`): the newest non-major one, else the lowest major; the others are reported with a `, separate PR` hint.
- A dep the pre-policy ignores is still resolved when some per-kind policy un-ignores it (`updateTypes: [patch], ignore: false`), only for those kinds; it gets no security lookup.
- **Non-version values.** A current value the dep's versioning scheme cannot parse is skipped as `non-version value "<v>"` before any lookup, not reported as an error. The exception is a docker-datasource dep whose digest can be maintained (docker manager, or a custom dep with a digest), e.g. `latest@sha256:…`: it gets digest-only resolution without listing releases or asking OSV. Pinned → the digest of the same tag is refreshed (`digest`, no update when unchanged); unpinned with `pin: digest` → `pin`; unpinned with `pin: keep` → skipped as `non-version tag`. Go pseudo-versions are the other exception (section 2, gomod): releases are ranked against the pseudo-version's base (`vX.0.0-0` without one), only releases newer than the pseudo-version count, and the head's newer pseudo-version takes the place of the digest refresh.
- **Major module paths** (gomod) come after security: each successor path's best release takes the bucket a major falls into (`major`, `v<N>` with `separateMultipleMajor`, `""` without `separateMajorMinor`), replacing a lower update there unless that one carries advisories.
- **Release-time guards** (each candidate's time is asked at most once per dep, only when the datasource implements `datasource.Timer`). A bucket's best candidate is rejected and the bucket's next one tried (bounded per bucket: 3 `older than current`, 3 `release time unknown`, 50 rejections in total, then no update for that bucket):
  - A major candidate whose release time predates the current version's is rejected as `older than current`. This drops legacy pre-module tags such as `k8s.io/client-go v1.5.2` (2016) when the current version is `v0.37.1`. It is skipped for docker and git-tags deps (`movingTimes`): their times can be those of the content a floating tag points to now (an image rebuild; the release of the commit a floating `v5` points to, which moves with every backport and can be minutes newer than `v7`'s), not of the version's first release.
  - With a `minimumReleaseAge` in the policy of the candidate's kind, a candidate released less than that long before `Options.Now` is rejected as `too new (released <date>, minimum <age>)`; the bucket's next lower candidate is tried. Without `separateMajorMinor` all kinds share one bucket, so a too-new major falls back to an old enough minor.
  - A datasource implementing `datasource.ReleaseBound` names a time no version was published after, from data its `Releases` already fetched; when that bound is at least the age before `Options.Now` the candidate passes without a release-time lookup, which cannot change the outcome. npm uses the abbreviated document's `modified` (every publish updates it), so the full packument is only fetched for majors (`older than current`) and packages changed within the age. On backstage with a 3-day age this cut full packument fetches 459 → 218 (87 → 57 MB gzip).
  - Unknown times (zero, a lookup error logged at debug level, or a datasource without `Timer`) depend on the policy's `minimumReleaseAgeBehaviour` (Renovate's option; `config.Policy.TimestampRequired`). `timestamp-optional`, the default, never rejects. `timestamp-required` rejects as `release time unknown (minimum <age>, timestamp-required)`, reported like a too-new candidate (trace, `Result.Pending`, the summary's held-back list); after 3 such candidates in a bucket it stops, without prefetching, since the datasource likely has no times for the dep. Digest refreshes and pins (no new version) and security updates are exempt from the age in both modes; Go pseudo-version digest updates are not (the new pseudo-version has a commit time; without one it is held under `timestamp-required`), and neither are major module paths.
- The chosen update (also digest, pin and security updates) takes `CommitType`/`CommitScope` and `Automerge`/`AutomergeStrategy` and, unless it is a security update, `Group`/`Schedule` from the policy of its type (`config.Policy` also has `Monorepo`, the built-in group that applied, and `Kind`, the type a `PolicyFor` result is for).

```go
// internal/platform
type Kind string // "github" | "gitea" | "forgejo"
type Info struct{ Kind Kind; ServerURL, APIURL, Host, Repo string } // Repo = "owner/name"
type PR struct {
    Number int; Title, Body, Head, HeadSHA, State, URL string; Merged bool; Mergeable *bool
    CreatedAt time.Time // zero when omitted
    NodeID    string    // GitHub GraphQL id; "" on Gitea/Forgejo
    AutoMerge bool      // GitHub auto_merge non-null; always false on Gitea/Forgejo (not reported)
}
type Issue struct{ Number int; Title, Body, URL string }
var ErrAutoMergePending, ErrAutoMergeUnavailable, ErrIssuesDisabled error
type Client interface {
    DefaultBranch(ctx context.Context) (string, error)
    ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) // "open" | "closed"
    GetPR(ctx context.Context, number int) (PR, error)
    CreatePR(ctx context.Context, head, base, title, body string) (PR, error)
    UpdatePR(ctx context.Context, number int, title, body string) error
    ClosePR(ctx context.Context, number int) error
    EnableAutoMerge(ctx context.Context, pr PR, strategy string) error // ErrAutoMergePending, ErrAutoMergeUnavailable
    Checks(ctx context.Context, sha string) (CheckState, error)       // none, pending, success, failure over statuses and check runs
    MergePR(ctx context.Context, pr PR, strategy string) error         // pinned to pr.HeadSHA
    ListIssues(ctx context.Context, maxPages int) ([]Issue, error)     // open, PRs excluded; ErrIssuesDisabled
    CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error)
    UpdateIssue(ctx context.Context, number int, body string) error
}
type Overrides struct{ Kind, APIURL, Repo string } // from flags; "" = auto
func FromEnv(getenv func(string) string) (Info, bool)
func Detect(ctx context.Context, getenv func(string) string, remoteURL string, o Overrides, c *http.Client) (Info, error)
func NewClient(info Info, token string, c *http.Client) Client
func DefaultActionsHost(info Info, getenv func(string) string) (host, source string)

// platformCommit (GitHub only); cmd wraps it with gitx.Repo into a bot.Git
func IsAppToken(token string) bool // "ghs_" prefix: App installation token (also GITHUB_TOKEN)
type CommitFile struct{ Path string; Executable bool; Content []byte } // nil Content deletes
func NewGitHubCommits(ctx context.Context, info Info, token string, c *http.Client, prefix string) (*GitHubCommits, error) // looks up the token's account
func (g *GitHubCommits) Author() (name, email string) // <id>+<login>@users.noreply.<host>
func (g *GitHubCommits) Commit(ctx context.Context, parent string, files []CommitFile, msg string) (string, error)
func (g *GitHubCommits) Push(ctx context.Context, commit, branch, expected string) error // updateRefs, beforeOid = expected
func (g *GitHubCommits) BranchTip(ctx context.Context, branch string) (author string, parents []string, err error)
```

```go
// internal/bot
type Git interface {
    RevParse(ctx context.Context, rev string) (string, error)
    RemoteURL(ctx context.Context) (string, error)
    LsRemote(ctx context.Context, pattern string) (map[string]string, error)
    WriteTree(ctx context.Context, parent string, changes []engine.FileChange) (string, error) // local temporary index; equals the tree of Commit's commit
    Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error)
    Push(ctx context.Context, commit, branch, expected string) error // gitx.ErrStale (lease or non-fast-forward), gitx.ErrRejected (origin declined)
    DeleteBranch(ctx context.Context, branch string) error
    BranchTip(ctx context.Context, branch string) (gitx.Tip, error) // Tip{Author, Parents, Tree}
}
type Options struct {
    Base, AuthorName, AuthorEmail string
    BranchPrefix                  string // ending in '/'; "" = config.DefaultBranchPrefix
    DryRun                        bool
    RebaseWhen                    string // config.RebaseWhens; "" = conflicted
    UpdateNotScheduled            bool   // update and rebase open PRs outside their schedule (config updateNotScheduled)
    MaxNewPRs                     int    // PRs created per run (--max-new-prs); 0 = unlimited
    ConcurrentLimit, HourlyLimit  int    // prConcurrentLimit, prHourlyLimit; 0 = unlimited
    IgnoredAuthors                []string // gitIgnoredAuthors: branch-tip emails that count as the bot's own
    Managers                      []manager.Manager
    Now                           time.Time // prHourlyLimit reference; zero = time.Now()
    Trigger                       schedule.Trigger // what started the run (schedule.Detect)
    Notes                         Notes     // nil = no ages/notes
    Dashboard                     *DashboardOptions // nil = no dashboard; an existing one is left alone
}
type DashboardOptions struct{ Title string; Labels []string }
type Notes interface { // implemented by releasenotes.Notes
    Section(ctx context.Context, ups []dep.Update) string
    Age(ctx context.Context, u dep.Update) string
}
type Action struct {
    Branch, Title, Status, URL string; Number int
    Automerge string // automerge outcome appended to the status ("created, automerge"); "" = not automerged
    Dashboard bool   // the dashboard issue, not a PR
}
func Run(ctx context.Context, o Options, root string, res *engine.Result, post map[string]engine.PostProcessor, pc platform.Client, g Git) ([]Action, []error)
```

### Versioning

- **Actions tags.** A tag must match `^(v?)(\d+)(?:\.(\d+))?(?:\.(\d+))?$`; suffixed and prerelease tags are never considered. Candidates must have the same prefix and the same number of parts as the current tag. This precision is what makes `@v4` move to `@v5` and `# v7.0.1` move to `# v7.0.2`.
- **Go modules.** `golang.org/x/mod/semver` syntax and ordering, each version parsed once (section 3). Excluded candidates:
  - pseudo-versions (a pseudo-version as the *current* version gets digest updates instead, section 2);
  - prereleases, unless the current version is a prerelease;
  - `+incompatible` versions, unless the current version has it;
  - other majors, except the step from v0 to v1.
- **Docker tags (`docker`).** `^(v?)(\d+(?:\.\d+)*)(.*)$` → prefix, numeric parts, suffix. Candidates must have the same prefix, the same number of parts and an identical suffix; `latest` and other non-numeric tags are invalid, and so are commit hashes (7–40 lowercase hex digits, not all decimal, as in Renovate), which would otherwise parse as `3a22eba…` → version `3` with a suffix no other tag shares.
- **Semver (`semver`).** Masterminds loose semver. Prereleases only if the current version is one; any precision. The result keeps the current version's `v` prefix style. Default for `custom[]`; used by npm, whose versions have no `v` and whose prerelease candidates (`2.0.0-rc.1`) are only considered from a prerelease.
- **PEP 440 (`pep440`).** Used by the Python managers. PEP 440 ordering; pre and dev releases only from a pre or dev release, local versions (`+local`) only from a local version. The current precision is kept (`~=4.2` → `~=5.1`).
- **Loose (`loose`, Renovate's loose).** 1–6 numeric dot parts, an optional `v` (candidates must share the current one), an optional suffix after `-` or `_` (`2.0-rc1`, `5_beta`) that counts as a prerelease, so suffixed candidates are only considered from a suffixed version. Parts compare numerically with a missing part read as 0 (`1877` < `1877.25`; Renovate ranks a shorter version higher instead), then a release above its prereleases, prereleases by dot-separated identifiers (numeric ones numerically), and finally fewer parts below more (`1.2` < `1.2.0`). Kind by position: part 0 major, part 1 minor, later parts and suffix changes patch. Any precision is a candidate, so combine it with an `allow` regex to keep one (`/^\d+\.\d+$/`). `Semver` builds `MAJOR.MINOR.PATCH[-suffix]` from the first three parts.
- **HashiCorp (`hashicorp`, Renovate's hashicorp).** 1–3 numeric parts, optional `v`, `-prerelease` only on full versions. Candidates are rewritten to the current value's precision and `v` style before ranking (`5.0` with `5.31.2` → `5.31`; `1.5.7` with the tag `v1.9.5` → `1.9.5`), duplicates ranked once; a prerelease is not cut to a partial value (it would read as a release). Prereleases only from a prerelease. Used by the terraform managers, whose constraint operators stay outside the version span.
- **Maven (`maven`).** Maven's `ComparableVersion` (the 3.x algorithm before the 3.9 combination items): lowercased, split on `.`, `-` and digit/letter transitions, with `-` and transitions opening sublists and trailing nulls (`0`, `""`, `ga`/`final`/`release`) dropped, so `1` = `1.0` = `1.0-final`; qualifiers `alpha` (`aN`) < `beta` (`bN`) < `milestone` (`mN`) < `rc` (`cr`) < `snapshot` < release < `sp` < unknown ones (alphabetical); numbers above lists above strings. Valid: a leading digit, then `[0-9A-Za-z._-]` (so `${prop}`, ranges and `LATEST` are not versions). SNAPSHOT candidates are always rejected, other prereleases only from a prerelease; the first unknown qualifier after a `-` must match (`33.3.1-jre` never becomes `-android`), while words after `.` (Jetty `.v20230217`, Jenkins `.v<hash>`) are not compared. Kind by the first three top-level numbers. Used by maven, gradle and gradle-wrapper.
- **Regex (`regex:<RE2>`, Renovate's regex).** The RE2 matches the version (anchor it with `^…$`) and names its parts: `major` (required), `minor`, `patch`, `build`, `revision` (numeric; absent groups read as 0), `prerelease` and `compatibility`; other named groups are errors, so a typo cannot silently read as 0. Go's regexp (since Go 1.22) accepts Renovate's `(?<name>…)` as well as `(?P<name>…)`, so expressions from `renovate.json5` are used unchanged (no translation step). Candidates must have the current `compatibility` (else `compatibility`), prereleases only from a prerelease. Ordering: major, minor, patch, build, revision numerically, then a release above its prereleases, prereleases by identifiers. Kind: major or minor by group, otherwise patch (build, revision and prerelease changes). `Semver` is `MAJOR.MINOR.PATCH[-prerelease]` for `allow` constraints. Schemes are compiled when the config is loaded and cached by name. OCM's cosign fork: `regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$` orders `v3.1.4+ocm.6` < `v3.1.5+ocm.1` (patch) and matches both the `+ocm.N` release and the `-ocm.N` image tag.
- **`go`/`toolchain` directives** use `tag` versioning against the `go-toolchain` releases: every stable `X.Y.Z` plus a derived `X.Y` per minor, so `go 1.26` moves to `go 1.27` and `go 1.26.1` to `go 1.27.1`. Prerelease toolchains (`go1.27rc1`) are skipped.
- **Rule versioning.** `rules[].versioning` replaces the manager's scheme for matching deps before resolution (pre-policy only; rejected together with `updateTypes`). It applies to every place the dep's scheme is used: validation of the current value (a value the new scheme cannot parse is skipped as `non-version value`), ranking, kinds, `allow` conversion and security fixes.
- Every scheme reports each dropped candidate with a reason (`invalid`, `prerelease`, `precision`, `prefix`, `suffix`, `major`, `compatibility`, `not newer`, `policy: <why>`) for `Trace`; policy reasons are `update type <k> not allowed` and `outside allow <constraint or /regex/>`.

## 5. Platforms

### Detection order

1. **Environment** (no network):
   1. `FORGEJO_ACTIONS=true` → forgejo. Reads `FORGEJO_SERVER_URL`, `FORGEJO_API_URL` and `FORGEJO_REPOSITORY`, each falling back to the `GITHUB_*` name.
   2. `GITEA_ACTIONS=true` → gitea, from the `GITHUB_*` variables.
   3. `GITHUB_ACTIONS=true` → github, from the `GITHUB_*` variables (covers GHES).
2. **The `origin` remote URL.** Accepted forms: `https://host/owner/repo(.git)`, `git@host:owner/repo(.git)` and `ssh://git@host[:port]/owner/repo(.git)`.
   - Host `github.com` → github, API `https://api.github.com`.
   - Any other host is probed unauthenticated, with a 10s timeout each, in this order:
     1. `/api/forgejo/v1/version` returns 200 → forgejo.
     2. `/api/v1/version` returns 200 with JSON `version` → gitea. Both use API `https://host/api/v1`.
     3. `/api/v3/meta` returns 200 → GHES, API `https://host/api/v3`.
     4. Nothing matches → error `cannot detect platform for <host>; pass --platform and --api-url`.

     Instances that require sign-in for the whole API answer probes 1 and 2 with 401/403 and a JSON `message` body; that also counts as a match. Missing paths return 404, so other servers are not misdetected.
3. **Flags.** `--platform`, `--api-url` and `--repo` override the detected values. If only the kind is overridden, the API URL is derived with the per-kind rule above.

**Default host for `owner/repo@ref`**, first match wins:
1. `actions.defaultHost` in the config (source `config`).
2. `DEPS_DEFAULT_ACTIONS_URL` (source `env`).
3. Platform default (source `platform default`): forgejo → `data.forgejo.org`; github, gitea or unknown → `github.com`.

`DEPS_DEFAULT_ACTIONS_URL` is not set by the composite action (it uses no `gitea.` expressions, so it runs unchanged on GitHub). Gitea's `gitea_default_actions_url` is not exposed to expressions by the Gitea 28 runner anyway, so the platform default applies. Instances with a non-default `DEFAULT_ACTIONS_URL` set `actions.defaultHost` in `.deps.yaml`.

### Endpoints

**GitHub.** Headers: `Authorization: Bearer <t>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`.

| Method | Endpoint |
|---|---|
| DefaultBranch | `GET {api}/repos/{repo}` → `.default_branch` (the repository JSON is fetched once per run and also gives `allow_auto_merge`, `allow_merge_commit`, `allow_squash_merge`, `allow_rebase_merge`, `has_issues`) |
| ListPRs | `GET {api}/repos/{repo}/pulls?state=<s>&sort=updated&direction=desc&per_page=100&page=N` |
| GetPR | `GET .../pulls/{n}` (nullable `mergeable`, `node_id`, `head.sha`, nullable `auto_merge`) |
| CreatePR | `POST .../pulls {title,head,base,body}` |
| UpdatePR | `PATCH .../pulls/{n} {title,body}` |
| ClosePR | `PATCH .../pulls/{n} {state:"closed"}` |
| EnableAutoMerge | `POST {graphql}` mutation `enablePullRequestAutoMerge(input:{pullRequestId:<node_id>, mergeMethod:MERGE\|SQUASH\|REBASE})`; not sent when `allow_auto_merge` is `false` |
| Checks | `GET .../commits/{sha}/status` (`state`, `total_count`) and `GET .../commits/{sha}/check-runs?per_page=100&page=N` (`status`, `conclusion`); a failed status or check run wins over an unfinished one |
| MergePR | `PUT .../pulls/{n}/merge {merge_method, sha}` |
| ListIssues | `GET {api}/repos/{repo}/issues?state=open&per_page=100&page=N`, entries with `pull_request` dropped; 410 → issues disabled |
| CreateIssue | `POST .../issues {title,body,labels:[names]}` |
| UpdateIssue | `PATCH .../issues/{n} {body}` |

On GitHub, `Merged` is `merged_at != null` and `AutoMerge` is `auto_merge != null`. `{graphql}` is `https://api.github.com/graphql`, or `https://<host>/api/graphql` on GHES.

**Gitea / Forgejo.** Header: `Authorization: token <t>`.

| Method | Endpoint |
|---|---|
| DefaultBranch | `GET {api}/repos/{repo}` (also `default_merge_style`, `has_issues`, `external_tracker`) |
| ListPRs | `GET {api}/repos/{repo}/pulls?state=<s>&sort=recentupdate&limit=50&page=N` |
| GetPR / CreatePR / UpdatePR / ClosePR | same paths and bodies as GitHub |
| EnableAutoMerge | `POST .../pulls/{n}/merge {do, merge_when_checks_succeed: true, delete_branch_after_merge: true, head_commit_id}`; the style field is `do` on Gitea 28 and `Do` on Forgejo (both `swagger.v1.json`). 409 `already scheduled` is success; 405 `Please try again later` is pending. |
| Checks | `GET .../commits/{sha}/status` (Actions report commit statuses); `total_count` 0 → none, `success`, `pending`, anything else (`failure`, `error`, `warning`) → failure |
| MergePR | `POST .../pulls/{n}/merge {do, delete_branch_after_merge: true, head_commit_id}` |
| ListIssues | `GET {api}/repos/{repo}/issues?state=open&type=issues&limit=50&page=N`; a 404 with `has_issues: false` or an `external_tracker` → issues disabled |
| CreateIssue | `POST .../issues {title,body,labels:[ids]}`; ids from `GET .../labels` and `GET {api}/orgs/{owner}/labels` (case-insensitive names) |
| UpdateIssue | `PATCH .../issues/{n} {body}` |

On Gitea and Forgejo, `Merged` is the `merged` field and `Mergeable` is the `mergeable` field. Their PR JSON does not report a scheduled auto-merge, so `AutoMerge` is always false and the request is repeated (idempotently) on later runs.

Both clients share these rules:
- `Head` = `head.ref`; `URL` = `html_url`.
- Paging stops at a short page or at `maxPages`.
- Errors have the form `<METHOD> <path>: <status>: <first 200 bytes of body>`.

**Git (all platforms).**
- Tags are fetched from `<source>/info/refs?service=git-upload-pack`, then `POST <source>/git-upload-pack` (`ls-refs`). Servers that only speak protocol v0 are handled by parsing the advertisement and using the `^{}` peeled lines.
- Auth uses HTTP Basic with user `x-access-token`.

**GitHub releases (datasources and notes).** With a token for the host: `POST {api}/graphql` (github.com `https://api.github.com/graphql`, GHES `https://<host>/api/graphql`) with `Authorization: Bearer <t>`. Without one: REST `GET {api}/repos/{repo}/releases?per_page=100&page=N` and `/releases/tags/{tag}`. GraphQL answers failed queries with 200 and an `errors` array: `NOT_FOUND` is treated as a 404, `RATE_LIMITED` fails with the rate-limit reset time.

### Token sources

1. **Platform API:** `--token`, then the first non-empty of `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, then the password from `git credential fill` for the platform host.
   - If no token is found, `run` fails with `no token for <host>`.
   - With `--dry-run`, the API is used anonymously instead.
2. **Tag lookups:** the platform token is used for the platform host. `DEPS_GITHUB_COM_TOKEN` is used for github.com when the platform is not github.com.
   - On a 401 without static credentials, deps runs `git credential fill` once per host (`GIT_TERMINAL_PROMPT=0`, 10s timeout), caches the result (success or failure), and retries once.

### PR lifecycle (`deps run`)

1. **Base branch:** `--base`, or the repo's default branch.
2. **Freshness check:** local `HEAD` must equal the remote tip of `origin/<base>`. Otherwise deps fails with `checkout HEAD <h7> is not the tip of origin/<base> (<r7>); check out the latest <base>`.
3. **State:**
   - remote `refs/heads/<branchPrefix>*` (`branchPrefix`, default `deps/`);
   - open PRs (up to 20 pages) and closed PRs (up to 4 pages);
   - both PR lists are filtered to head branches under `branchPrefix`; the bot never touches other branches or PRs;
   - with `dependencyDashboard`, the dashboard issue and its ticked checkboxes (see [Dependency dashboard](#dependency-dashboard)). A failed issue list is an error and the dashboard is not written this run; a repository without issues skips the dashboard (`skipped: issues disabled`, a warning).
4. **Grouping:**
   - Updates whose policy sets `group` form one group per group name: branch `<prefix>group/<slug(group)>`, title `<type>(<scope>): update <group> (<n> dependencies)`, `update <group> to <v>` when all members move to one version, or the single-dependency title below when only one dependency is in it.
   - Every other update forms one group per (manager, name): branch `<prefix><manager>/<slug(name)>`.
   - **PR buckets** split both kinds of groups: updates of one dep in different buckets (section 4) never share a group, and a non-empty bucket is appended to the branch as `-<bucket>`: `-major`, `-v<N>` (`separateMultipleMajor`; deps uses the major number where Renovate writes `<N>.x`), `-minor`, `-patch`. The non-major bucket (and the only bucket with `separateMajorMinor: false`) has no suffix, so its branches keep their names. A policy group gets the suffix of its members' bucket: `<prefix>group/<slug>-patch`. Titles carry their version as before (`update actions/checkout to v7.0.1`).
   - **Built-in monorepo groups:** a curated list of about 60 single-repository monorepos (kubernetes staging modules, opentelemetry-go, aws-sdk-go-v2, babel, typescript-eslint, angular, ...) is applied as implicit rules before the user rules, setting only `group: <name> monorepo`. Any user rule overrides it; `monorepos: false` disables the list; `deps explain` shows them as `builtin`.
   - **Shared versions:** deps whose `VersionRef` points at the same definition (a Maven property, a Gradle `version.ref` or variable: same Manager, File and RefSpan) and that get updates from at least two distinct deps are updated together. Per bucket every member takes the lowest member `NewVersion` (each member's update is its best accepted candidate, so the lowest is the highest version all are known to reach; a lowered member's `Type` is recomputed and it loses its advisories). All members share one group: the first member's policy group, else the `VersionRef` (`deps/group/jackson.version`, title `update jackson.version to 2.16.0`); such groups keep security members. `Local` (plan/apply) keeps every member update that makes the same edit, which `patch.Apply` applies once.
   - Security updates (with advisories) are never put into a policy group, except members of a shared version (above), which cannot be split. Title: `fix(<scope>): update <name> to <ver> [security]`, or `fix(<scope>): update <name> [security]` when versions differ.
   - **Semantic prefix** (Renovate's `:semanticPrefixFixDepsChoreOthers`): production deps (gomod `direct`, npm and cargo `dependencies`, poetry `dependencies`, pep621 `project.dependencies`, pipenv `packages`, maven `compile`/`provided`/`runtime`/`system`/`import`/`parent`) use `fix(deps):`, everything else `chore(deps):`, security updates always `fix`. Rules override with `commitType` and `commitScope` (`""` drops the scope: `chore: ...`). A group uses `fix` if any member is `fix`, else the first member's type, and always the first member's scope.
   - Other titles, first match wins (`<p>` = the semantic prefix):
     - every update is a pin with the same digest: `<p> pin <name> to <sha7>`;
     - every update is a digest refresh with the same digest: `<p> update <name> digest to <sha7>`;
     - every update shares one new version: `<p> update <name> to <ver>`;
     - otherwise: `<p> update <name>`.
   - `slug` lowercases, maps bytes outside `[a-z0-9._-]` to `-`, collapses runs of `-` and trims `-`.
   - Changes are built for at most 4 groups concurrently (groups that will be pushed: in schedule, or with an open PR under `updateNotScheduled`), then the groups are handled one after another. Every group is built from its own updates only, so a dep's buckets never conflict.
5. **Fingerprint:** the first 16 hex characters of sha256 over the sorted `path\x00newcontent` pairs.
   - The PR body is a `| File | From | To | Type |` table (with an *Age* column when release notes are enabled), a `### Security` section listing the advisories (if any), the release notes section, then `<!-- deps:fingerprint=<fp> -->`.
   - The commit message is the title, a blank line, then one line per update: `- <file>:<line> <from> -> <to>`.
6. **Per-group decision**, first match wins:
   Dashboard requests for a branch apply first: `create` and `recreate` bypass the schedule (the group is built) and the new-PR limit, `recreate` also bypasses rule 1, and `rebase` bypasses the schedule, the limit and rule 5 and turns an unchanged open PR into a rebuild (`rebased`).
   1. No open PR exists, and a closed PR has the same branch, was not merged and has the same title → `skipped: closed PR for this version`.
   2. A non-security update of the group is outside its schedule: the run was triggered by a cron (`GITHUB_EVENT_NAME=schedule`) whose expression, read from the `schedule` field of the event payload (`GITHUB_EVENT_PATH`; filled by GitHub and by Gitea 28.3, `services/actions/schedule_tasks.go`) or from `--trigger-schedule`/`DEPS_TRIGGER_SCHEDULE`, is not in the update's `schedule` (compared after collapsing whitespace; never evaluated as a time window), or the run was triggered by `push`, which is outside every schedule (follow-up maintenance after a merge must not open the weekly groups early). `workflow_dispatch`, local runs and schedule events without a readable cron (warned) are in schedule for every group. With `updateNotScheduled: true` (the default, as in Renovate) a group with an open PR continues below, so content changes, conflicts and `rebaseWhen` keep the PR current, and only creating a new PR waits for a matching trigger → `skipped: outside schedule`. With `updateNotScheduled: false` every out-of-schedule group is `skipped: outside schedule`. Nothing is built or pushed for skipped groups.
   3. The open PR's fingerprint matches (its content is unchanged):
      - `rebaseWhen: never` → `up-to-date`, even when the PR conflicts.
      - The PR is not unmergeable and `rebaseWhen` is `conflicted` (the default), or the branch does not exist → `up-to-date`. On GitHub, a nil `mergeable` triggers a `GetPR` call; nil still counts as mergeable.
      - The PR is not unmergeable and `rebaseWhen: behind-base-branch`: the branch tip is fetched (`BranchTip`). If it is the bot's and its only parent is the base head → `up-to-date`; otherwise it is rebuilt on the base head (step 8, status `rebased`). deps branches are one commit on top of base, so the parent alone tells whether base moved.
      - An unmergeable PR falls through and is rebuilt (`updated`).
   4. No open PR exists, it is not an existing branch automerge branch, and the new-PR limit is exhausted → `skipped: new PR limit reached`. The limit of a run is the minimum over the non-zero limits: `--max-new-prs` (default 10) minus PRs (and branch automerge branches) created this run; `prConcurrentLimit` minus the open managed PRs; `prHourlyLimit` minus the managed PRs (open and closed) whose `created_at` is within the last hour. `0` means unlimited for each; `prConcurrentLimit` and `prHourlyLimit` default to 0 (Renovate defaults to 10 and 2).
   5. The branch exists and the author email of its last commit is neither the bot's (`AuthorEmail`) nor in `gitIgnoredAuthors` (case-insensitive) → `skipped: branch modified by someone else`. Such branches are never rebased.
   6. A branch automerge group (`Group.AutomergeBranch`) without an open PR and without the dashboard's `pr` request continues in [Branch automerge](#branch-automerge).
   7. `--dry-run` → `would-create`, `would-update` or `would-rebase`, with no git or API writes.
   8. Otherwise: commit on a temporary index, then `push --force-with-lease` against the expected sha (empty means the branch must not exist yet); with platform commits, the API commit and `updateRefs` with that sha as `beforeOid` (section 3). Then update the PR (`updated`) or create one (`created`). A rebase only pushes: content, title and body are unchanged, so the PR is not edited and no release notes are fetched (`rebased`, counted as updated in the run summary line).

   **Branch tip fetch:** `BranchTip` runs `git fetch --no-tags origin refs/heads/<b>` (with `--depth=1` in shallow clones, so full clones stay full) and parses the raw `git cat-file commit FETCH_HEAD` header for the author email, parents and tree; the raw object still lists the parent when a shallow graft hides it. With platform commits it reads `GET /commits/refs/heads/<b>` instead (`commit.tree.sha` is the tree) and reports the bot's email for a tip GitHub links to the token's account. It is fetched at most once per group and only when the branch exists and the author, parent or tree is needed.

   A build, git or API error in one group is recorded, and the next group is processed.
8. **Automerge:** for a group whose updates all automerge, after `created`, `updated`, `rebased` or `up-to-date` (see [Automerge](#automerge)). An automerge error is recorded; the PR's action is kept.
9. **Cleanup:** runs only when the whole run (engine and bot) produced zero errors and is not a dry run. Every open PR under `branchPrefix` whose branch matches no current group is closed and its branch deleted (`closed`). Then every branch under `branchPrefix` that matches no current group and has no PR in the open or closed lists is deleted (`deleted`, counted as closed in the run summary line) if its tip is the bot's: branch automerge branches whose update is gone, or merged ones whose deletion failed. Groups that are outside their schedule still count as current, so their PRs and branches are never removed as obsolete. This guard ensures that a failed upstream lookup never closes a valid PR.
10. **Dashboard:** with `dependencyDashboard`, the body is rendered from the actions, the engine and bot errors and `Result.Deps`, then the issue is created (with `dependencyDashboardLabels`), edited when the body changed, or left alone (`up-to-date`). A dry run reports `would-create`/`would-update`.

### Automerge

A group automerges when every update in it has `Automerge` from the policy of its type; its strategy is the first update's `AutomergeStrategy`. Mixed groups are not automerged. The request goes to the PR deps just created, updated or rebased (with the pushed head), or to the up-to-date open PR:

- **Already enabled** (GitHub `auto_merge` non-null) → `automerge enabled`, no request.
- **Dry run** → `would-automerge`, no request.
- **Gitea / Forgejo:** `POST /pulls/{n}/merge` with `merge_when_checks_succeed: true` schedules the merge; the forge merges once the required status checks succeed (right away when they already did) and deletes the branch. 201/200 and 409 `already scheduled` → `automerge`. 405 `Please try again later` (the mergeability check of a fresh push is still running, or the PR conflicts) → `automerge pending`, retried next run without an error. `head_commit_id` pins the head deps pushed or saw.
- **GitHub:** `enablePullRequestAutoMerge` → `automerge`. GitHub refuses it when auto-merge is disallowed in the repository (also known up front from `allow_auto_merge: false`) or when the PR has nothing left to wait for (`Pull request is in clean status`, also `unstable`/`has_hooks`). Then deps falls back to merging directly, but only when a fresh `GetPR` reports `mergeable: true` and `Checks(head.sha)` is `success`: at least one commit status or check run exists, the combined status is `success` (when there are statuses) and every check run is `completed` with `success`, `neutral` or `skipped`. Then `PUT /pulls/{n}/merge` with `sha` = the checked head → `merged`. Otherwise → `automerge waiting for checks`, and the next run checks again. A repository without any checks therefore never merges through the fallback.
- `auto` maps to the repository default: Gitea/Forgejo `default_merge_style` (else `merge`); GitHub the first allowed of merge commit, squash, rebase (else `MERGE`).
- Security updates follow the same policy.

### Branch automerge

`automergeType: branch` (Renovate's branch automerge) merges a group's branch without a PR. It applies to a group whose updates all automerge with that type (`Group.AutomergeBranch`), has no open PR, and was not ticked under *Pending branch automerge* on the dashboard (`deps:pr=<branch>`). An open PR (raised below, or left from `automergeType: pr`) is always automerged as a PR. Steps 1 to 5 of the per-group decision apply first; an existing branch outside its schedule is still handled under `updateNotScheduled`, like an open PR.

1. **Fresh branch:** the branch exists, its tip is the bot's, its only parent is the base head and its tree equals `WriteTree(head, changes)`. Anything else (no branch, base moved, new content, a dashboard rebase) is rebuilt and force-pushed: `created` (counts toward the new-PR limit), `updated` or `rebased`, outcome `branch automerge waiting for checks`. A dry run reports `would-…` with `would-automerge`. Unlike PR branches, a branch behind base is always rebuilt, whatever `rebaseWhen` says: only a branch directly on base can be fast-forwarded.
2. **Checks** of a fresh branch (`Checks(tip)`), status `up-to-date`:
   - `none` or `pending` → `branch automerge waiting for checks`. A repository without checks never merges; the dashboard lists the branch.
   - `failure` → the PR is created (within the new-PR limit, else `skipped: new PR limit reached`), its body starting with why, and Run requests platform auto-merge for it → `created, automerge`.
   - `success` → dry run: `would-automerge`; another branch already merged in this run: `branch automerge next run`; otherwise `push --force-with-lease=refs/heads/<base>:<head> origin <tip>:refs/heads/<base>`, a fast-forward because the tip's parent is head.
3. **Push result:** success → the branch is deleted (a failed delete only warns; cleanup removes it next run) → `merged`, and no further branch is merged in this run, since base moved. `ErrStale` (base moved since the run started) → `branch automerge next run`, no error. `ErrRejected` (branch protection, a pre-receive hook) → the PR is raised as for failed checks. Other errors are recorded.

`gitx.Push` classifies a refused push from its `--porcelain` status line: `[rejected]` (`stale info`, non-fast-forward) is `ErrStale`, `[remote rejected]` is `ErrRejected`. With platform commits the fast-forward is `updateRefs` with `beforeOid` = head; `platform.ErrRefStale` (the branch moved) and `platform.ErrRefRejected` (any other refusal, e.g. branch protection) map to the same errors. `WriteTree` stays local there: git object ids are content hashes, so the local tree equals the tree of the API commit made from the same changes on the same parent.

### Lock file maintenance

`lockFileMaintenance: {enabled, schedule}` (Renovate's option) adds one update per lock root in `Resolve`: `Type` `lockFileMaintenance` (not rule-selectable), `Dep.File` the manifest next to the lockfile, `Dep.Name` the lockfile path, `Schedule` from `lockFileMaintenance.schedule` (else the top-level one), the top-level automerge settings; rules do not apply, and none are added with `Options.Filter` (`explain`). Managers opt in with `manager.LockMaintainer` (`LockRoots`), post-processors with `engine.LockMaintainer` (`Maintain`, called instead of `Process`; other updates of the same scope are an error). cargo (`cargo update` in the temp mirror) and terraform (`terraform init -upgrade -backend=false`, `tofu` for OpenTofu, in the temp mirror; lock roots are the directories of configuration files with a `.terraform.lock.hcl`) implement both; npm or gomod only need the two methods.

- All maintenance updates form one group on `<prefix>lock-file-maintenance`, titled `chore(deps): lock file maintenance`; `engine.Local` leaves them out, so `plan`/`apply` list them as `separate PR` and never write them.
- The PR is rebuilt from the base each run in schedule (fingerprint decides `up-to-date`/`updated`). A group whose build yields no change (current locks) gets no action: nothing is pushed and its open PR is closed as obsolete. `minimumReleaseAge` does not apply.
- PR body: one `| `<lockfile>` | | | lock file maintenance |` row per lockfile plus a sentence that the manifests are unchanged; commit message lines `- <lockfile>: lock file maintenance`.

### Dependency dashboard

- **Finding it.** The first open issue (lowest number) among up to 10 pages of open issues whose title equals `dependencyDashboardTitle` and whose body contains `<!-- deps:dashboard -->`. GitHub lists PRs as issues; they are dropped. deps never closes it; with `dependencyDashboard: false` it is neither read nor written.
- **Checkboxes.** `- [x] <!-- deps:<action>=<branch> -->` lines (`x` or `X`) are requests for the next run: `create` (awaiting schedule, rate-limited), `recreate` (closed without merging), `rebase` (open PRs, and branches modified by someone else, whose commits are then discarded), `pr` (pending branch automerge: open the PR instead). Every body deps writes has all boxes unticked.
- **Body.** Sections in this order, each left out when empty: `Awaiting schedule` (each item names the crons it waits for: the intersection of its updates' schedules, ``(waits for `0 0 * * 0`)``), `Rate-limited`, `Ignored or blocked`, `Pending branch automerge` (branches without a PR waiting for their checks or the next run), `Open` (PR links), `Errors` (at most 20, flattened to one line of at most 300 characters), `Detected dependencies` (`<details>` per manager and per file, one `` `name version` `` line per extracted dep, sorted by manager, file, line and name). Items follow the group order, so the body is deterministic and an unchanged run does not edit the issue (line endings and surrounding whitespace are ignored in the comparison). The body is capped at 60 000 bytes (GitHub allows 65 536 characters); lists that do not fit end with a `not listed` note, and open `<details>` are always closed.

### Release age and notes

- **When.** Only for PRs that are created or updated; never for up-to-date, skipped or dry-run groups. Independently, `minimumReleaseAge` asks the same `Timer` during resolve, once per chosen candidate (section 4); the datasources dedupe these lookups, so a PR body reuses them.
- **Age.** From the optional `datasource.Timer`: `@v/<v>.info` (or `go list -m -json` for private modules), the npm packument's `time` (stream-decoded keeping only `time` and `repository`; skipped by `minimumReleaseAge` when the abbreviated `modified` is old enough, section 4), the crate index's `pubtime`, PyPI upload times, GitHub release publication times (the shared release list, section 3), git tags on github.com or Gitea/Forgejo via their releases API (a tag without its own release: the release of the same commit, else the tag date; section 3, and for Gitea/Forgejo `GET /repos/{repo}/tags/{tag}`, whose `commit.created` is the tagger date of an annotated tag, else the commit date), the `tags/list` times of gcr.io and Artifact Registry (Registries, section 3), else the image config's `created` (else, and for non-image artifacts such as OCI charts, the manifest's `org.opencontainers.image.created`), a chart index's `created`, the `Last-Modified` of a Maven version's POM (one `HEAD`), Gradle's `buildTime` and Go's release history for toolchains. Custom datasources have no times.
- **Notes.** One page of the source repository's GitHub or Gitea/Forgejo releases per run; on GitHub one GraphQL query (`releases(first:100)` with `tagName url description isDraft isPrerelease`) when a token for the host is known, else the REST page. The repository is the action's, the module's forge repository (plus a small vanity table: golang.org/x, k8s.io, sigs.k8s.io, google.golang.org/{grpc,protobuf}, go.yaml.in/yaml), npm's `repository`, the crate version's `repository` (crates.io API), PyPI `project_urls`, the Maven POM's `<scm>`, or the OCI `org.opencontainers.image.source` label. Versions in (current, new] are listed newest first, each body capped at 2 KiB and the section at 40 KiB, plus a compare link when both tags exist.
- **Sanitising.** Quoted bodies are neutralised: no `@mention` pings, upstream `#123` links, `redirect.github.com`.
- **Cost.** Lookups are lazy, deduped and bounded by the default request pool; failures only drop the notes. The fingerprint covers file contents only, so changing notes never churns PRs.

### Security updates (OSV)

- **Source.** `api.osv.dev`: `/v1/querybatch` requests of 100 distinct packages each, sent concurrently (round by round while results carry page tokens), then one `/v1/vulns/{id}` fetch per distinct advisory (deduped per run), started as soon as the batch naming it returns. OSV has its own request pool (2× `--concurrency`), so the fetches don't queue behind the release lookups. One batch of 687 packages took ~1.1 s; seven batches of 100 in parallel ~0.5 s. Runs once per run, concurrently with the release lookups. Disabled with `security: false`.
- **Ecosystems.**
  - `Go`: gomod deps of type `direct` or `indirect`, queried with module path and version; OSV matches server-side. Fixed versions get the module `v` prefix.
  - `npm`: npm deps, queried with package name and the locked version (`Dep.Locked`, else the bare version from package.json); OSV matches server-side. Fixed versions are bare semver. GHSA IDs are preferred over aliases.
  - `crates.io`: cargo deps of crates.io (no registry), queried with crate name and the version Cargo.lock locks (`Dep.Locked`), else the requirement's version; without a lock a partial requirement (`0.4`) is queried as its lowest version (`0.4.0`) and its fixed versions are cut to its precision, so only fixes outside its range count. RUSTSEC IDs are preferred over aliases.
  - `PyPI`: deps with datasource `pypi`, queried with the PEP 503-normalized name and the locked version (`Dep.Locked`, else the range's anchor version).
  - `Maven`: maven-datasource deps named `group:artifact` (maven and gradle managers, custom entries; Gradle plugin ids are not queried), with the locked version (`Dep.Locked`, e.g. from `gradle.lockfile`) else the version; OSV matches server-side. Fixed versions as published.
  - `GitHub Actions`: github-actions deps hosted on github.com, package `owner/repo` (subpath dropped). OSV does not evaluate ranges for this ecosystem, so the package is queried without a version and the ranges are evaluated locally. A floating tag with fewer than three parts (`v4`, `v4.1`) is affected only when every release of its line is. Fixed versions are rendered in the dep's style (prefix, part count).
- **Dedupe.** Withdrawn advisories are skipped. Aliases (`GO-2024-2687` / `GHSA-4v7x-pqxf-cx7m`) are reported once, preferring the ecosystem's native ID.
- **Decision.** The minimum fix F is the max over vulns of each vuln's lowest fix compatible with the current version (the locked version of an npm dep when known; vulns without one are ignored). If there is no normal update, or it is below F, the smallest compatible release ≥ F is chosen, bypassing `allow`, `updates` and `schedule`; `ignore` still wins. Under `update-lockfile` a fix the range admits is a lockfile-only update. No such release: the normal update (or none) stays, without error.
- **Failure.** A failed lookup adds `security advisories: <err>` to the errors and resolution continues without advisories (so obsolete PRs are not closed that run).

## 6. Running in CI

### Composite action (`action.yml`)

The action downloads a released static binary instead of building from source, so runners need no Go to run it:

1. **Resolve** (its own step, output `version`): map `uname -s`/`uname -m` to `linux/amd64` or `linux/arm64`; anything else fails with `deps: unsupported runner <os>/<arch>; only linux/amd64 and linux/arm64 are released`. Version: the `version` input, else `github.action_ref` when it is a release tag (`vX.Y.Z…`), else the latest release of `repository` (Gitea/Forgejo: `<host>/api/v1/repos/<repo>/releases/latest`; `https://github.com/...`: `api.github.com/repos/<repo>/releases/latest`). The binary dir is `$RUNNER_TOOL_CACHE/deps/<version>/<arch>` (falls back to `$RUNNER_TEMP` if unset or not writable).
2. **Preinstalled** (unless `use-preinstalled: false`): if `deps` on PATH prints the resolved version for `deps version`, run it and skip steps 3–5; output `cache-hit=preinstalled`. It is trusted without a checksum check (verifying it would need the checksums.txt download this step exists to avoid); the image that ships it is the trust anchor.
3. **Tool cache:** if the dir holds `deps` and `deps.sha256` and `sha256sum -c deps.sha256` passes, use it. Self-hosted runners keep the tool cache between jobs.
4. **Actions cache:** on a tool-cache miss, `actions/cache/restore` with key `deps-<version>-linux-<arch>` into the dir. Explicit restore/save steps instead of `actions/cache` with a post step, so nothing depends on post steps of actions nested in a composite action.
5. **Install:** a restored binary is used only if it passes the same checksum check; otherwise (or on a miss) download `deps_linux_<arch>` and `checksums.txt` from `<repository>/releases/download/<version>/` (`gh release download` on github.com runners), verify with `sha256sum -c`, and move the binary plus its recorded checksum line (`deps.sha256`) into the dir atomically. Print `deps version`. Output `cache-hit`.
6. **Save:** after a download, `actions/cache/save` stores the dir under the key.
7. Run `"<dir>/deps" <args>` (or the preinstalled binary) with `DEPS_TOKEN` from the `token` input.

| Input | Default |
|---|---|
| `token` | `${{ github.token }}` (the job token on GitHub, Gitea and Forgejo) |
| `args` | `run` |
| `version` | action ref if it is a release tag, else latest release |
| `repository` | the GitHub mirror on github.com runners, the Gitea repository elsewhere (any Gitea/Forgejo or GitHub repository URL) |
| `cache` | `true`; `false` skips both cache layers and downloads every run |

The action uses only `github.*` expressions, so it runs unchanged on GitHub Actions and Gitea/Forgejo with default settings. Its generated reference is [reference/action.md](reference/action.md).

Releases: a `v*` tag runs the `release` job of `.gitea/workflows/ci.yml` after `test` (`task ci` runs once). `task release:build` builds `deps_linux_amd64` and `deps_linux_arm64` with `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -buildid= -X main.version=<tag>"`, plus `checksums.txt`; the job checks the binaries are static. `task release:packages` packages them as reproducible `.deb`, Arch Linux `.pkg.tar.zst` and Alpine `.apk` files (both architectures each), which the job uploads to the owner's Gitea Debian (`stable`/`main`), Arch (`jakobmoellerdev`) and Alpine (`stable`/`main`) registries. `gt release create` publishes the binaries, packages and checksums. Attestations: the GitHub mirror's release workflow signs SLSA provenance for every file and an SPDX SBOM per binary keyless (`actions/attest`); the Gitea job verifies those bundles with cosign against its byte-identical files and attaches them, since Gitea 28 has no Actions OIDC. Details: [ci.md](ci.md#releases-maintainers).

### Gitea / Forgejo workflow

```yaml
# .gitea/workflows/deps.yml (or .forgejo/workflows/deps.yml)
on:
  schedule: [{cron: "0 5 * * *"}]
  workflow_dispatch:
permissions: {contents: write, pull-requests: write}
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
      - uses: https://git.jakob-moeller.cloud/jakobmoellerdev/deps@v0.1.0   # or ./ inside this repo
```

If the job token cannot push or open PRs, or cannot read private action repos, store a PAT as a secret. Pass it to both `actions/checkout` (`token:`) and the action (`token:`).

Lockfile regeneration needs the package manager: the binary on `PATH`, else `npx --yes <tool>[@<packageManager version>]`, so `npm`/`npx` on the runner (e.g. `actions/setup-node`) suffices; Python locks need `uv` (poetry and pipenv fall back to `uvx`). Without them, those manifests fail with `<tool> not found (needed to update <lock>)` and are not updated. `Cargo.lock` needs `cargo` on `PATH` (e.g. `dtolnay/rust-toolchain`). Workflows, tokens and runner tools in detail: [ci.md](ci.md).

### GitHub workflow

GitHub workflows cannot `uses:` an action hosted on Gitea. Either use the public GitHub mirror, `uses: jakobmoellerdev/deps@v0.3.0` (on github.com runners the defaults download from the mirror's releases), or download the binary directly (no Go needed):

```yaml
on:
  schedule: [{cron: "0 5 * * *"}]
  workflow_dispatch:
permissions: {contents: write, pull-requests: write}
jobs:
  deps:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7.0.1
        with:
          token: ${{ secrets.DEPS_TOKEN }}
      - run: |
          base=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/v0.1.0
          cd "$RUNNER_TEMP" && curl -fsSLO "$base/deps_linux_amd64" -O "$base/checksums.txt"
          grep ' deps_linux_amd64$' checksums.txt | sha256sum -c - && install -m 0755 deps_linux_amd64 deps
      - run: '"$RUNNER_TEMP/deps" run'
        env:
          DEPS_TOKEN: ${{ secrets.DEPS_TOKEN }}
```

**Caveat:** PRs created with `GITHUB_TOKEN` do not trigger workflows (`pull_request`, `push`), so CI never runs on them. Use a PAT or a GitHub App token. Pass it via the `token` input (or `DEPS_TOKEN`), and also to `actions/checkout` `token:` so the pushed branches trigger CI.

### Output and logging

Results and diagnostics are separate streams of stderr content:

- **Results** are always printed: `update`, `error` and `pr` lines and the final count line; `skip` lines with `-v`. `plan` writes the diff to stdout. `explain` writes its report to stdout.
- **Diagnostics** go through `log/slog` to stderr, filtered by `--log-level` (`debug|info|warn|error`, env `DEPS_LOG_LEVEL`, default `warn`) and formatted by `--log-format` (`text|json`, env `DEPS_LOG_FORMAT`, default `text`). An invalid value exits 2.
  - `info`: the run trigger (`trigger event=<GITHUB_EVENT_NAME> cron=<cron>`, `run` only), detected platform and how (`ci environment`, `remote host`, `api probe`, `--platform`), the actions default host and its source, per-phase counts and durations (discover, extract, resolve), and the bot's view of the repo (base, head, deps branches, open/closed PRs).
  - `debug`: every HTTP request (method, credential-free URL, status, protocol, time to headers), every `git`, `go`, `npm` and `cargo` and Python lock tool process (args, duration, error), credential-helper outcome per host (never the secret), GOPROXY resolution per module, per-file extraction, per-dependency verdict, patch and post-processor steps, `.npmrc` loads, npm metadata fetches (registry, versions, deprecated), crate index fetches, and the bot's per-PR decision inputs (fingerprints, mergeability, branch author).
  - `warn`: obsolete PRs left open because the run had errors; a schedule event whose cron cannot be read (every schedule then matches).

When debug is disabled, the HTTP transport skips timing entirely after one `Enabled` check; other debug calls are cheap level-filtered no-ops.

## 7. CLI reference

The complete, generated reference (every command, flag, default, environment variable, status and exit code) is [reference/cli.md](reference/cli.md); the help texts it is generated from are what `deps <command> --help` prints. In short:

| Command | Behavior |
|---|---|
| `deps plan` | Unified diff on stdout, report on stderr. Post-processors do not run: go.sum, package-lock.json, Cargo.lock and .terraform.lock.hcl changes are not computed (`note    go.sum changes are computed by apply/run`, `note    package-lock.json changes are computed by apply/run`, `note    Cargo.lock changes are computed by apply/run`, `note    .terraform.lock.hcl changes are computed by apply/run`). A dep with updates in several PR buckets contributes one of them to the diff (`engine.Local`: the newest non-major update, else the lowest major); every update gets an `update` line, the others with `(<type>, separate PR)`. The summary line counts all of them. |
| `deps apply` | Writes the changes, including `go mod tidy` output, regenerated package-lock.json files, Cargo.lock and .terraform.lock.hcl; the same one update per dep as `plan`. |
| `deps run` | Branch and PR per group (section 5). Flags: `--platform`, `--api-url`, `--repo`, `--token`, `--base`, `--author-name`, `--author-email`, `--max-new-prs` (default 10, `0` = unlimited), `--dry-run`. The commit author resolves per field: explicitly set flag > `$DEPS_GIT_AUTHOR` > `gitAuthor` > `deps[bot] <deps-bot@noreply.invalid>`. |
| `deps explain <name-glob>...` | Resolves only deps whose name matches any doublestar glob (as rule `names`). Prints per occurrence `<name>  <file>:<line>  (<manager>, <depType>, <datasource>, <versioning>)` (the versioning after a rule's override), then `current`, `rules` (`#<i> <fields>` or `none`; for updates, the `updateTypes` rules that applied to their types follow as `#<i> <fields> (applies to <type>)`), `policy` (`pin= allow= updates= group= schedule= minAge=`, plus `versioning=` when a rule set it; the pre-policy, then `<type>: <policy>` per type whose `updateTypes` rules applied), `candidates`, `rejected` (versions by reason, up to 3 each, e.g. `ignored by rule #4 for major`, `outside allow /^\d+\.\d+$/`, `too new (released 2026-11-01, minimum 28 days)`), one `result` line per PR bucket (`update <from> -> <to> (<type>)`, with `, bucket <b>` for a non-empty bucket), or `up to date`, `skipped: <reason>` or `error: <err>`, and one `security` line per advisory after its update. A glob matching nothing is an error. |
| `deps schema` | Prints the JSON Schema (draft 2020-12) of `.deps.yaml`, reflected from `internal/config` by `internal/jsonschema`. Unknown keys are rejected, as in `Load`. |
| `deps version` | Prints the version set via `-ldflags "-X main.version=…"`. |

`plan`/`apply` update lines carry advisories as a suffix: `update  <file>:<line>  <name>  <from> -> <to>  (<type>)  security: <id>,<id>`; updates left to their own PR read `(<type>, separate PR)`.

Editor support: the repository root has the generated `deps.schema.json` (`task docs` regenerates it from `deps schema`); start `.deps.yaml` with `# yaml-language-server: $schema=./deps.schema.json` pointing at a copy.

## 8. Roadmap

Open items from [renovate-parity.md](renovate-parity.md) and the performance plan:

- PR body badges: Mend adoption/passing/confidence (L5; external badge URLs or drop) and the OpenSSF scorecard (L6);
- the gaps from the OCM comparison ([what is still missing for OCM](migrating-from-renovate.md#what-is-still-missing-for-ocm)):

  - updating the action's `version:` input along with a pinned ref (O6);
  - the OCM cutover (O7);
- Python: HTML-only indexes, `PIP_EXTRA_INDEX_URL`, `requires-python` filtering (M21–M26);
- `actions.lock` via `github.com/github/actions-lockfile/go`;
- a benchmark gate: `BenchmarkResolve` in CI with a regression threshold (the benchmark exists; CI does not run it yet);
- Go proxy request volume (next performance phase): on open-component-model about 370 `@v/list` and 250 `@v/<v>.info` requests are the critical path (release times are already served from GOMODCACHE where cached). There is no batch endpoint for lists, so the remaining option is an opt-in cross-run Go proxy cache.

## 9. Configuration

The user-facing reference (every key, its default and validation, rule semantics, versioning schemes, custom managers and datasources) is [reference/configuration.md](reference/configuration.md), generated from the JSON Schema that `deps schema` prints; managers and datasources are in [reference/managers.md](reference/managers.md). The design decisions behind it:

- **Strict decoding.** `yaml.Decoder.KnownFields(true)`: a typo is an error with its line, never a silently ignored key. All validation errors are joined (`<path>: <field>: <msg>`), so one run reports every problem. Config errors exit 2.
- **Ordered rules with field-wise override.** Rules are evaluated top to bottom; each matching rule overwrites only the fields it sets (`updates` is replaced, not merged). This is Renovate's packageRules model without presets, so the effective policy of a dep is always the file read top to bottom (`deps explain` prints it).
- **Pre-policy vs per-type policy.** Decisions needed before an update exists (`ignore` of the dep, `pin`, `rangeStrategy`, `versioning`, whether to look it up) come from rules without `updateTypes`; everything else is evaluated per candidate type (section 4), which is why `pin`, `rangeStrategy` and `versioning` are rejected on `updateTypes` rules.
- **Renovate-compatible defaults where they matter.** Default `ignorePaths`, default-ignored dep types (indirect, `go`, peerDependencies, engines), semantic commit prefixes and built-in monorepo groups replace the presets OCM relies on; the PR limits default to unlimited (`prConcurrentLimit`/`prHourlyLimit` 0), capped per run by `--max-new-prs`.
- **Validated at load.** Constraints, `allow` regexes, schedules, ages and regex versioning schemes are compiled in `Validate`; custom `match` expressions and datasource selectors are checked there too, so a bad config never starts a lookup.
- **Branch automerge only on green, fast-forward only.** `automergeType: branch` pushes to the base branch, but only a tip whose parent is the base head, with all checks green, leased against the head the run started from; anything else opens a PR or waits. There is no `ignoreTests`.

## 10. Why Go

- **Ecosystem.**
  - `golang.org/x/mod` gives exact Go module semantics: modfile parsing, semver, pseudo-versions and path escaping.
  - oras-go covers Docker registries and image references (`internal/imageref` adds the Docker Hub defaults).
  - The Gitea SDK lineage exists, and `actions-lockfile` (roadmap) is a Go module.
  - Forges are mostly written in Go, so their behavior is easy to reference.
- **I/O-bound work.** The job is mostly HTTP and git. Goroutines, one shared HTTP/2 client and a semaphore cover it without a runtime, an interpreter or node_modules. The result is one static binary that starts instantly and has a small memory footprint.
