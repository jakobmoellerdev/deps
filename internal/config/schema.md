<!--
Long descriptions of the .deps.yaml keys. `deps schema` appends each section to the key's
description (markdownDescription in deps.schema.json, shown by editors) and `task docs` renders
them into docs/reference/configuration.md. "## <Definition>.<key>" belongs to that key of the
schema definition (Config is the top level, see `deps schema`); "## Config" describes the file.
Sections must not contain "## " headings; links are relative to docs/reference/.
-->

## Config

`deps` reads `.deps.yaml` (then `.deps.yml`) from `--dir`, or the file given with `--config`, which must exist. Without a file the defaults apply; an empty file also yields the defaults.

The JSON Schema printed by `deps schema` is the authoritative key list; this page is generated from it, and the repository root has a copy as `deps.schema.json`. For editor completion and hover documentation (yaml-language-server, e.g. the VS Code YAML extension), start the file with:

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
```

Replace `vX.Y.Z` with your deps version (`deps version`, or the action's `version:`): every release after v0.3.0 carries its schema as the `deps.schema.json` asset (listed in `checksums.txt`), whose `$id` is that URL, so the editor checks exactly the keys your deps accepts; bump the modeline together with deps. To follow the development version instead, use `https://git.jakob-moeller.cloud/api/v1/repos/jakobmoellerdev/deps/raw/deps.schema.json?ref=main`, or point `$schema` at a local copy (`deps schema > deps.schema.json`, then `# yaml-language-server: $schema=./deps.schema.json`).

**Loading and validation.**

- Decoding is strict. Every problem is reported as `<path>:<line>:<col>: <field>: <message>`, one per line, sorted by position, all in one run: syntax errors, unknown keys, values of the wrong type and failed validations.
- An unknown key names a close match, for a Renovate option its deps equivalent, or for a key of an earlier deps version where it moved: `.deps.yaml:1:1: automerg: unknown key (did you mean automerge?)`, `.deps.yaml:3:5: rules[0].matchPackageNames: unknown key (Renovate's matchPackageNames is names here)`, `.deps.yaml:2:1: dependencyDashboard: unknown key (moved to dashboard.enabled)`.
- A value of the wrong type names what the key takes: `.deps.yaml:1:8: rules: want a list, got a mapping`.
- An unknown value of a fixed set (`depTypes`, `datasources`, `updateTypes`, `pin`, `rangeStrategy`, `commitType`, ...) suggests a close match (`unknown value "devDependency" (did you mean devDependencies?)`); without one, sets of up to 10 values are listed, longer ones point at `deps config describe <key>`.
- Under GitHub Actions, each problem is also printed as an `::error file=<path>,line=<line>,col=<col>,title=deps::<field>: <message>` workflow command, so it shows as an annotation on the config file (Gitea and Forgejo do not render annotations, so deps prints none there).
- A config error exits with code 2 (see [exit codes](cli.md#exit-codes)).
- `deps config check` loads and validates the file offline (exit 2 on errors), e.g. in a pre-commit hook; `deps config show --sources` prints the effective values with their source, and `deps config describe <key>` prints a key's section of this page in the terminal. `deps init` writes a starter file.

**Example** using most keys:

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
version: 1
schedule: ["0 5 * * 1-5"]          # create PRs in the weekday run (a cron of the workflow)
pin: digest                         # pin actions to SHAs and images to digests
minimumReleaseAge: 3 days
branchPrefix: deps/
rebaseWhen: behind-base-branch
git:
  author: 'deps[bot] <deps-bot@noreply.invalid>'
prConcurrentLimit: 10
prHourlyLimit: 2
dashboard:
  enabled: true
  labels: [dependencies]
separate: majors                    # one PR per newer major
ignorePaths: ['**/node_modules/**', '**/testdata/**', 'third_party/**']
rules:
  - managers: [github-actions]      # minor+patch of all actions in one PR
    updateTypes: [minor, patch]
    group: actions
  - updateTypes: [minor, patch]     # automerge minor and patch updates once checks pass
    automerge: true
    automergeStrategy: squash
  - datasources: [git-tags, docker] # digest refreshes in one weekly PR
    updateTypes: [digest, pin]
    group: digest-pins
    schedule: ['0 5 * * 0']         # the workflow's Sunday cron
  - managers: [gomod]               # include indirect requirements...
    depTypes: [indirect]
    ignore: false
  - managers: [gomod]               # ...without their majors
    depTypes: [indirect]
    updateTypes: [major]
    ignore: true
  - names: ['golang.org/x/**']
    group: golang-x
  - managers: [docker]
    names: ['postgres']
    allow: '< 18'
  - managers: [npm]
    depTypes: [devDependencies]
    group: npm-dev
    commitScope: dev-deps
  - datasources: [npm]
    minimumReleaseAge: 7 days
custom:
  - files: ['Makefile', '.env']     # '# deps:' / '# renovate:' markers
  - files: ['Dockerfile']
    match: 'ARG GH_VERSION=(?P<version>\S+)\nARG GH_SHA256=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: cli/cli
    checksum: {asset: 'gh_{{version}}_linux_amd64.tar.gz'}
customDatasources:
  envtest:
    url: https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/master/envtest-releases.yaml
    format: yaml
    versions: .releases | keys
```

## Config.version

Only `1` exists; `version: 2` fails with `version: must be 1, got 2`.

## Config.pin

`rules[].pin` overrides it per dep.

- `keep`: keep each reference's current style. Refs that are already pinned (`@<sha> # v4.2.0`, `image:tag@sha256:…`) stay pinned, and both the digest and the version are refreshed.
- `digest`:
  - github-actions: tag refs are pinned to the tag's commit SHA, with ` # <tag>` appended.
  - docker: `@sha256:<digest>` is appended after the tag (the index digest for multi-platform images).

gomod, npm and custom deps ignore `pin`.

```yaml
pin: digest
```

## Config.rangeStrategy

It applies to the ranges of npm `dependencies`, `devDependencies`, `optionalDependencies` and `peerDependencies` (`^1.2.3`, `~1.2.3`, `>=1.2.3`, `=1.2.3` and exact `1.2.3`) and to the npm `engines` and `volta` ranges other than an operator and one version (`>= 22 <27`, `^24 || ^26`, `22.x`; see the [npm manager](managers.md#npm)), to the npm ranges of Deno `jsr:` and `npm:` specifiers (`jsr:@std/path@^1.0.0`, `npm:chalk@5`; locked in `deno.lock`, see the [deno manager](managers.md#deno)), to every cargo requirement (`1.2`, `~2.7.0`, `">= 0.10, < 0.11"`, `1.*`, `*`), to every Python specifier of the pip_requirements, pep621, poetry, pipenv and homeassistant-manifest managers and of conda `pip:` lists (see Python ranges below), to conda version specs (`=1.26`, `>=1.26,<2`; see the [conda manager](managers.md#conda)), to bundler gem requirements (`"2.241.2"`, `"~> 7.1"`, `"~> 7.1", ">= 7.1.2"`, none), to Elm package constraints (`1.0.0 <= v < 2.0.0`, below), to composer.json constraints (`^1.2`, `~1.2.3`, `>=1.0 <2.0`, `1.2.*`, `^1.0|^2.0`; below), to Julia `[compat]` entries (`"0.21, 1"`, `"~1.2"`, `"=0.3.1"`, `">= 1.2"`, `"1.2 - 2"`), to NuGet floating versions and intervals (`13.*`, `[1.0,2.0)`, below), to Paket constraints (`~> 1.2`, `>= 1.2 < 2`; see the [paket manager](managers.md#paket)), to Maven version ranges (`[1.0,2.0)`, `(,1.0]`; admitted versions are kept, newer ones extend the range as Renovate does, `pin` writes the version; see the [maven manager](managers.md#maven)), to pub constraints of pubspec.yaml (`^1.2.3`, `>=1.2.3 <2.0.0`, `1.2.3`, the SDK constraints too; see the [pub manager](managers.md#pub)), to Elixir requirements of mix and gleam (`~> 1.7`, `>= 1.0.0 and < 2.0.0`, `~> 1.0 or ~> 2.0`, `== 1.2.3`; below), and to terraform version constraints (below). Other deps always replace their exact version. `rules[].rangeStrategy` overrides it per dep (not together with `updateTypes`).

- `update-lockfile`: the current version is the one the lock root's lockfile (`package-lock.json`, `npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock` or `bun.lock`) resolves the dep to, and update types compare it with the new version.
  - A newer version the range admits is a **lockfile-only update**: package.json is unchanged and the lockfile locks exactly that version. `apply` and `run` run, on a copy, `npm install --package-lock-only <name>@<version>` (with `--workspace=<dir>` for a workspace member; deps restores the range npm writes into the lockfile's own package entries), `pnpm update --no-save --lockfile-only --filter <dir> <name>@<version>`, `yarn set resolution <name>@npm:<range> npm:<version>` (berry), or install from a copy of package.json pinned to the version and put the range back into the lockfile (yarn classic, bun). `plan` lists the update but shows no diff for it.
  - A version outside the range replaces the range's version and keeps its operator (`^5.2.8` → `^6.0.0`).
  - Without a lockfile, versions the range admits are not proposed: an install already gets them.
- `bump`: every newer version raises the range (`^1.2.3` → `^1.4.0`), and the lockfile is regenerated.
- `replace`: versions the range admits are not proposed; a version outside it replaces the range's version (`^5.2.8` → `^6.0.0`). The lockfile is not touched for admitted versions.
- `auto` (default): Renovate's default for the manager: `update-lockfile` for npm, deno, cargo, Python, bundler Gemfiles, composer (`widen` for composer constraints with alternatives), mix (`widen` for requirements with several comparators) and Julia projects with a manifest, `replace` for terraform, terragrunt, gemspec dependencies and pub (pubspec.lock is updated, but Renovate proposes no lockfile-only pub updates), `widen` for Elm package constraints (Dependabot's; Renovate has no Elm manager), gleam requirements and Julia projects without a manifest (see Julia below).

peerDependencies never get lockfile-only updates: under `update-lockfile` and `auto`, versions their range admits are left alone. Renovate widens peer ranges (`^1.0.0 || ^2.0.0`); deps replaces the range when a version falls outside it.

Locked versions are read from every lockfile deps updates; see the [npm manager](managers.md#npm) for the details. When the lockfile does not parse (or a `package-lock.json` has no `lockfileVersion`), `update-lockfile` and `auto` act as `bump`. A v1 `package-lock.json` is read, and npm rewrites it as version 3 on the first update.

Security updates check the locked version against OSV when it is known, so a fix that is already locked is not proposed. When the locked version is vulnerable, the fix is a lockfile-only update if the range admits it. Without a lock, the version in package.json is checked.

For cargo the lock is `Cargo.lock` of the workspace root: a lockfile-only update runs `cargo update --package <name>@<locked> --precise <version>`, a rewritten requirement keeps its operators and precision (`1.2` → `2.0` for 2.0.1, which then goes into the lock). A requirement without a floor (`*`, `<2`) has no version to raise, so under `bump` too versions it admits are lockfile-only updates. See the [cargo manager](managers.md#cargo).

**Helm** chart version ranges (Masterminds constraints: `~1.2.0`, `^1`, `1.2.x`, `1.2`, `>=1.0.0 <2`, `>= 1.2, < 2`, `^1 || ^2`) of the helm manager (Chart.yaml, requirements.yaml, helmfiles, k0s, Flux HelmRelease and OCIRepository `spec.ref.semver`, Argo CD, Sveltos, Fleet, Helmsman, vendir), kustomize `helmCharts` and terraform `helm_release` are rewritten as Renovate's helm versioning does (its npm versioning), keeping operators, precision, wildcards and commas. `auto` is `replace` (Renovate's default for Helm): versions the range admits are not proposed, a newer one replaces the range (`~1.2.0` → `~1.3.0`); the Chart.lock entry takes the new version. deps reads no locked chart versions, so `update-lockfile` acts as `replace`. See the [helm manager](managers.md#helm).

**Ruby** gem requirements of the bundler manager (Gemfile, `gems.rb`, `*.gemspec`; one string or a list: `"2.241.2"`, `"~> 7.1"`, `">= 1.2", "< 2"`, `"~> 7.1", ">= 7.1.2"`, also as an array) and the Gemfile `ruby` directive follow the same strategies; only the strings that change are rewritten, and in them only the version when the operator stays. The lock is the `Gemfile.lock` next to the Gemfile (a gemspec uses its Gemfile's): lockfile-only updates and rewritten requirements are locked to exactly their version with `bundle lock --update <gems> --conservative` (the requirement pinned to `= <version>` for that run). See the [bundler manager](managers.md#bundler).

- `auto`: `update-lockfile` for Gemfiles, as in Renovate; `replace` for gemspec dependencies (DepType `runtime`, `development`), which state what a gem supports rather than what it locks (Renovate has no gemspec manager).
- `update-lockfile`: a version the requirements admit is a lockfile-only update; one outside them replaces them. Without a Gemfile.lock (and for the `ruby` directive, which no lock holds) admitted versions are not proposed.
- `replace`: admitted versions are left alone; others replace the requirements: an exact version takes the new one (`2.241.2` → `2.242.0`), `~>`, `>=` and `>` take it at their own precision (`~> 7.1` → `~> 8.0` and `~> 7.1.2` → `~> 8.0.1` for 8.0.1, where Renovate writes `~> 8.0.0`; `">= 1.2", "< 2"` → `">= 2.1", "< 3"` for 2.1.0; `>` becomes `>=`), an upper bound the version reaches moves past it at its precision (`< 2` → `< 3`, `< 1.3` → `< 1.5` for 1.4.2, `<= 2.6` → `<= 2.7.1`), `!=` stays.
- `bump`: every newer version raises the floors: `>=` and `>` become `>= <version>`, `~>` takes the version at its precision (`~> 7.1` → `~> 7.2` for 7.2.0) and, when that cannot hold it and no `>=` accompanies it, gets one (`"~> 7.1", ">= 7.2.3"` for 7.2.3, `"~> 7.1", ">= 7.1.5"` for 7.1.5, as Renovate); upper bounds and `!=` stay. Requirements without a floor (`< 2`) are handled as under `update-lockfile`.
- `in-range-only`: `bump` limited to the versions the requirements admit.
- `widen`: admitted versions are left alone; for others only the upper bounds move (`">= 1.2", "< 2"` → `">= 1.2", "< 3"` for 2.1.0), and a `~>` the version outgrows becomes its two bounds (`"~> 7.1"` → `">= 7.1", "< 9"`, `"~> 7.1.2"` → `">= 7.1.2", "< 8.1"` for 8.0.1; next to a `>=` only the bound: `"< 9", ">= 7.1.2"`). Ruby requirements have no `||` alternatives to add. An exact version is replaced, as are requirements widening cannot make admit the version; a version an `!=` excludes is not proposed.
- `pin`: the requirements become `= <version>` (an exact one keeps its form: `2.241.2` → `2.242.0`); a list is joined into that one string (`">= 1.2", "< 2"` → `"= 2.1.0"`) unless something other than commas and blanks (a comment) separates its strings, in which case it is not pinned.
- A gem without requirements (`gem "rake"`, `>= 0`) is never rewritten: under `update-lockfile`, `bump` and `in-range-only` only its Gemfile.lock entry moves; `replace`, `widen` and `pin` propose nothing for it (no gemspec dependency without requirements is ever updated under `auto`).

**Elm** package constraints of the elm manager (`dependencies`, `test-dependencies` and `elm-version` of a package `elm.json`: `1.0.0 <= v < 2.0.0`) have no lockfile, so admitted versions are proposed only under `bump` and `in-range-only`; the lower bound is the current version. `auto` is `widen`, Dependabot's update of Elm packages (Renovate has no Elm manager): the lower bound stays and an upper bound the new version reaches moves past it (`1.0.0 <= v < 2.0.0` → `1.0.0 <= v < 3.0.0` for 2.1.0). `replace` and `update-lockfile` also set the lower bound to the new version (`2.1.0 <= v < 3.0.0`), `bump` does so for admitted versions too, and `pin` writes `2.1.0 <= v <= 2.1.0`. An exclusive upper bound moves to the next boundary of its precision (`< 1.5.0` → `< 1.8.0` for 1.7.2), an inclusive one to the new version. See the [elm manager](managers.md#elm).

**Composer** constraints of the composer manager follow the same strategies with the versions `composer.lock` locks: lockfile-only updates and rewritten constraints are locked to exactly their version with `composer update <name>:<version> --with-dependencies --minimal-changes`. `auto` is `update-lockfile`, except that a constraint with alternatives (`^1.0|^2.0`, `^1 || ^2`) is widened, as in Renovate, which also turns `bump` of one into `widen`. A version outside the constraint replaces it after Renovate's composer versioning: exact versions take it (`1.2.3` → `1.4.0`, a `v` prefix kept), `^`, `~`, `>=` and `>` take it at their precision (`^5.2.8` → `^6.0.1`, `~1.2` → `~1.4`; a two-part `^`/`~` moving to a new major becomes `<major>.0`: `^4.1` → `^5.0` for 5.2.0), wildcards keep theirs (`1.2.*` → `1.4.*`), upper bounds move past it (`>=1.0 <2.0` → `>=2.3 <3.0` for 2.3.0); of alternatives only the last one is kept. `widen` moves the upper bounds of the last alternative (`>=1.0 <2.0` → `>=1.0 <3.0`) or appends the replaced last alternative with the constraint's separator (`^1.2` → `^1.2 || ^2.0`, `^1.0|^2.0` → `^1.0|^2.0|^3.0`); `bump` raises the lower bounds of the alternative admitting the version (`^1.2` → `^1.4`), and leaves versions it cannot raise them for alone (`^1` with 1.5.0); `pin` writes the exact version. Constraints without a lower bound (`*`, `<2.0`) only get lockfile-only updates. See the [composer manager](managers.md#composer).

**Julia** `[compat]` entries of the julia manager (`Project.toml`, `JuliaProject.toml`) follow the same strategies; the entry is rewritten as a whole (Pkg's grammar: comma-separated caret, `~`, `=`, `>=`/`≥`, `<` and `a - b` terms, any of which admits a version). The lock is the `Manifest.toml` next to the project, or next to its workspace root: lockfile-only updates and rewritten entries move exactly that package with `Pkg.add(name=…, version=…)` run by `julia`. See the [julia manager](managers.md#julia).

- `auto`: `update-lockfile` when the project has a manifest that is read (an application); `widen` otherwise (a package, which commits no manifest: the Julia convention, as CompatHelper keeps older majors supported).
- `update-lockfile`, `replace`: as above; an entry is replaced by its top term (the one with the highest upper bound) for the version at that term's precision (`0.21, 1` → `2` for 2.3.4, `4.2` → `5.1`, `~1.2` → `~1.5`, `=0.3.1` → `=0.4.2`, `1.2 - 1.5` → `2.1 - 2.1`, `< 2` → `< 3`).
- `widen`: a term for the version is appended in the top term's style, a caret term as the version's caret prefix (`0.21, 1` → `0.21, 1, 2`, `0.21` → `0.21, 0.22` for 0.22.1), `~` at its precision, `=` exact; a top `<` or hyphen term raises its upper bound in place (`< 2` → `< 3`, `1.2 - 1.5` → `1.2 - 2.1`).
- `bump`, `in-range-only`: the terms entirely below the version are dropped and the floor of the term admitting it is raised at its precision (`0.21, 1` → `1` for 1.4.5, `1.2` → `1.4`; `1` cannot be raised for 1.4.5, so that is a lockfile-only update).
- `pin`: the entry becomes `=<version>`.
- A `[deps]` package without a compat entry (`>= 0`) is never rewritten: only its manifest entry moves.

**Elixir** requirements of the mix (`mix.exs`) and gleam (`gleam.toml`) managers follow the same strategies, after Renovate's hex versioning; see the [mix](managers.md#mix) and [gleam](managers.md#gleam) managers. The lock is `mix.lock` (an umbrella app's: the umbrella's) or `manifest.toml`: lockfile-only updates and rewritten requirements move exactly that package (`mix deps.update` / `gleam deps update` with the requirement pinned to `== <version>` in a temporary copy).

- `auto`: `update-lockfile` for mix, `widen` for mix requirements with several comparators (`>= 1.0.0 and < 2.0.0`, `~> 1.0 or ~> 2.0`) and for every gleam requirement, as in Renovate.
- `update-lockfile`, `replace`: a version outside the requirement replaces its last alternative (`~> 1.7` → `~> 2.1` for 2.1.0, `~> 1.7.2` → `~> 2.1.0`, `== 0.12.0` → `== 0.13.0`, `>= 1.0.0 and < 2.0.0` → `>= 2.1.0 and < 3.0.0`; upper bounds move past the version at their precision).
- `widen`: the upper bounds of the last alternative move past the version (`>= 0.44.0 and < 2.0.0` → `>= 0.44.0 and < 3.0.0`, `<= 0.38.0` → `<= 0.39.0`), else the replaced last alternative is appended (`~> 1.7` → `~> 1.7 or ~> 2.1`).
- `bump`, `in-range-only`: the lower bounds of the first alternative admitting the version are raised (`~> 1.7` → `~> 1.9` for 1.9.3, `>= 1.0.0 and < 2.0.0` → `>= 1.4.0 and < 2.0.0`); `bump` of a requirement with several comparators widens instead, as in Renovate.
- `pin`: the requirement becomes `== <version>`.

**NuGet** floating versions (`13.*`, `1.2.*`, `1.0.0-rc*`) and intervals (`[1.0,2.0)`, `(,2.0]`) of the nuget manager follow the strategies too; a bare version (`13.0.3`) and an exact range (`[13.0.3]`) always replace their version. deps reads no `packages.lock.json`, so under `update-lockfile` (and `auto`) versions a range admits are not proposed; one outside it moves a floating version at its precision (`13.*` → `14.*`) and an interval's upper bound past it (`[1.0,2.0)` → `[1.0,3.0)` for 2.5.1), keeping the lower bound. `bump` also raises an interval's lower bound to every newer version it admits (`[1.5.0,2.0)`); `widen` is `replace`; `pin` writes the bare version. See the [nuget manager](managers.md#nuget).

**GitHub Actions** setup inputs that are npm ranges (`go-version: 1.27.x`, `node-version: ">=24"`, `python-version: ^3.12`, setup-dotnet's `8.0.x` and channels `8.0`; see [Setup action inputs](managers.md#github-actions)) have no lockfile, so under `update-lockfile` (and `auto`) and `replace` versions a range admits are not proposed and one outside it rewrites the range at its precision (`1.27.x` → `1.28.x`, `8.0` → `10.0`), with npm's rules: `bump` raises an admitted range's floor (`>=24` → `>=26`), `widen` appends an alternative (`8.0.x || 10.0.x`), `pin` writes the version.

Python ranges (`>=1.2,<2`, `<1.0.0,>=0.114.2`, `~=1.4`, `^1.2`, `>=1.2 <2`, `^1 || ^2`, `<2`, with `!=` exclusions) follow the same strategies, with the versions `uv.lock`, `poetry.lock`, `pdm.lock` or `Pipfile.lock` lock, or a compiled requirements file pins for the requirements of its inputs (hand-written requirements files have none, so `update-lockfile` proposes only versions outside the range there):

- `update-lockfile` and `auto`: a version the range admits is a lockfile-only update (`uv lock --upgrade-package <name>==<version>`; for poetry.lock and pdm.lock `poetry lock`/`pdm lock --update-reuse` with the dep pinned to `==<version>`, then again with the manifest restored; `pipenv upgrade --lock-only <name>==<version>`); one outside it rewrites the range with Renovate's pep440 `replace` rules (`>=1.2,<2` → `>=3.1,<4` for 3.1.0; `!=` clauses are kept).
- `bump`: every newer version raises the lower bound (`>=1.2,<2` → `>=1.5.0,<2` for 1.5.0); a range without one to raise (`<2`, `>1`) is handled as under `update-lockfile`.

See the Ranges section of the Python managers for the exact rewrite rules.

**terraform** constraints (`required_providers`, `required_version`, registry `module` versions, `tfe_workspace`, Terragrunt `version`) follow the same strategies with Terraform's grammar (`~> 5.0`, `>= 1.2, < 2.0`); see the [terraform manager](managers.md#terraform):

- `auto` is `replace`, Renovate's terraform default: a version the constraint admits is left alone, otherwise the constraint's version is replaced (`~> 5.0` → `~> 6.2`).
- `update-lockfile`: an admitted version is a lockfile-only update of the provider's `.terraform.lock.hcl` entry (`terraform init` on a copy pins it); deps without a lock act as under `replace`.
- `bump`: every newer version raises the constraint (`~> 5.0` → `~> 5.46`, `>= 1.5.7` → `>= 1.9.5`).
- Constraints with an upper bound, an exclusion or several clauses (`>= 4.0, < 6.0`) are never rewritten; only their locked version moves (under `update-lockfile` and `bump`).

```yaml
rangeStrategy: bump
```

## Config.schedule

Each entry is either a **cron trigger** or a **time window**; a group is in schedule when any entry matches. How an entry is classified:

| Entry | Kind | In schedule when |
|---|---|---|
| a cron whose minute is fixed: `0 5 * * 1`, `30 4 * * *`, `*/15 6 * * *` | trigger | the run was started by that cron of the workflow |
| a cron whose minute is `*`: `* 0-5 * * 1` (Renovate's cron schedules) | window | the run time is in every listed hour, day of month, month and day of week |
| text, i.e. the entry starts with a letter: `before 6am on monday` | window | the run time is inside the window |

**Cron triggers** are matched against what started the run, never against the clock:

- The trigger is read from the CI environment: `GITHUB_EVENT_NAME` (GitHub, Gitea and Forgejo set it) and, for `schedule` events, the `schedule` field of the event payload at `GITHUB_EVENT_PATH`. GitHub and Gitea (verified in 28.3) put the triggering cron there. `--trigger-schedule <cron>` or `DEPS_TRIGGER_SCHEDULE` override the detection (for local tests or runners that lack the payload).
- Crons are compared as strings after collapsing whitespace: `0 0 * * 0` matches `0  0 * * 0` but not the equivalent `0 0 * * 7`. Copy them verbatim from `on.schedule`.
- `workflow_dispatch`, `repository_dispatch` and a local `deps run` act immediately: every trigger matches. Any other event (`push` after merging a deps PR, `issues` when the dashboard is ticked, `workflow_run` after CI, …) matches no trigger: only unscheduled groups and security updates get new PRs, and open PRs are refreshed per `updateNotScheduled`. A `schedule` event whose cron cannot be read matches every trigger, with a warning.
- Cron triggers of CI systems are UTC; [`timezone`](#timezone) does not apply to them.
- Fields: `minute hour day-of-month month day-of-week`; `*`, `n`, `a-b`, comma lists, `/step` on `*` or a range; day of week `0-7`; no names. Errors look like `schedule[0]: "60 5 * * *": minute: value 60 out of range 0-59`.

**Time windows** (Renovate's `schedule`) are evaluated against the time of the run, in [`timezone`](#timezone) (UTC by default), whatever started it, so `workflow_dispatch`, `push` and local runs outside the window do not create PRs either. `--schedule-time <RFC 3339 time>` or `DEPS_SCHEDULE_TIME` evaluate them at another time (for local tests). The workflow still needs crons that run inside the windows, e.g. hourly. Text windows are a subset of Renovate's [later](https://breejs.github.io/later/parsers.html#text) syntax, case-insensitive, with clauses in any order separated by spaces or `and`:

| Clause | Meaning |
|---|---|
| `before 6am`, `before 6:30am`, `before 18:00` | minutes of the day before that time |
| `after 10pm`, `after 22:00` | from that time to midnight |
| `after 10pm and before 5am` | both: a range, wrapping past midnight when the start is later |
| `every weekday`, `every weekend`, `every monday` | Monday to Friday, Saturday and Sunday, one day |
| `on monday`, `on friday and saturday`, `on mon, wed`, `on monday through friday` | days of the week (full names, `mon`…`sun`, plurals) |
| `on the first day of the week`, `on the 2nd day of the week`, `on the last day of the week` | day of the week, counted from Sunday as in later: Sunday, Monday, Saturday |
| `on the first day of the month`, `on the 15th day of the month`, `on the last day of the month` | day of the month |
| `every 2 weeks`, `every 3 weeks of the year` | ISO weeks of the year 1, 3, 5, … and 1, 4, 7, … (every n-th week from week 1, as later counts them) |
| `every 3 months` | months 1, 4, 7 and 10 (every n-th month from January) |
| `at 5am`, `at 5am and 6:30pm` | that minute of the day (Renovate: that second). The run must start in it; for runs at a fixed time a cron trigger such as `0 5 * * *` is more robust |
| `at any time` | always |

Windows joined by `also` are alternatives: `before 6am also after 10pm` is in schedule before 6am and after 10pm. `except` followed by windows (joined by `also`) excludes them: `after 9:00 except on saturday also on sunday`, `before 6am also after 10pm except on sunday`. An entry has at most one `except`, at the end. deps excludes every time an exception contains; Renovate's later evaluates exceptions as ranges and ignores some time-of-day exceptions (`on monday except before 6am` is in schedule on Monday at 5am there).

Examples: `before 6am on monday`, `after 10pm and before 5am every weekday`, `every weekend`, `before 4am on the first day of the month`, `every 3 months on the first day of the month`, `every 2 weeks on monday before 6am`. A time needs `am`/`pm` or minutes (`6am`, `18:00`; not `6`). Errors look like `schedule[0]: "befor 6am": unknown word "befor" (want before, after, at, every, on, also, except or a cron)`.

The schedule only gates creating new PRs and, with `updateNotScheduled: false`, updating existing ones. Security updates ignore schedules.

A daily workflow cron plus a weekly one, with the weekly cron reserved for some groups:

```yaml
# .github/workflows/deps.yml
on:
  schedule:
    - cron: "0 0 * * *"   # daily
    - cron: "0 0 * * 0"   # weekly, Sunday
```

```yaml
# yaml-language-server: $schema=https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/vX.Y.Z/deps.schema.json
rules:
  - updateTypes: [digest, pin]
    group: digest-pins
    schedule: ["0 0 * * 0"]   # PRs are created only by the Sunday run
```

Other deps have no schedule, so both crons create their PRs. The same with a time window, for a workflow that runs hourly:

```yaml
timezone: Europe/Berlin
rules:
  - updateTypes: [digest, pin]
    group: digest-pins
    schedule: ["before 6am on sunday"]   # PRs are created by runs on Sunday before 06:00 in Berlin
```

## Config.timezone

Renovate's `timezone`. An [IANA time zone](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) name such as `Europe/Berlin` or `America/New_York`; the time zone database is built into deps. It applies to the time windows of every schedule (top level, `rules[].schedule`, `lockFileMaintenance.schedule`), including daylight saving time; cron triggers are matched as text and are unaffected. `Local` is rejected, unknown names fail with `timezone: unknown IANA time zone "Europe/Berln"`. Summaries and `deps explain` print a schedule with windows outside UTC with its zone, e.g. `before 6am on monday (Europe/Berlin)`.

## Config.branchPrefix

Branches are `<prefix><additionalBranchPrefix><slug(name)>[-<bucket>]` for the updates of one dependency, across managers and files, `<prefix><additionalBranchPrefix>group/<slug(group)>[-<bucket>]` for [groups](#rulesgroup) and, for [security groups](#rulessecuritygroup), `<prefix><additionalBranchPrefix>group/<slug(securityGroup)>[-<bucket>]-security`; lock file maintenance uses `<prefix>lock-file-maintenance`. With several [base branches](#basebranches) the base follows the prefix (`<prefix><base>/…`). deps never touches branches and PRs outside the prefix.

- It must end with `/`.
- It must not contain spaces, control characters, any of `~^:?*[\`, `..` or `@{`.
- No component may be empty, start with `.` or end with `.lock`.

To take over Renovate's open PRs, set `branchPrefix: renovate/` (see [branch prefix takeover](../migrating-from-renovate.md#branch-prefix-takeover)).

## Config.additionalBranchPrefix

Renovate's `additionalBranchPrefix`: text between [`branchPrefix`](#branchprefix) and the branch topic. Renovate builds branch names as `{{branchPrefix}}{{additionalBranchPrefix}}{{branchTopic}}`; deps' topics are the dependency name (`slug(name)`) and, for groups, `group/<slug(group)>`, followed by the [bucket](#separate) suffix. Without it (the default `""`) the updates of one dependency name share one branch and PR across managers and files, as in Renovate: `anchore/syft` updated as a GitHub action, a Homebrew formula and a [custom](#custom) match gets one PR on `deps/anchore-syft`.

- **Templates.** Literal text plus Renovate's `{{manager}}` (the deps manager), `{{parentDir}}` (the name of the directory of the dependency's file, `""` at the root) and `{{packageFileDir}}` (that directory's path, `""` at the root); the triple-brace forms (`{{{manager}}}`) are the same. Any other template is a config error naming these three. The expanded text is cleaned as Renovate cleans branch names: whitespace is dropped, characters git rejects become `-`, repeated `/` and `-` collapse, and no component starts with `-` or `.`, so `{{parentDir}}-` of a file at the root expands to nothing.
- **Sharing.** Updates whose expanded prefix differs never share a branch or PR, also in a [group](#rulesgroup): `{{manager}}/` keeps managers apart (`deps/github-actions/anchore-syft`, `deps/homebrew/anchore-syft`; groups `deps/github-actions/group/tools`), `{{parentDir}}-` splits a monorepo by directory (`deps/web-react`). Lock file maintenance ignores it.
- **Members' settings.** When one dependency is updated by several managers or files, their settings combine as in a [group](#rulesgroup): the PR automerges only when every member does (with the first member's `automergeStrategy`), is a draft or needs [dashboard approval](#dashboardapproval) when any member asks, waits for the most waiting [`prCreation`](#prcreation), is created only when every member is in [schedule](#schedule), and gets the labels, reviewers and assignees of all members; the commit type is `fix` when any member's is, the scope and [PR templates](#prtitle) are the first member's. The title is `update <name> to <v>` when every member moves to the same version; versions differing only by a leading `v` (an action's `v0.7.0`, a formula's `0.7.0`) count as the same, and the title shows the first member's form.
- **Rules** override it for matching deps (with `updateTypes`: for those updates); `""` removes it.
- **Upgrading from deps 0.7 and earlier.** Those versions named branches `<prefix><manager>/<slug(name)>`, one per manager. The first run with the new names opens the PRs under them and closes the per-manager PRs as obsolete (with their branches), as for any branch no update wants. `additionalBranchPrefix: '{{manager}}/'` keeps the old single-dependency names (groups then also split per manager, `<prefix><manager>/group/<slug>`). A new branch whose name is the directory of a remaining old branch (dep `helm`, branches `deps/helm/…`) can only be pushed once those are gone, normally on the next run.

```yaml
additionalBranchPrefix: '{{manager}}/'   # deps/github-actions/actions-checkout
rules:
  - paths: ['web/**']
    additionalBranchPrefix: '{{parentDir}}-'
```

## Config.baseBranches

`deps run` proposes the updates of each base branch on its own: it resolves the dependencies of the branch's tip and opens the PRs against it. Renovate calls this `baseBranchPatterns` (formerly `baseBranches`); Dependabot sets a `target-branch` per update entry.

- **Entries.** A branch name, `$default` (the repository's default branch), or an RE2 regex `/<RE2>/` (flag `i` allowed) matching branch names on origin, in their sorted order. The unfolded list keeps the order of the entries without duplicates. A name origin lacks is skipped with a warning (`base branch not found on origin, skipping it`); when nothing is left, `run` fails with `baseBranches: no branch on origin matches [...]`.
- **Branch names.** With more than one entry, or a regex entry (it can match more branches later), every update branch carries its base: `<branchPrefix><base>/<additionalBranchPrefix><slug(name)>`, `<branchPrefix><base>/<additionalBranchPrefix>group/<slug(group)>`, e.g. `deps/release/1.x/golang.org-x-net`. A single named entry keeps the plain names. Switching from one base to several renames the branches: the open PRs of the old names are closed as obsolete in that run.
- **Checkouts.** The checkout of `--dir` serves the base whose tip it is; every other base is fetched (shallow in a shallow clone) and checked out into a temporary linked work tree, removed when `run` ends. Every base is resolved with the config of the checkout (Renovate's default `useBaseBranchConfig: none`), its rules matching that base with [`rules[].baseBranches`](#rulesbasebranches); with [`useBaseBranchConfig`](#usebasebranchconfig) each base's own config file is merged over it.
- **Per base:** the PRs of a base are those targeting it, so its obsolete PRs are closed only when its own resolution had no error; branches without a PR are deleted only when no base had one. PRs to branches the run does not handle are left alone.
- **For the run:** the PR limits ([`prConcurrentLimit`](#prconcurrentlimit), `prHourlyLimit`, `--max-new-prs`) count all bases together, and the [dashboard](#dashboard) is one issue with a section per base.
- `run --base <branch>` overrides the key: one base, the checkout's, with the plain branch names. `plan` and `apply` work on the checkout as before; `explain --base <branch>` and `preview --base <branch>` resolve a base as `run` does.

```yaml
baseBranches: [$default, '/^release\/v\d+$/']
rules:
  - baseBranches: ['/^release\//']   # release branches get no majors
    updateTypes: [major]
    ignore: true
```

## Config.useBaseBranchConfig

Renovate's `useBaseBranchConfig`: which config `deps run` applies to a base branch of [`baseBranches`](#basebranches) that it checks out into its own work tree (every base but the one the checkout of `--dir` is at).

| Value | Config of such a base |
|---|---|
| `none` (default) | the checkout's |
| `merge` | the config file of the same name on the base branch (`.deps.yaml`, `.deps.yml`, the `--config` path inside `--dir`, with `--renovate` its Renovate file translated) merged over the checkout's; a base without it fails the run with a config error |
| `merge-if-present` | as `merge`; a base without the file gets the checkout's config |

- **Merge.** As an [`extends`](#extends) chain merges, with the base's file (and the files it extends) last: its top-level keys replace the checkout's, its `rules` and `custom` entries follow the checkout's, `customDatasources` and `registryAliases` are merged by name. The merged config is validated as a whole; a problem in the base's file names it `<base>:<file>`.
- **Run-wide keys** stay the checkout's, as one run handles every base: `baseBranches`, `useBaseBranchConfig` (as in Renovate), `branchPrefix`, `git`, `prConcurrentLimit`, `prHourlyLimit`, `prNotPendingHours`, `rebaseWhen`, `rebaseLabel`, `stopUpdatingLabel`, `updateNotScheduled` and `dashboard`. A base that sets another value logs a warning (`the base branch's config sets a key that spans the run`).
- **Which config applied.** `run` prints `base    <base>  config .deps.yaml + <base>:.deps.yaml` before the updates of a merged base; `explain --base <base>` prints the same line and names the base's rules `from <base>:.deps.yaml:<line>`; `preview --base <base>` (default `$GITHUB_BASE_REF`, `$CI_MERGE_REQUEST_TARGET_BRANCH_NAME`) merges both checkouts' files over the default branch's config when the pull request targets another branch and the default branch's config sets `useBaseBranchConfig`, and names the files in its output and step summary.

```yaml
baseBranches: [$default, '/^release\//']
useBaseBranchConfig: merge   # release branches add their own rules in their .deps.yaml
```

## Config.rebaseWhen

| Value | An unchanged open PR is rebuilt on the current base branch when |
|---|---|
| `conflicted` (default) | the platform reports the PR as not mergeable |
| `behind-base-branch` | also when the branch is not a single commit on the current base head (status `rebased`) |
| `never` | never; only content changes update the PR |

A PR whose content changed is always updated. Branches with commits by other authors are never rebuilt (see [`git.ignoredAuthors`](#gitignoredauthors)).

## Config.builtins

Switches for what deps does on its own, without any rule: all three are on unless set to `false`. They are the built-in data and lookups Renovate gets from presets (`config:recommended`'s vulnerability alerts, `group:monorepos`, `replacements:all`).

```yaml
builtins:
  security: false      # no OSV lookups, no security updates
  monorepos: false     # no built-in monorepo, ruby and vcpkg groups
  replacements: false  # no built-in replacements
```

## Builtins.security

deps queries [OSV](https://osv.dev) for Go modules, npm packages, crates.io crates, PyPI packages, Maven artifacts and GitHub Actions. A vulnerable dep is moved to the smallest compatible release at or above the minimum fixed version.

- The go.mod `toolchain` directive is checked against the Go standard library (`stdlib`) and the go command (`toolchain`), so `toolchain go1.22.0` gets a security update to the smallest Go release of its line that fixes its advisories (`1.22.2` for GO-2024-2687). The `go` directive is checked too once a rule un-ignores it (it is ignored by default); a 2-part `go 1.22` admits every 1.22.x toolchain, so only an advisory without a fix in 1.22 moves it.
- Container images, Terraform providers and modules, and Helm charts get no security updates: OSV has no ecosystem for them. The queries per ecosystem are listed under [Security advisories (OSV)](managers.md#security-advisories-osv).
- Security updates bypass `allow`, `allowedUpdateTypes`, schedules, `minimumReleaseAge` and rules with `updateTypes`. They never bypass a pre-policy `ignore`, except the default ignore of pip_requirements `indirect` pins (the transitive pins of pip-compile/`uv pip compile` output, which Renovate's vulnerability alerts enable too): those get security updates, and only those, unless a rule ignores them.
- They always use commit type `fix`, get a `[security]` title suffix, and are not put into a `group`. A rule's [`securityGroup`](#rulessecuritygroup) opts in to batching them into a PR of their own.
- A failed OSV lookup is reported as an error (`security advisories: <err>`). Advisories that cannot be fetched while others can are logged as a warning and left out.

```yaml
builtins:
  security: false
```

## Builtins.monorepos

When `true`, deps released from one source repository share a PR, with group `<name> monorepo`. The 62 built-in entries include kubernetes (`k8s.io/` staging modules), go-openapi, opentelemetry-go, opentelemetry-go-contrib, aws-sdk-go-v2, azure-sdk-for-go, google-cloud-go, moby, etcd, sigstore, typescript-eslint, eslint, babel, jest, vitest, angular, react, vue, storybook, tailwindcss, playwright, codeql-action and actions-cache. The full list is in `internal/config/monorepos.go`.

The Ruby version is grouped the same way: every dep of the `ruby-version` datasource (the Gemfile `ruby` directive, `.ruby-version`, the asdf and mise `ruby` tool) gets group `ruby`, so one PR moves all files naming it, `Gemfile.lock`'s `RUBY VERSION` included. `ruby` Docker images (`ruby:3.4-slim` in a Dockerfile or CI config) stay docker deps; a rule such as `{managers: [docker], names: [ruby], group: ruby}` adds them to that PR.

The deps of the `vcpkg` manager (registry baselines and port versions) get group `vcpkg`: a port is looked up at the new commit of its registry's baseline when that baseline moves in the same run, and a port version newer than the baseline does not install with the old one, so the bump and the port versions it unlocks share one PR. With another group, `builtins.monorepos: false`, or a port update in another bucket (a major with [`separate`](#separate) other than `none`), the port is looked up at its current baseline instead, and the versions the bump unlocks follow in the next run.

They are applied as implicit rules *before* the user rules, so any rule that sets `group` overrides them. `deps explain` shows them as `builtin  monorepo <name> -> group "<name> monorepo"`, `builtin  toolchain ruby -> group "ruby"` and `builtin  registry vcpkg -> group "vcpkg"`.

```yaml
builtins:
  monorepos: false
```

## Config.git

The commit identity ([`git.author`](#gitauthor), [`git.ignoredAuthors`](#gitignoredauthors)) and whether commits go through the GitHub API ([`git.platformCommit`](#gitplatformcommit)).

```yaml
git:
  author: 'ocmbot[bot] <125909804+ocmbot[bot]@users.noreply.github.com>'
  ignoredAuthors: ['125909804+ocmbot[bot]@users.noreply.github.com']
```

## Git.author

- Precedence, per field: `--author-name`/`--author-email` > `$DEPS_GIT_AUTHOR` > `git.author` > `deps[bot] <deps-bot@noreply.invalid>`.
- GitHub bot noreply addresses such as `ocmbot[bot] <1+ocmbot[bot]@users.noreply.github.com>` are accepted.
- An invalid value fails with `git.author: must be 'Name <email>', got "<v>"`.

## Git.ignoredAuthors

Each entry must be an email address. A branch whose tip was committed by one of them still counts as the bot's own, so it is updated and rebased instead of `skipped: branch modified by someone else`. Useful when the bot identity ([`git.author`](#gitauthor)) changes.

## Config.prConcurrentLimit

Managed PRs are those under [`branchPrefix`](#branchprefix). New PRs are only created while the open count plus PRs created this run stay below the limit.

- **Renovate defaults to 10** (and `prHourlyLimit` to 2), so set both explicitly to keep Renovate's throttling.
- The `run` flag `--max-new-prs` (default `10`, `0` = unlimited) is a separate per-run cap. A run creates at most the minimum of every non-zero limit. Groups over the limit get `skipped: new PR limit reached`. Existing PRs are always updated.

```yaml
prConcurrentLimit: 10
prHourlyLimit: 2
```

## Config.prHourlyLimit

Open and closed PRs are counted by their creation time. **Renovate defaults to 2.** Combined with `prConcurrentLimit` and `--max-new-prs` as described under [`prConcurrentLimit`](#prconcurrentlimit).

## Config.actions

See [`actions.defaultHost`](#actionsdefaulthost).

## Actions.defaultHost

First match wins:

1. `$DEPS_DEFAULT_ACTIONS_URL`, its hostname (source `env`);
2. `actions.defaultHost` (source `config`);
3. the platform default (source `platform default`): `data.forgejo.org` on Forgejo, otherwise `github.com`.

With `--log-level info`, deps logs the result as `actions default host host=<h> source=<s>`. Gitea instances with a non-default `DEFAULT_ACTIONS_URL` should set this key.

```yaml
actions:
  defaultHost: gitea.com
```

## Config.terraform

See [`terraform.defaultRegistry`](#terraformdefaultregistry).

## Terraform.defaultRegistry

The registry of provider and module addresses without a host (`hashicorp/aws`, `terraform-aws-modules/vpc/aws`, `tfr:///…` in Terragrunt). Without it, `*.tf` files use `registry.terraform.io` and `*.tofu` files OpenTofu's `registry.opentofu.org`, as the two CLIs do. Set it when an OpenTofu project keeps `*.tf` files, or for a private mirror implementing the registry protocol. A scheme and trailing slash are ignored. For Terragrunt's `tfr:///…` sources, `$TG_TF_DEFAULT_REGISTRY_HOST` takes precedence when set, as it does for Terragrunt.

```yaml
terraform:
  defaultRegistry: registry.opentofu.org
```

## Config.gomod

See [`gomod.updateImportPaths`](#gomodupdateimportpaths).

## Gomod.updateImportPaths

Go majors from v2 on live under their own module path (`github.com/google/go-github/v92` → `/v93`). For every go.mod requirement whose major updates the rules allow, deps lists the next module paths (`/v<N+1>`, … up to 5; `/v2` for v0/v1 modules, `gopkg.in/x.v<N+1>`) from the GOPROXY and proposes the newest release of each as a `major` update (PR buckets as for other majors, see [`separateMajorMinor`](#separate)).

- The update rewrites the require (path and version) and the import paths of the old module and its packages in the `.go` files of that module (vendor, testdata and nested modules excluded); `go mod tidy` sees the rewritten files.
- The new major's API may differ, so the PR may need code changes before it builds; its body says so.
- `false` proposes no module path changes at all: without rewritten imports `go mod tidy` would put the old path back.
- Renovate's equivalent is `postUpdateOptions: [gomodUpdateImportPaths]`, which is off there by default.

```yaml
gomod:
  updateImportPaths: false
```

## Config.registryAliases

Renovate's `registryAliases`. A key is a prefix as written in files, usually a CI variable (`$CI_REGISTRY`, `${CI_REGISTRY}`, `${CI_REGISTRY:-}`) or a pull-through cache path; its value is the registry (with an optional path) it stands for, or `""` for Docker Hub. A trailing slash on the value is ignored.

- An image reference `<key>/<path>[:<tag>][@<digest>]` is looked up as `<value>/<path>`; a key ending in `}` also matches without the `/` (`${REGISTRY}alpine:3.22`). The longest matching key wins. A bare `$VAR` key does not match `$VAR_OTHER/…`.
- The dep's name is the resolved image (`registry.example.com/team/app`), which rules match. Updates edit the tag and digest only, so the file keeps the alias; aliased images are not renamed by replacements.
- It applies to the image references of the docker manager (Dockerfiles after ARG substitution, Compose files, workflows, actions, Kubernetes manifests, Helm values `repository`), gitlabci, kustomize, terraform, devcontainer, ansible, quadlet, crossplane, the CI pipeline managers (bitbucket-pipelines, woodpecker, crow, droneci, circleci, azure-pipelines, cloudbuild, velaci, buildkite) and tekton (bundles and step images). Without a matching alias, a reference with a variable is skipped as `variable reference`.
- **Helm** (Renovate's helm-v3 and flux): a Chart.yaml or requirements.yaml `repository: "@<key>"` (or `alias:<key>`) resolves to the chart repository `<value>` (an `https://`, `http://` or `oci://` URL), and a Flux HelmRelease `sourceRef` naming no scanned HelmRepository falls back to the entry of its name. A Fleet `helm.repo` URL that is a key is looked up at its value (Renovate's fleet manager). Without an entry, a repositories.yaml in the repository, the Chart.lock and Renovate's default `stable: https://charts.helm.sh/stable` resolve the alias; see the [helm manager](managers.md#helm).
- Extended files merge it by key, a later file's entry replacing an earlier one's.

```yaml
registryAliases:
  $CI_REGISTRY: registry.gitlab.example.com
  ${HARBOR}/proxy: harbor.example.com/dockerhub-proxy
```

## Config.minimumReleaseAge

- **Syntax:**
  - `<n> <unit>`, where the unit is minutes, hours, days or weeks, written singular, plural or as the first letter, with the space optional: `28 days`, `1 day`, `3d`, `12h`, `90m`, `2w`;
  - `0`;
  - a Go duration: `1h30m`.

  Negative or unparsable values are errors (`invalid age "<v>": want e.g. "3 days", "12h", "90m" or "0"`).
- **Fallback:** a too-new candidate is rejected as `too new (released <date>, minimum <age>)`, and the next older candidate in the same PR bucket is tried.
- **Unknown release times** are allowed by default (`minimumReleaseAgeBehaviour: timestamp-optional`). This covers datasources without release times (`custom.<name>`, `bazel`: Bazel registries such as the BCR publish none), images without a `created` time and failed time lookups.
- **Exempt:** digest refreshes, pins and security updates. Go pseudo-version digest updates are not exempt: the new pseudo-version's commit time is checked against the age of the `digest` update type.
- `rules[].minimumReleaseAge` overrides the value per dep. With `updateTypes` it overrides it per update type. `0` or `0 days` turns the age off for matching deps.

```yaml
minimumReleaseAge: 28 days
rules:
  - names: ['open-component-model/cosign']
    minimumReleaseAge: 0 days
```

## Config.minimumReleaseAgeBehaviour

- `timestamp-optional` (the default): a candidate whose release time is unknown passes `minimumReleaseAge`.
- `timestamp-required`: such a candidate is held back like a too-new one, as `release time unknown (minimum <age>, timestamp-required)` in the trace and the summary's held-back list, and the next older candidate in the same PR bucket is tried. After 3 candidates without a time the bucket gives up: the datasource likely has none for the dep.
- Without a `minimumReleaseAge` (or with `0`) the behaviour has no effect. Digest refreshes, pins and security updates stay exempt in both modes.
- The names are Renovate's. Renovate defaults to `timestamp-required` since version 42; deps keeps `timestamp-optional` so that adding `minimumReleaseAge` does not silently stop updates of deps whose datasource has no times. Set `timestamp-required` to match Renovate.
- `rules[].minimumReleaseAgeBehaviour` overrides it per dep, with `updateTypes` per update type.

```yaml
minimumReleaseAge: 28 days
minimumReleaseAgeBehaviour: timestamp-required
rules:
  - managers: [custom] # in-house datasources without release times are not held back
    minimumReleaseAgeBehaviour: timestamp-optional
```

## Config.abandonmentThreshold

A dependency whose newest release is older than this is listed under **Abandoned dependencies** on the [dashboard](#dashboard), and its update PRs say so under **Warnings** (Renovate's `abandonmentThreshold`).

- **Syntax:** as for [`minimumReleaseAge`](#minimumreleaseage), plus months (30 days) and years (365 days): `1 year`, `18 months`, `2y`. Empty or `0` turns the check off (the default).
- **Newest release:** the highest stable release the datasource lists. With npm, the package's `modified` time bounds every release, so a package not modified within the threshold is abandoned without another request (`no release since <date>`); otherwise the newest release's time is looked up, once per dep (`newest release <v> published <date>`; for npm that is the full packument, shared with `minimumReleaseAge` and the PR's age column). Datasources without release times, and unknown times, never flag a dep.
- Only deps that are looked up are checked: ignored deps and deps without a valid version are not.
- `rules[].abandonmentThreshold` overrides it per dep; `0` turns it off for matching deps.

```yaml
abandonmentThreshold: 2 years
rules:
  - managers: [github-actions]
    abandonmentThreshold: 0
```

## Config.ignorePaths

- When the key is **absent**, Renovate's `:ignoreModulesAndTests` list (the default above) applies.
- Any explicit list, including `[]` or a null value, replaces that default.
- Independently of `ignorePaths`, discovery always prunes `.git`, `node_modules`, `vendor` and `testdata` directories.
- Each entry must be a valid doublestar glob or a `/RE2/` regex (flag `i` allowed) matched anywhere in the path; `!` negations are rejected.

```yaml
ignorePaths: ['**/node_modules/**', 'docs/**']
```

## Config.separate

`separate` splits one dep's candidate updates into **PR buckets**. Each bucket yields at most one update, and each update gets its own branch (suffix) and PR:

| `separate` | Buckets (branch suffix) |
|---|---|
| `none` | one bucket, the newest update of any kind (no suffix) |
| `major` (default) | newest major (`-major`); newest minor/patch (no suffix) |
| `majors` | one per newer major *N*, the newest release of each (`-v<N>`); minor/patch (no suffix) |
| `minor-patch` | newest major (`-major`); newest minor (`-minor`); newest patch of the current minor (`-patch`) |
| `majors-minor-patch` | one per newer major (`-v<N>`); newest minor (`-minor`); newest patch (`-patch`) |

Renovate's `separateMajorMinor: false` is `none`, `separateMultipleMajor` is `majors`, `separateMinorPatch` is `minor-patch`, and both together are `majors-minor-patch`.

- The bucket of a candidate comes from the policy of its update type. A rule with `updateTypes` can therefore set `separate` for its types, unlike in Renovate (see [behavior differences](../migrating-from-renovate.md#behavior-differences)).
- `plan`/`apply` write one update per dep: the newest non-major one, else the lowest major. The others are reported with `, separate PR`.
- A policy group inherits its members' bucket suffix: `deps/group/actions-patch`.

## Config.updateNotScheduled

- `true` (the default): open PRs outside their schedule are still updated (content changes, conflicts, `rebaseWhen`). Only *creating* a PR waits for a run triggered by one of their crons or inside one of their time windows.
- `false`: out-of-schedule groups are left untouched (`skipped: outside schedule`).

Out-of-schedule PRs are never closed as obsolete.

## Config.automerge

deps itself never merges red or pending PRs or branches. Rules override `automerge`, `automergeStrategy` and `automergeType` per dep or per update type, so Renovate's `matchUpdateTypes: [minor, patch], automerge: true` is:

```yaml
rules:
  - updateTypes: [minor, patch]
    automerge: true
```

- A PR automerges only when **every** update in it does; a group that mixes an automerged minor with a major does not. The strategy comes from the PR's first update.
- The request is sent after the PR is created, updated or rebased, and on every run for an up-to-date automerge PR, until the platform reports it scheduled. Security updates follow the same policy.
- With [`automergeType: branch`](#automergetype) there is no PR: deps merges the branch itself once its checks pass.
- Outcomes are listed under [automerge outcomes](cli.md#deps-run); platform behavior and the GitHub fallback are described in [design.md](../design.md#automerge).

## Config.automergeStrategy

`auto` uses the repository's default: Gitea/Forgejo `default_merge_style`, on GitHub the first method the repository allows (merge commit, then squash, then rebase), and on GitLab the project's squash default. GitLab's merge method (merge commit, semi-linear, fast-forward) is a project setting, so there `squash` squashes and `merge` and `rebase` merge without squashing. Branch automerge (`automergeType: branch`) always fast-forwards and ignores it.

## Config.automergeType

`pr` (the default) requests the platform's auto-merge for the update PR. `branch` is Renovate's branch automerge: updates land on the base branch without a PR, so passing updates are silent.

1. deps pushes the update branch (one commit on top of the base) and opens no PR.
2. Later runs read the commit statuses and check runs of the branch tip:
   - none reported yet, or still running: `branch automerge waiting for checks`;
   - all green: deps fast-forwards the base branch to the branch with `git push --force-with-lease` (so the push fails if the base moved) and deletes the branch (`merged`);
   - one failed: deps opens the PR, noting why in its body, and requests platform auto-merge for it like `automergeType: pr`.
3. A branch behind the base, or with other content, is rebuilt and pushed again, whatever `rebaseWhen` says: only a branch directly on the base can be fast-forwarded. Its checks then start over.
4. Only one branch is merged per run, because merging moves the base. Other green branches report `branch automerge next run`; the next run rebases and merges them one by one.
5. If the base branch declines the push (branch protection, a required PR, a pre-receive hook), deps opens the PR instead, as for failed checks.

- CI must run on pushes to the update branches (`deps/**`), and the token must be allowed to push to the base branch. On GitHub a protected base branch needs the token's App or user on the bypass list; on Gitea and Forgejo it needs push access in the branch protection (*Enable push*, with the user allowlisted if restricted); on GitLab the token's role must be *Allowed to push* on a protected branch.
- A repository without any checks never merges: the branch waits. Tick it under **Pending branch automerge** on the [dashboard](#dashboard) to open its PR instead.
- Once a PR exists for the branch, it stays a PR and is automerged as such; the strategy then applies again.
- New branches count toward the PR limits (`--max-new-prs`, `prConcurrentLimit`, `prHourlyLimit`) of the run that pushes them, as does raising a PR for a failed branch.
- Branches no update wants anymore and without a PR are deleted after an error-free run (`deleted`), unless someone else committed to them.
- A group merges its branch only when **every** update in it uses `automergeType: branch`; otherwise it automerges as a PR. Lock file maintenance follows the top-level value.

## Config.draftPR

Renovate's `draftPR`: new update PRs are opened as drafts, which the forge does not merge until someone marks them ready for review. It applies when the PR is created; deps never turns an existing PR into a draft or back.

- GitHub opens a draft PR (the `draft` flag of the API). Marking it ready is up to you.
- Gitea and Forgejo have no draft flag in their API: the title starts with `WIP: `, one of the forges' default work-in-progress prefixes. Title updates keep the prefix while the PR is a draft; remove it from the title to mark the PR ready, and deps leaves it off.
- GitLab likewise marks a draft merge request by its title: it starts with `Draft: `, kept on title updates while the merge request is a draft (GitLab reports `draft`).
- A PR that automerges is never a draft: forges do not merge drafts. Rules turn it on or off per dep or update type, e.g. drafts for majors only:

```yaml
rules:
  - updateTypes: [major]
    draftPR: true
```

## Config.prCreation

Renovate's `prCreation`. With `immediate` (the default) the PR is opened right after the branch is pushed. The other modes push the branch first and open its PR on a later run, once its commit statuses and check runs are in, so a PR arrives with its test results:

| Mode | The PR is opened when the branch's checks | Until then |
|---|---|---|
| `immediate` | (right away) | |
| `not-pending` | have finished: all passed, or one failed | `PR waiting for checks` |
| `status-success` | all passed | `PR waiting for checks`, or `PR waiting for green checks` after a failure |

1. The first run pushes the branch (one commit on top of the base) without a PR: `created, PR waiting for checks`.
2. Later runs read the checks of the branch tip and open the PR when the mode allows (`created`); until then the branch is `up-to-date` with the outcome above. deps does not wait for the checks within a run.
3. A branch with other content than this run builds is rebuilt and pushed again (`updated`), and its checks start over. A branch that is only behind the base is kept, unless `rebaseWhen: behind-base-branch` (then it is rebuilt on the base, like a PR branch) or its commit was made through the API (`git.platformCommit`): deps cannot read such a branch's files locally, so it rebuilds it whenever the base moved.
4. Waiting branches are listed under **Pending status checks** on the [dashboard](#dashboard); ticking one opens its PR on the next run without waiting.

- CI must run on pushes to the update branches (`deps/**`); with workflows that only run on `pull_request` the branch never gets checks and never gets its PR. A repository without any checks waits forever too (Renovate's `prNotPendingHours` fallback is not offered); tick the branch on the dashboard to open its PR.
- New branches count toward the PR limits of the run that pushes them, as does opening the PR of a waiting branch.
- Once the PR is open, the branch is handled like any PR branch. Groups use the most waiting mode of their updates; [`automergeType: branch`](#automergetype) groups have no PR anyway and ignore it. Lock file maintenance follows the top-level value.

## Config.prTitle

The title of each update PR and the subject of its commit, as a [Go `text/template`](https://pkg.go.dev/text/template) over these fields of the PR:

| Field | Value | Renovate |
|---|---|---|
| `.Type` | conventional commit type: `fix` for production deps, else `chore`, or [`rules[].commitType`](#rulescommittype); always `fix` for security updates | `semanticCommitType` |
| `.Scope` | commit scope, `deps` or [`rules[].commitScope`](#rulescommitscope); `""` = none | `semanticCommitScope` |
| `.Name` | the dependency, or the group name when the PR updates several (`lock file maintenance` for that PR) | `depName` |
| `.Group` | the policy group, or the shared version definition (`jackson.version`) of the PR; `""` = none | `groupName` |
| `.From` | the current version (the locked one for lockfile-only updates), when every update has the same; else `""` | `currentVersion` |
| `.To` | the new version, when every update has the same; else `""` | `newVersion` |
| `.UpdateType` | `major`, `minor`, `patch`, `digest`, `pin` or `lockFileMaintenance`, when every update has the same; else `""` | `updateType` |
| `.Count` | the number of dependencies | `upgrades.length` |
| `.IsSecurity` | some update fixes advisories | `isVulnerabilityAlert` |

- Empty (the default) means the built-in titles described under [`rules[].group`](#rulesgroup).
- The title is one line: runs of white space, newlines included, become one space.
- Titles of PRs with security updates end with ` [security]`; deps appends it unless the template already does.
- The lock file maintenance PR keeps its title (`chore(deps): lock file maintenance`); `prHeader` and `prFooter` apply to it.
- A group's PR takes the templates of its first update (as for `commitType` and `commitScope`); its type is `fix` when any member's is.
- [`rules[].prTitle`](#rulesprtitle), `prHeader` and `prFooter` override the templates per dep or, with `updateTypes`, per update type; `''` resets them to the built-in title, or no header or footer.
- **Validation.** Templates are parsed when the config is loaded: every field reference must name a field above, also in branches that are not taken (`rules[0].prTitle: line 1, column 13: unknown field .DepName (fields: .Type, ...)`), and each template is executed on sample data (a single dependency, and a security group without versions or scope), where a title must not render empty. Errors name the config line and the line and column in the template; the built-in functions of `text/template` (`printf`, `eq`, `and`, `or`, `slice`, ...) are available.
- **Existing PRs.** The content fingerprint covers the file contents only, so a changed template rewrites the title and body of an open PR the next time its content changes, not before. Closed PRs are matched by the `deps:versions` marker in their body, not by title, so a changed template does not reopen closed updates (see [closed PRs](../design.md#pr-lifecycle-deps-run)).

```yaml
prTitle: '{{.Type}}{{with .Scope}}({{.}}){{end}}: bump {{.Name}}{{with .To}} to {{.}}{{end}}'
prHeader: |
  This PR updates {{if eq .Count 1}}{{.Name}}{{else}}{{.Count}} dependencies of {{.Name}}{{end}}.
prFooter: '{{if .IsSecurity}}**Security update: please review soon.**{{end}}'
rules:
  - managers: [github-actions]
    prTitle: 'ci: bump {{.Name}} from {{.From}} to {{.To}}'
```

## Config.prHeader

Rendered above the update table of every update PR body, with the fields of [`prTitle`](#prtitle); leading and trailing white space is trimmed, and an empty result adds nothing. Use it for review instructions or links. The marker comments and the rebase checkbox stay deps'.

## Config.prFooter

Rendered below the release notes, above the workflow run link (in Actions, see `deps help run`), the rule and the rebase checkbox, with the fields of [`prTitle`](#prtitle); trimmed like [`prHeader`](#prheader).

## Rule.postUpgradeTasks

Shell commands, Renovate's `postUpgradeTasks`, for generated code or formatting that must follow an update (`go generate ./...`, `make generate`): a list of commands, or `{commands, executionMode, files}`. The last matching rule that sets it decides; `[]` clears it. `plan` notes `postUpgradeTasks run in apply/run`.

- **Allowlist.** `apply` and `run` run a command only when a pattern of `--allow-tasks` (repeatable; `DEPS_ALLOW_TASKS` or the action input `allow-tasks`, one per line) matches it whole, with runs of spaces and tabs compared as one space: `go generate ./...` (exactly that), `make *` (a make call), `*` (every command). `*` matches any text except command chaining and substitution (`;`, `&`, `|`, backticks, `$(`, line breaks), so `make *` allows `make docs` but not `make docs; curl …` or `make $(id)`; operators written in the pattern match literally (`echo * >> deps.txt`), and the pattern `*` alone allows every command. Before anything runs, a group with a command no pattern allows fails with `postUpgradeTasks command not allowed: "<command>"; allow it with --allow-tasks '<command>' (or DEPS_ALLOW_TASKS)`; with no patterns, every task fails that way.
- **Files.** `files` (doublestar globs on paths relative to `--dir`; `!glob` excludes, as in [`paths`](#rulespaths)) limits the files the commands' changes are committed for; elsewhere the build's changes stay as they were. Without `files`, every change `.gitignore` does not ignore counts. In a group, a change is kept when any task entry keeps it (an entry without `files` keeps all). `apply` restores every other file below `--dir` the commands changed to its state before them (found with a temporary index of `HEAD`, so ignored files are left alone).

- **Execution mode.** `branch`, the default (also of the list form), runs each distinct command once per branch. `update` runs the commands once per update of the branch, in the branch's order, each run seeing the files of the runs before it, with the update in `DEPS_MANAGER`, `DEPS_PACKAGE_FILE`, `DEPS_DEP_NAME`, `DEPS_DEP_TYPE`, `DEPS_DATASOURCE`, `DEPS_UPDATE_TYPE`, `DEPS_CURRENT_VERSION` (the locked version of a lockfile-only update), `DEPS_NEW_VERSION`, `DEPS_CURRENT_DIGEST`, `DEPS_NEW_DIGEST` and `DEPS_NEW_NAME` (a replacement's or Go major module path's new name). Commands are not templated; use the variables instead (`helm dependency update "charts/$DEPS_DEP_NAME"`). A group's branch-mode commands run after all its update-mode runs; a failing update-mode run names its dep (`postUpgradeTasks: <command> (<dep>): …`).
- `run` copies the work tree of `--dir` (without `.git`) to a temporary directory and makes the copy a throwaway git repository of the base commit: HEAD detached at the base, the index read from it, the tags and the `origin` URL, and the checkout's objects shared read-only, so `git status`, `diff`, `log`, `describe` and even `commit` work and show exactly the group's changes. Commands run in `--dir` of the copy, in order with `sh -c`; then every file below `--dir` they add, change or delete that `.gitignore` does not ignore is committed. Whatever they write to git stays in the copy: the checkout's repository is only read. Initialized submodules get the same throwaway repository at their checked-out commit, uninitialized ones stay empty. Files inside submodules never join the commit, and a command that moves a submodule's HEAD fails the group (`postUpgradeTasks: <command> changed submodule <path>; submodule updates are not supported`). `apply` runs the commands in `--dir` after writing the updates.
- Commands inherit deps' environment without its tokens (`DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN`, `GITLAB_TOKEN`, `DEPS_GITHUB_COM_TOKEN`, `DEPS_GITLAB_TOKEN`, `DEPS_AZURE_DEVOPS_TOKEN`, `DEPS_CIRCLECI_TOKEN`, `CI_JOB_TOKEN`, `DEPS_SIGNING_KEY`) and without `GIT_DIR`-like variables, and are not templated. Each may take 15 minutes; a failure fails the group like a post-processor: `postUpgradeTasks: <command>: <error>: <last 20 lines of output>`.
- They run repository code: see the [security notes](../ci.md#post-upgrade-tasks).

```yaml
rules:
  - managers: [gomod]
    postUpgradeTasks: ["go generate ./..."]
  - managers: [helm]
    postUpgradeTasks:
      commands: ['helm dependency update "${DEPS_PACKAGE_FILE%/*}"']
      executionMode: update
      files: ["charts/**", "!**/*.bak"]
```

with `deps run --allow-tasks 'go generate ./...' --allow-tasks 'helm dependency update *'`.

## Rule.prTitle

Overrides the top-level [`prTitle`](#prtitle) for matching deps; `''` restores the built-in titles. `rules[].prHeader` and `rules[].prFooter` work the same, with `''` for none. With `updateTypes` the override applies to updates of those types, so majors can get their own title.

## Config.rebaseLabel

Every update PR body ends with a checkbox, `- [ ] <!-- deps:rebase --> If you want to rebase/retry this PR, check this box`. Ticking it, or adding this label to the PR, makes the next `deps run` rebuild the branch on the current base like the **Open** checkbox of the [dashboard](#dashboard):

- The request bypasses `schedule` (also with `updateNotScheduled: false`) and rebuilds a branch someone else committed to, discarding those commits.
- Unchanged content is `rebased`, changed content `updated`. The rewritten body has the checkbox unticked again, and deps removes the label once the branch is pushed; a failed removal is reported as an error, and the next run rebases again. A dry run reports `would-rebase` (or `would-update`) and changes nothing.
- The content fingerprint covers the file contents only, not the body, so ticking the checkbox (or editing the body) never counts as a change by itself.
- The name must be non-blank and must not contain `,`; labels compare case-insensitively.

## Config.stopUpdatingLabel

An open update PR with this label is left alone: deps does not build, push or rebase its branch, ignores its rebase checkbox, the [`rebaseLabel`](#rebaselabel) and the dashboard, and does not close it when its update is gone (it stays open until someone closes or merges it). The run reports it as `skipped: stop-updating label`, and the [dashboard](#dashboard) lists it under **Ignored or blocked**. Remove the label to hand the PR back to deps. It must differ from `rebaseLabel`.

## Config.dashboard

With [`dashboard.enabled: true`](#dashboardenabled), `deps run` maintains one open issue titled [`dashboard.title`](#dashboardtitle). It lists, in this order:

1. **Pending approval**: updates of [`dashboardApproval`](#dashboardapproval) without a PR or branch. Ticking one makes the next run create its PR.
2. **Awaiting schedule** and **Rate-limited**: updates without a PR because of `schedule` or the PR limits (`--max-new-prs`, `prConcurrentLimit`, `prHourlyLimit`). Awaiting-schedule items name the schedule entries they wait for, e.g. ``(waits for `0 0 * * 0`)`` or ``(waits for `before 6am on monday`)``. Each has a checkbox; ticking it makes the next run create the PR regardless of schedule and limits.
3. **Ignored or blocked**: updates whose PR was closed without merging, for that version or, for a major update, for that major (checkbox: recreate the PR), branches someone else pushed to (checkbox: discard those commits and rebuild the branch), and PRs with the [`stopUpdatingLabel`](#stopupdatinglabel).
4. **Pending branch automerge**: branches of [`automergeType: branch`](#automergetype) waiting for their checks, without a PR. Ticking one makes the next run open its PR instead.
5. **Pending status checks**: branches whose [`prCreation`](#prcreation) waits for their checks, without a PR (`checks failed` under `status-success`). Ticking one makes the next run open its PR now.
6. **Open**: the open update PRs, each with a checkbox that forces a rebuild of its branch.
7. **Deprecated dependencies**: deps whose registry marks the package or the installed version deprecated (npm `deprecated`: a deprecated `dist-tags.latest` deprecates the package, else the installed version's message counts), yanked (crates; PyPI releases on the JSON API and on other indexes' simple API, PEP 592 `yanked`/`data-yanked`) or inactive (PyPI `Development Status :: 7 - Inactive`), or whose source repository is archived (github-releases deps, when a GitHub token makes the release list a GraphQL query; git-tags and git-refs deps such as GitHub Actions on github.com with a GitHub token, on GitLab and on Gitea/Forgejo hosts; gitlab-, gitea- and forgejo-releases/-tags deps), and GitHub-hosted runner images GitHub deprecated (`runs-on: ubuntu-20.04`, the github-runners table). These come from the documents the release lookup fetches; the archived flag of a git-tags or forge repository costs one request per repository, sent alongside its tag or release list, and on github.com one GraphQL query per 100 repositories of the run. The update PRs of such a dep repeat the reason under **Warnings**.
8. **Abandoned dependencies**: deps whose newest release is older than [`abandonmentThreshold`](#abandonmentthreshold) (off by default).
9. **Errors** of the run (at most 20, each cut to 300 characters).
10. **Detected dependencies**: every extracted dependency (skipped ones too), as `name version` per manager and file in collapsed `<details>` blocks.

- With several [base branches](#basebranches), items 1 to 5 are listed per base under ``## Base branch `<base>` `` (a base with nothing to list is left out), errors of a base's resolution start with `<base>: `, and the detected dependencies have one ``### `<base>` `` block per base.
- The body is capped at 60 000 bytes (GitHub allows 65 536 characters). Lists that do not fit end with a `… not listed` note.
- The body is deterministic: the issue is only edited when its content changes. Every run regenerates it, so ticked checkboxes are unticked again once handled.
- The issue is found by its exact title and the `<!-- deps:dashboard -->` marker in its body, among the first 1 000 (GitHub, GitLab) or 500 (Gitea/Forgejo) open issues.
- deps never closes the dashboard. `dashboard.enabled: false` leaves an existing dashboard untouched.
- A repository without issues (disabled, or an external tracker on Gitea/Forgejo) reports `dashboard skipped: issues disabled` and is not an error.
- The token needs issues:write.

```yaml
dashboard:
  enabled: true
  labels: [dependencies]
```

## Dashboard.title

It must be non-blank and one line.

## Dashboard.labels

Each label must be non-blank and must not contain `,`. GitHub and GitLab create missing labels; Gitea and Forgejo look the names up in the repository's and its organization's labels and leave out unknown ones (with a warning).

## Config.dashboardApproval

Renovate's `dependencyDashboardApproval`: an update that needs approval gets no PR (and, with [`automergeType: branch`](#automergetype), no branch) until someone approves it on the [dashboard](#dashboard). Usually it is set per rule, e.g. for majors:

```yaml
dashboard:
  enabled: true
rules:
  - updateTypes: [major]
    dashboardApproval: true
```

- The run reports `skipped: awaiting dashboard approval` and does not build the update; the dashboard lists it under **Pending approval** with a checkbox. Ticking it makes the next run create the PR, regardless of `schedule` and the PR limits, like the other `create` checkboxes.
- Approval is needed once: an update with an open PR or an existing branch is handled as usual, also when its version moves on. A PR closed without merging is ignored as usual (**Ignored or blocked**); its `recreate` checkbox approves it too.
- A group needs approval when any of its updates does. Security updates never need approval (Renovate's `vulnerabilityAlerts` default). Lock file maintenance follows the top-level value.
- It requires [`dashboard.enabled: true`](#dashboardenabled). Without issues (`dashboard skipped: issues disabled`) nothing can be approved, so these updates wait.

## Config.extends

Shared organisation config (Renovate's `extends: ["local>org/renovate-config"]`): keep the common settings in one repository and let every repository's `.deps.yaml` build on them.

```yaml
extends:
  - my-org/deps-config                       # .deps.yaml on the default branch of my-org/deps-config
  - my-org/deps-config//presets/go.yaml#v2   # another file, at tag v2
  - https://github.com/other/cfg.git//deps.yaml#main
rules:
  - names: ['k8s.io/**']                     # this repository's own rules come last, so they win
    group: k8s
```

- **Entries** are `<repo>[//<path>][#<ref>]`. `<repo>` is `owner/name` (GitLab-style `group/sub/name` too) on the forge of the repository deps runs on (the CI environment's server, else the host of the checkout's `origin` remote), or a git URL (`https://`, `http://`, `ssh://`, `git://`, `file://`, or `user@host:path`; no passwords in it). `<path>` is a clean relative slash path in that repository, default `.deps.yaml`. `<ref>` is a branch, tag or commit (fetching a commit needs a forge that allows it, as GitHub and Gitea do), default the repository's default branch.
- **Reading.** Each file is fetched with git: a shallow fetch of the ref into a temporary bare repository, then the file's blob; nothing is checked out. Public repositories need no credentials. For an https repository on the platform host, the platform token (`DEPS_TOKEN`, …; `DEPS_GITHUB_COM_TOKEN` for github.com) is offered as `x-access-token:<token>` through a credential helper scoped to that host, after git's own credential helpers (which a CI job usually has none of), so it is in neither the command line nor a file; other hosts and SSH URLs use git's own credentials. A file is fetched once per run, also when several configs (or the repositories of `run --repositories`) extend it. On GitHub Actions the job's `GITHUB_TOKEN` reads only its own repository: a private config repository needs a token with read access to it (an App token or PAT as `DEPS_TOKEN`), or a public one.
- **Strict.** An extended file is decoded as strictly as `.deps.yaml` (unknown keys and wrong types are errors) and may itself have `extends`, resolved the same way. A file the chain already reaches through another entry is read once (its rules come where it is first reached); a cycle (`a` extends `b` extends `a`) is an error naming the chain. Every problem, also a missing file or a failed fetch, is a config error (exit 2) located at this file's `extends` entry it comes through, e.g. `my-org/deps-config//.deps.yaml: rules[1].allow: invalid constraint ...` or `extends[0]: my-org/deps-config//.deps.yaml: no file .deps.yaml at HEAD of https://github.com/my-org/deps-config.git`.
- **Merging.** The files are combined in order: each entry's file (after the files it extends), then the next entry's, then this file. `rules` are concatenated, so the rules of extended files come first and this file's rules override them (later matching rules win field by field); `custom` managers are appended; `customDatasources` and `registryAliases` are merged by name, a later file's entry replacing an earlier one's; every other top-level key a later file sets replaces the earlier value as a whole (`labels: []` clears inherited labels, `lockFileMaintenance` and `actions` are replaced, not merged). Keys no file sets keep their defaults; `ignorePaths` keeps the default list unless some file sets it.
- **Where a rule comes from.** `deps explain` prints each matching rule with its file and line, `(from my-org/deps-config//.deps.yaml:12)` or `(from .deps.yaml:5)`. Rule numbers (`#<i>`, `ignored by rule #<i>`) count the rules of the merged config.

## Config.rules

A rule has two groups of fields:

- **Match** fields select what the rule applies to: `managers`, `names`, `depTypes`, `paths`, `datasources`, `baseBranches`, `currentVersion`, `currentAge` and `updateTypes`. A rule **matches** a dep (or, with `updateTypes`, a candidate update of it) when every match field it sets matches; a list matches when any of its entries does. A rule without match fields matches every dep.
- **Apply** fields override the policy of what the rule matches: every other field (`ignore`, `pin`, `allow`, `allowedUpdateTypes`, `rangeStrategy`, `versioning`, `separate`, `group`, `commitType`, `commitScope`, `schedule`, `dashboardApproval`, `minimumReleaseAge` and the `automerge*` fields). Every apply field a matching rule sets **overrides** the value from earlier rules (field-wise); `allowedUpdateTypes` replaces the earlier list, it is not merged.

**Warnings.** `plan`, `apply`, `run` and `explain` warn on stderr (`warn    <path>: <field>: <msg>`) about rules that cannot have an effect: a rule without apply fields (or without any field), a `depTypes` entry no listed manager produces, a `datasources` entry no listed manager uses, and `depTypes` and `datasources` no dep has together (`managers: [gomod], depTypes: [devDependencies]` matches nothing: `devDependencies` are npm's). After resolving, `plan`, `apply` and `run` also warn about every rule of the repository's own config that matched none of its dependencies, e.g. one for a manager the repository does not use or a misspelled name glob: `warn    rule .deps.yaml:12 matched no dependency (managers=[npm] names=[react])`, with `--renovate` `warn    rule renovate.json packageRules[3] matched no dependency (…)`. Rules of extended configs and Renovate presets, and those deps adds for Renovate's defaults, are written for many repositories; they are only counted, in an `info` log record.

**Evaluation.**

1. The policy of a dep starts as `{pin: <top-level>, allowedUpdateTypes: all five types (all but `major` for travis deps, as in Renovate: `node_js` lists are test matrices), schedule: <top-level>, minimumReleaseAge: <top-level>, bucket flags: <top-level>}`. The commit type starts as `fix` for production deps (gomod `direct`, npm, cargo, poetry and bundler `dependencies`, pep621 `project.dependencies`, pipenv `packages`, elm `direct` and `dependencies`, julia `deps`, pub `dependencies`, mix `prod`, gleam `dependencies`) and `chore` otherwise, and the scope as `deps`.
2. **Default ignores.** Dep types `indirect` (Go, pip_requirements' transitive pins of compiled requirements files, which still get security updates, and Elm applications' indirect dependencies), `test-indirect` (Elm applications' indirect test dependencies), `go` (the go.mod `go` directive), `peerDependencies` and `engines` (npm), `submodule` (git-submodules, disabled by default as in Renovate), `task` (azure-pipelines tasks: Renovate disables the manager, since a public task version may not be installed in the organization yet) and `tool.uv.constraint-dependencies`, `tool.uv.override-dependencies`, `tool.uv.build-constraint-dependencies` (pep621: uv's lists usually cap versions on purpose; Renovate does not extract them) are ignored unless a rule sets `ignore: false`. The skip reason is `ignored by default (<type>; enable with a rule setting ignore: false)`.
3. **Built-in monorepo groups** set `group: <name> monorepo`, the Ruby version deps `group: ruby` and the vcpkg deps `group: vcpkg` (unless [`builtins.monorepos: false`](#builtinsmonorepos)).
4. **Pre-policy.** Rules *without* `updateTypes` are evaluated top to bottom. Together they decide `ignore`, `pin`, `versioning`, and whether the dep is looked up at all. An ignored dep is skipped as `ignored by rule #<i>`. When a [`currentAge`](#rulescurrentage) rule can match, this waits for the release time of the current version.
5. **Per-type policy.** Once a candidate's type is known, *all* rules are evaluated again in file order, and rules with `updateTypes` participate when they list that type. The candidate is accepted when that policy does not ignore it, its type is in `allowedUpdateTypes`, and it satisfies `allow`. Otherwise the next candidate of the bucket is tried, so ignoring indirect majors keeps their minor and patch updates. Rejections read `ignored by rule #<i> for <type>`, `update type <type> not allowed` or `outside allow <allow>`.
6. **Group settings.** When the resulting policy (pre-policy or per-type) has a `group` with an entry in [`groups`](#groups), the keys the entry sets override it last, after every rule.
7. The chosen update takes `group`, `schedule`, `commitType`, `commitScope`, `prTitle`, `prHeader`, `prFooter`, `automerge`, `automergeStrategy` and `postUpgradeTasks` from the policy of its type. Security updates take only `securityGroup`, the commit scope, the PR templates, the automerge settings and `postUpgradeTasks`.

`updateTypes: [patch]` together with `ignore: false` opts a default-ignored or ignored dep in for those types only. `deps explain <name>` prints the matching rules, each as `match: <fields>; apply: <fields>`, and the resulting policy of a dep.

```yaml
rules:
  - managers: [gomod]              # opt indirect requirements in
    depTypes: [indirect]
    ignore: false
  - managers: [gomod]              # but never their majors
    depTypes: [indirect]
    updateTypes: [major]
    ignore: true
  - managers: [github-actions]     # minor+patch in one PR, majors apart
    updateTypes: [minor, patch]
    group: GitHub Actions
```

## Rule.managers

Manager names as in [managers.md](managers.md); `custom` matches the deps of every `custom[]` entry. With exactly one manager listed, the JSON Schema offers only that manager's dep types for `depTypes`.

## Rule.names

Renovate's `!pattern` works: `['actions/*', '!actions/cache']` matches every `actions/` dep but `actions/cache`, and a list of only exclusions (`['!k8s.io/**']`) matches every other name. Exclusions win regardless of their position. `paths` works the same on the dep's file. A bare `!` is a config error (`rules[i].names[j]: invalid glob "!": missing pattern after !`).

## Rule.baseBranches

Renovate's `matchBaseBranches`. The base branch is the one `deps run` resolves: each of [`baseBranches`](#basebranches), the `--base` branch, else the default branch. Entries are exact names or RE2 regexes `/<RE2>/` (flag `i` allowed); a leading `!` excludes (`'!main'`, `'!/^release\//'`), exclusions win regardless of their position, and a list of only exclusions matches every other base. `explain --base <branch>` and `preview --base <branch>` (default: the pull request's target from the CI environment) resolve that base; `plan`, `apply`, and `explain` and `preview` without one resolve without a base branch, so a rule with `baseBranches` matches nothing there.

## Rule.depTypes

[managers.md](managers.md) lists the dep types of each manager.

## Rule.currentVersion

Renovate's `matchCurrentVersion`. The current version is the locked one when a lockfile has it (npm, cargo, Python, bundler, Julia manifests, mix.lock, Gleam manifests, terraform locks), else the version as written (for a range its bare version: `1.2.3` of `^1.2.3`).

- A **constraint** (`< 2`, `>= 1.20, < 1.22`) is checked against the Masterminds conversion of the dep's scheme, after any [`versioning`](#rulesversioning) rule. A version that cannot be converted (`latest`) does not match.
- A **regex** (`/^v1\./`, `!/-rc/`, `/^V1/i`) matches the version as written, as in [`allow`](#rulesallow).
- It cannot be combined with `versioning` in the same rule. Invalid values are errors as for `allow`: `rules[i].currentVersion: invalid constraint "<v>": …`.

## Rule.currentAge

Renovate's `matchCurrentAge`: `> 1 year` matches deps whose current version (the locked one when known) was released more than a year ago, `< 30 days` those released within the last 30 days. Operators are `<`, `<=`, `>` and `>=`; ages take the units of [`minimumReleaseAge`](#minimumreleaseage) plus `month`/`months` (30 days) and `year`/`years`/`y` (365 days).

- The release time comes from the datasource (see the release times of each datasource in [managers.md](managers.md)). It is looked up once per dep and only when the other selectors of a currentAge rule (`updateTypes` aside) match it; other deps cost nothing.
- An unknown time (a datasource without times, or a failed lookup) matches no currentAge rule. `deps explain` shows the lookup in its `age` line: `released <date>`, or `unknown (currentAge rules do not match)`.
- Deps whose datasource has release times wait for the lookup before their pre-policy skip (so `currentAge` with `ignore: false` can opt default-ignored deps in). With `updateTypes` the rule applies per candidate as usual.
- It cannot be combined with `versioning` or `extractVersion` in the same rule: the release time is looked up after both are decided. Errors: `rules[i].currentAge: invalid age range "<v>": want an operator <, <=, > or >= and an age, …`.

```yaml
rules:
  - currentAge: '> 1 year'          # deps left behind for a year: one PR
    group: stale dependencies
  - names: ['k8s.io/**', '!k8s.io/klog/**']
    currentVersion: '< 0.30'
    updateTypes: [major]
    ignore: true
```

## Rule.datasources

Renovate names are normalized: `golang-version` = `go-toolchain`, `github-tags` = `git-tags` (so it also matches actions), `github-release-attachments` = `github-releases`, `node` = `node-version`, `bitbucket-tags` = `git-tags` (Bitbucket pipes), `azure-tags` and `bitbucket-server-tags` = `git-tags` (tags via `git ls-remote`, which works on every forge), `github-digest` = `git-refs` (branch and tag commits). `custom.<name>` must be defined in [`customDatasources`](#customdatasources).

## Rule.updateTypes

Must not be empty. A rule with `updateTypes` is skipped in the pre-policy pass and applies per candidate (see [evaluation](#rules)); it cannot set `pin` or `versioning`.

## Rule.ignore

- `true`: do not update; skipped as `ignored by rule #<i>`.
- `false`: opt in a default-ignored dep type.
- With `updateTypes`: reject (or allow) updates of those types only.

## Rule.pin

See the top-level [`pin`](#pin). Applies to github-actions and docker deps; not allowed with `updateTypes`.

## Rule.allow

- A **constraint** (`< 7`, `~1.2`, `>= 1.20, < 2`) is checked against the scheme's Masterminds conversion of the candidate. Versions that cannot be represented are rejected.
- A **regex** (`/^\d+\.\d+$/`, `!/-rc/`, `/^v1\./i`) matches the candidate *as written* (tag, image tag, module version) and never converts it. Only flag `i` exists.
- Invalid values are config errors: `rules[i].allow: invalid constraint "<v>": …`, `invalid regex "/(/": …`, `unknown flags "g" (only i)`.

## Rule.allowedUpdateTypes

When no rule sets it, all five update types are allowed, except `major` for [travis](managers.md#travis) deps (Renovate disables their major updates: a `node_js` list is a test matrix); a rule such as `{managers: [travis], allowedUpdateTypes: [major, minor, patch]}` allows them. A later matching rule replaces the list; `[]` allows none (security updates still apply).

## Rule.versioning

The scheme validates the current value, ranks candidates, classifies update kinds, converts versions for `allow`, and computes security fixes. A current value that the new scheme cannot parse is skipped as `non-version value "<v>"`.

| Scheme | Accepts | Notes |
|---|---|---|
| `tag` | `^(v?)\d+(\.\d+)?(\.\d+)?$` | candidates need the same prefix and the same number of parts (`v4` → `v5`, `v4.1.0` → `v4.2.0`); no suffixes or prereleases |
| `go` | Go module semver | no pseudo-versions; prereleases only from a prerelease; `+incompatible` only from `+incompatible`; same major except v0 → v1 |
| `docker` | `^(v?)(\d+(\.\d+)*)(.*)$` | same prefix, part count and suffix (`1.27-alpine` → `1.28-alpine`); `latest` and commit hashes (7–40 lowercase hex digits, not all decimal: `3a22eba`) are invalid |
| `semver` | Masterminds loose semver | any precision; prereleases only from a prerelease; keeps the `v` style; the default of `custom[]` and npm |
| `loose` | 1–6 numeric parts, optional `v`, optional `-`/`_` suffix | the suffix counts as a prerelease. Missing parts compare as 0 (`1877` < `1877.25`); then fewer parts sort below more (`1.2` < `1.2.0`). Any precision is a candidate, so combine it with an `allow` regex. |
| `cargo` | `MAJOR[.MINOR[.PATCH[-pre][+build]]]`, no `v` | candidates need the same number of parts (`0.4` → `0.5`); prereleases only from a prerelease. Update types follow Cargo's compatibility: leaving the range of the leftmost non-zero part is `major` (`0.4` → `0.5`, `0.0.3` → `0.0.4`), a minor change of a `1.x` version `minor`, anything else `patch`. The default of cargo deps |
| `pep440` | PEP 440 (epochs, pre/post/dev releases, local versions; at most 10 release parts) | ordered like Python's `packaging`; pre and dev releases only from a pre or dev release; local versions only from a local version. A candidate may have more release parts than the current version only if the extra parts are 0, and is then written at the current precision (`4.2` → `5.0` from `5.0.0`, never `5.0.1`). The default of the Python managers |
| `hashicorp` | 1–3 numeric parts, optional `v`, `-prerelease` on full versions | Terraform constraints: candidates are written at the current precision and `v` style (`5.0` → `5.31` from `5.31.2`, `1.5.7` → `1.9.5` from the tag `v1.9.5`); prereleases only from a prerelease; the default of the terraform managers |
| `maven` | Maven `ComparableVersion`: a leading digit, then `[0-9A-Za-z._-]` | `1.0` = `1.0.0` = `1.0-final`; `alpha` < `beta` < `milestone` < `rc` < `snapshot` < release < `sp`. SNAPSHOTs are never candidates, other prereleases only from a prerelease; the first unknown qualifier after a `-` must match (`-jre` stays `-jre`; `.v20230217` dates and hashes are not compared). The default of the maven, gradle and gradle-wrapper managers. |
| `ruby` | RubyGems `Gem::Version`: a leading digit, then dot-separated `[0-9A-Za-z]` segments, optional `-<prerelease>` | segments compare numerically, a letter segment marks a prerelease below its release (`1.0.0.rc1` < `1.0.0`; `1.0.0-beta` = `1.0.0.pre.beta`); trailing zeros do not count (`1.2` = `1.2.0`). Update types follow the first three segments; prereleases only from a prerelease. Ranges are Gem::Requirement lists (`~> 7.1, >= 7.1.2`). The default of the bundler manager |
| `conda` | conda's VersionOrder: `[<epoch>!]<version>[+<local>]`, segments separated by `.` or `_`, each numbers and words | a word sorts below a number (`1.1.1w` < `1.1.1`, `1.0rc1` < `1.0`), `dev` below every word, `post` above every number; trailing zeros do not count. `dev`, `rc`, `alpha`, `beta`, `pre`, `preview` and `a`/`b`/`c` followed by a number mark prereleases, proposed only from a prerelease; other words are part of the release (`2024a`, `9e`). The first segment is major, the second minor. Precision as for `pep440`. Ranges are conda version specs (`=1.26`, `1.26.*`, `>=1.26,<2`, `~=1.26.4`). The default of the conda manager (Renovate's and Dependabot's `conda` ordering) |
| `julia` | Julia `VersionNumber`: 1–3 numeric parts, `-<prerelease>` and `+<build>` after three | ordered as Julia does, builds too (`1.2.11+1` > `1.2.11+0` > `1.2.11`, the JLL rebuilds); update types follow Pkg's caret: the leftmost non-zero part is the major (`0.21` → `0.22` is major). Prereleases only from a prerelease. Ranges are Pkg compat entries (`0.21, 1`, `~1.2`, `=0.3.1`, `>= 1`, `< 2`, `1.2 - 2`). The default of the julia manager |
| `bazel-module` | Bazel module versions: `RELEASE[-PRERELEASE][+BUILD]`, dot-separated `[0-9A-Za-z]` identifiers (`-` also allowed after the first `-`) | ordered as Bazel's bzlmod `Version`: digit-only identifiers numerically and before the others (`1.2.3` < `1.2.3.1` < `1.2.3.bcr.1` < `1.2.4`), a shorter release first, a prerelease below its release, build metadata ignored. Update types follow the first three release identifiers (`.bcr.N` registry patches are patch updates); prereleases only from a prerelease. No ranges. The default of the bazel-module manager's `bazel_dep`s |
| `nuget` | NuGetVersion: 1–4 numeric parts, optional `-<prerelease>` labels, optional `+metadata` | missing parts are 0 (`1.2` = `1.2.0.0`); prereleases compare as SemVer 2.0, case-insensitively; metadata does not count. Update types follow the first two parts; prereleases only from a prerelease. Ranges are NuGet version ranges (`[1.0,2.0)`, `(,2.0]`, `[1.2.3]`) and floating versions (`1.*`, `1.0.0-rc*`). The default of the nuget manager |
| `rpm` | RPM EVR: `[<epoch>:]<version>[-<release>]`, version and release of `[0-9A-Za-z._+~^]`, the version starting with a digit | ordered as RPM's `rpmvercmp`: epoch, then version, then release; runs of digits compare numerically and above letters, `~` sorts below everything (`1.0~rc1` < `1.0`), `^` above the end but below more parts (`1.0` < `1.0^git1` < `1.0.1`); a version without a release sorts below the same version with one. A `~` marks a prerelease, proposed only from a prerelease. The first number of the version (and the epoch) is major, the second minor, anything else (the release: `1-1.azl3` → `1-2.azl3`) patch. A current version without an epoch takes the epoch the repository lists for its version and release, so an epoch bump that lowered the version (`1.5.10-34.fc41` → `1:0.8.0-1.fc43`) is newer, as dnf orders it; when the repository does not list it, epochs are ignored (Renovate's order). Candidates are written in the current version's form: without the epoch when it has none. No ranges. The default of rpm deps (Renovate's `rpm` versioning) |
| `apk` | Alpine package versions (apk-tools): `N[.N]...[a-z][_suffix[N]]...[~hash][-rN]` (`3.2.1-r0`, `2.39.0_rc1-r0`, `6.5_p20250503-r0`, `1.1.1w-r1`) | ordered as apk-tools does: numbers, then the letter, then suffixes (`_alpha` < `_beta` < `_pre` < `_rc` < none < `_cvs` < `_svn` < `_git` < `_hg` < `_p`), then the revision; the commit hash is ignored. Update types follow the first two numbers, everything after them (letter, suffixes, `-rN` revision) is a patch update; pre-releases only from a pre-release; a version with `-rN` only moves to versions with one and vice versa. No constraints (`=~`, `>=`). The default of apk deps (Renovate's `apk` versioning) |
| `deb` | Debian package versions (deb-version(7)): `[epoch:]upstream_version[-debian_revision]`, the upstream version starting with a digit | ordered as `dpkg --compare-versions`: epoch first, then upstream version and revision, digit runs numerically and `~` below everything (`1.0~rc1` < `1.0` < `1.0+deb12u1`). Update types follow an epoch change (major) and the first three numbers after the epoch, revision included (`8.14.1-1` → `8.14.1-2` is patch); every valid version is a candidate. No ranges. The default of `deb` deps |
| `hex` | SemVer 2.0 as Elixir's `Version` parses it: `MAJOR.MINOR.PATCH`, optional `-<prerelease>` and `+<build>`; no `v` prefix, no partial versions | ordered as SemVer 2.0 (the build is ignored); update types follow the three parts; prereleases only from a prerelease. Ranges are Elixir requirements (`~> 1.7`, `>= 1.0.0 and < 2.0.0`, `~> 1.0 or ~> 2.0`, `== 1.2.3`); a prerelease only satisfies an alternative naming one, as Hex resolves. The default of the mix and gleam managers |
| `nixpkgs` | branches of NixOS/nixpkgs, as Renovate's `nixpkgs`: `^(nixos\|nixpkgs\|release)-(YY.MM\|unstable)(-(small\|aarch64\|darwin))?$` (`nixos-24.05`, `nixpkgs-24.05-darwin`, `release-24.11`, `nixos-unstable`) | candidates need the same prefix and suffix; releases ordered by year and month, `unstable` above every release but never proposed from one. A new year is `major` (`nixos-24.11` → `nixos-25.05`), a new month `minor`. A `git-refs` dep under it gets the repository's branches as candidates instead of its tags. No ranges. The default of the nix manager's NixOS/nixpkgs branches and release channels |
| `glasskube` | Glasskube package versions: SemVer with an optional `v` and the package revision as the first build identifier (`v1.17.0+2`) | ordered as Renovate's: major, minor, patch, then the revision (absent = 0: `v1.17.0` < `v1.17.0+1` < `v1.17.0+2` < `v1.17.1+1`), then a prerelease below none; a new revision is a patch update; prereleases only from a prerelease. No ranges. The default of the glasskube manager and of `glasskube-packages` deps |
| `regex:<RE2>` | the expression's named groups | `major` (required), `minor`, `patch`, `build`, `revision` (numeric, absent = 0), `prerelease`, `compatibility`; other names are errors. `(?<name>)` and `(?P<name>)` both work. Candidates must share `compatibility`. |

Errors: `rules[i].versioning: regex: must have a (?<major>...) group`, `regex: unknown group "x" (allowed: ...)`, `regex: invalid RE2 regexp: ...`, and `cannot be combined with updateTypes: ...`.

```yaml
rules:
  - names: ['ghcr.io/gardenlinux/gardenlinux/fips']
    versioning: loose
    allow: '/^\d+\.\d+$/'
  - names: ['open-component-model/cosign']
    versioning: 'regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$'
```

## Rule.group

- **Groups.** All non-security updates whose policy has the same `group` (and the same expanded [`additionalBranchPrefix`](#additionalbranchprefix)) share a branch `<prefix><additionalBranchPrefix>group/<slug(group)>` and one PR. The title is `<type>(<scope>): update <group> (<n> dependencies)`, or `update <group> to <v>` when every member moves to one version.
- **Single deps.** Every other update gets the branch `<prefix><additionalBranchPrefix><slug(name)>`, shared by the updates of that dependency name in every manager and file, with titles `<p> update <name> to <ver>`, `<p> pin <name> to <sha7>` and `<p> update <name> digest to <sha7>`. [`prTitle`](#prtitle) replaces these built-in titles.
- `slug` lowercases the name, maps bytes outside `[a-z0-9._-]` to `-`, and collapses and trims `-`.
- **Commit type in groups.** A group uses `fix` if any member's type is `fix`, else the first member's type. It always uses the first member's scope and [PR templates](#prtitle).
- Built-in [monorepo groups](#builtinsmonorepos) are overridden by any rule that sets `group`.
- Security updates are not members (except of a shared version definition, which cannot be split); [`securityGroup`](#rulessecuritygroup) groups them apart.
- **Settings** shared by the group's PR (schedule, labels, automerge, …) are written once in [`groups`](#groups) instead of on every rule setting the group.

## Rule.securityGroup

Dependabot's `groups.<name>.applies-to: security-updates`, Renovate's `vulnerabilityAlerts` with `groupName`. Security updates are never put into a [`group`](#rulesgroup); with `securityGroup`, the security updates of matching deps whose policy has the same `securityGroup` share one PR instead of one each.

- **Branch** `<prefix>group/<slug(securityGroup)>[-<bucket>]-security`, so it never collides with the non-security group of the same name: `group: go` and `securityGroup: go` give `deps/group/go` and `deps/group/go-security`.
- **Title** `fix(<scope>): update <securityGroup> (<n> dependencies) [security]`, or `update <securityGroup> to <v> [security]` when every member moves to one version; a single dep keeps its own title (`fix(deps): update <name> to <v> [security]`). The type is always `fix`; the scope is the first member's.
- **Members** are only security updates. Non-security updates of the same deps stay in their `group` or their own PR. The body's Security section lists the advisories of every member.
- Like the commit scope it comes from the policy of the update's type, so a rule with `updateTypes` can set it. Schedules still do not apply. Updates of a shared version definition (Maven property, Gradle version entry) stay in the definition's group.

```yaml
rules:
  - securityGroup: all           # security updates share deps/group/all-security (majors: all-major-security)...
  - managers: [gomod]            # ...except Go's: later rules win
    group: go
    securityGroup: go            # deps/group/go-security, apart from deps/group/go
```

## Rule.commitScope

Must match `^[A-Za-z0-9][A-Za-z0-9._/-]*$`. In groups the first member's scope is used.

## Rule.separate

See the bucket table under [`separate`](#separate).

## Rule.additionalBranchPrefix

See [`additionalBranchPrefix`](#additionalbranchprefix).

## Config.groups

The settings of a group's PR, written once instead of on every rule that sets the group (as Dependabot's `groups.<name>`; Renovate repeats them in every `packageRules` entry with the `groupName`).

```yaml
rules:
  - managers: [gomod]
    names: ['k8s.io/**', 'sigs.k8s.io/**']
    group: k8s
  - managers: [custom]
    names: [envtest]
    group: k8s
groups:
  k8s:
    schedule: ['0 0 * * 0']
    labels: [kubernetes]
  kubernetes monorepo:            # a built-in group
    automerge: true
```

- **Which updates.** An entry applies to every policy whose group is its name: the `group` of the last matching rule that sets one ([`rules[].group`](#rulesgroup)), else a built-in group ([monorepo groups](#builtinsmonorepos) `<name> monorepo`, `ruby`, `vcpkg`). A rule with `updateTypes` that sets `group` puts only the updates of those types into the group, so only they get its settings.
- **Precedence: top level < rules < groups.** The keys an entry sets override the top level and every matching rule, also rules written after the one that set the group: with a rule `{group: k8s, automerge: false}` and `groups: {k8s: {automerge: true}}` the group's updates automerge. To vary a key inside a group (per update type, say), set it in rules and leave it out of the entry.
- **Keys** are the settings rules take apart from the pre-policy ones: `schedule`, `additionalBranchPrefix`, the release age keys, `separate`, the `automerge*` keys, `draftPR`, `prCreation`, `labels`, `reviewers`, `assignees`, the PR templates and `dashboardApproval`, with the values and errors of the top-level keys. `pin` and `rangeStrategy` are decided before the group is known and are errors (`groups.<name>.pin: cannot be set for a group: …`); match fields and the other rule keys (`ignore`, `allow`, `group`, `addLabels`, …) are unknown keys.
- **Security updates** are no members of the group's PR, but take the policy of their type like any update, group settings included, apart from `schedule`, `group` and `dashboardApproval` (see [`securityGroup`](#rulessecuritygroup)); a `securityGroup` name selects no entry.
- **Warnings.** An entry whose name no rule sets as `group` and no built-in group has (none with [`builtins.monorepos: false`](#builtinsmonorepos)) is a config warning: `warn    .deps.yaml: groups.k8: no rule sets group "k8", so its settings apply to nothing`.
- **extends** merges `groups` by name: an entry replaces the extended files' entry of the same name as a whole.
- `deps explain` prints the applied entry on a `group` line with its keys and file: `group     k8s settings: schedule=[0 0 * * 0] labels=[kubernetes] (from .deps.yaml:13)`.
- `deps migrate` and `--renovate` move a setting into `groups` when every rule (at least two) with that `groupName` sets it to the same value and moving it changes no policy.

## Config.custom

Each entry is one manager instance with dep types `annotation` (no `match` or `jsonata`), `regex` (`match` set) or `jsonata` (`jsonata` set). Datasources: `git-tags`, `github-releases`, `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases`, `forgejo-tags`, `go`, `go-toolchain`, `docker`, `npm`, `node-version`, `crate`, `rpm`, `apk`, `deb`, `artifactory`, the Renovate aliases `golang-version`, `github-tags`, `github-release-attachments`, `node`, `bitbucket-tags`, `azure-tags`, `bitbucket-server-tags` (`git-tags`; the name is the repository URL) and `github-digest` (`git-refs`: `owner/repo` on github.com or a repository URL; a version that is no version is a branch or tag whose commit, the `digest`, is kept current), and `custom.<name>`.

**Annotation mode (no `match`).** A comment marker annotates the **next line**, and that line's first version token (`v?N.N[.N…][-suffix]`, not glued to other word characters) is the version. Comment starters are `#`, `//`, `--`, `;` and `<!--`.

- `deps: <datasource> <name> [versioning=<scheme>]`; `helm` and `deb` also take `deps: <datasource> <registry> <name>`.
- `renovate: datasource=<ds> depName=<name> [packageName=<p>] [versioning=<v>] [registryUrl=<url>]`. Fields can come in any order. `packageName` wins over `depName`; `registryUrl` is the registry of `helm`, `gitlab-packages`, `rpm`, `apk`, `deb` and `artifactory` deps, the API base URL of `endoflife-date` deps and the instance of the `gitlab-*`, `gitea-*` and `forgejo-*` releases and tags datasources (default `https://gitlab.com`, `https://gitea.com`, `https://code.forgejo.org`; see [`custom[].registryUrl`](#customregistryurl)) and ignored for other datasources, as are unknown keys.
- With the docker datasource, the next line's first tagged image reference is used: the tag is the version, and an adjacent `@sha256:` digest is rewritten with it.
- With the deb datasource, the next line's first Debian version is used (`curl=8.14.1-2`, `1:2.39.5-0+deb12u2`, `3.0.15-1~deb12u1`: epoch and `~`/`+` suffixes included).
- A marker's `versioning=` takes a scheme name (not `regex:`). Without it, docker deps use `docker` versioning, others the entry's `versioning`, else their datasource's default (see [`custom[].versioning`](#customversioning)).
- Skips: `no version on next line`, `renovate marker without datasource`, `renovate marker without depName`, `invalid deb name "<name>"`, `invalid deb registry URL "<url>": <why>`, `invalid artifactory name "<name>"`.

```yaml
custom:
  - files: ['Dockerfile', '**/*.mk', '.env']
```

```dockerfile
# deps: github-releases golangci/golangci-lint
ARG GOLANGCI_VERSION=v2.5.0
# renovate: datasource=go depName=golang.org/x/tools
RUN go install golang.org/x/tools/cmd/goimports@v0.38.0
```

**Regex mode (`match` set)**: see [`custom[].match`](#custommatch). **JSONata mode (`jsonata` set)**: see [`custom[].jsonata`](#customjsonata).

## Custom.files

Must not be empty.

## Custom.match

`match` runs over the whole file, and each match is one dep.

| Group | Aliases | Meaning |
|---|---|---|
| `version` | `currentValue` | required; the replaced version |
| `name` | `depName`, `packageName` (wins over `depName`) | overrides `name` |
| `datasource` | | overrides `datasource` |
| `versioning` | | scheme name |
| `registryUrl` | | overrides `registryUrl` |
| `digest` | `currentDigest` | replaced together with the version |
| `checksum` | | requires `checksum:` (and vice versa) |

Empty groups skip the match as `empty version`, `empty name` or `empty datasource`. A checksum that is not 64 or 128 hex digits is skipped as `checksum is not 64 or 128 hex digits`.

```yaml
custom:
  - files: ['Makefile']
    match: 'GOLANGCI_VERSION \?= (?P<version>v\S+)'
    datasource: github-releases
    name: golangci/golangci-lint
```

## Custom.registryUrl

Renovate's `registryUrlTemplate` (without templates) and `registryUrl` group or marker field: the registry the deps of `helm`, `gitlab-packages`, `rpm`, `apk`, `deb`, `artifactory`, `gitlab-releases`, `gitlab-tags`, `gitea-releases`, `gitea-tags`, `forgejo-releases`, `forgejo-tags`, `endoflife-date` and `glasskube-packages` are looked up in. It is the default of the entry's deps; a `(?P<registryUrl>…)` group, a marker's `registryUrl=` or a JSONata result's `registryUrl` overrides it. Other datasources ignore it. For `rpm` it is the repository's `repodata/` directory (or its root); several whitespace-separated URLs are merged, e.g. a distribution's base and updates repositories:

```yaml
custom:
  - files: ['images/**/packages.txt']
    match: '(?m)^(?P<name>[a-z0-9._+-]+)=(?P<version>\S+)$'
    datasource: rpm   # versioning defaults to rpm
    registryUrl: >-
      https://packages.microsoft.com/azurelinux/3.0/prod/base/x86_64/repodata/
      https://packages.microsoft.com/azurelinux/3.0/prod/ms-oss/x86_64/repodata/
```

For `apk` it is Renovate's `<base>?arch=<arch>[&branch=<b>][&components=<c1>,<c2>]` (one `APKINDEX.tar.gz` per component, e.g. `https://dl-cdn.alpinelinux.org/alpine?branch=v3.22&components=main,community&arch=x86_64`), or an index URL or its directory (`https://packages.wolfi.dev/os/x86_64`).

A deb registry is Renovate's `<repository>?suite=<suite>&components=<c1,c2>&binaryArch=<arch>` (see the [deb datasource](managers.md#datasources)); several, separated by whitespace, are merged. Without one, deb deps use `https://deb.debian.org/debian?suite=stable&components=main,contrib,non-free&binaryArch=amd64`.

```yaml
custom:
  - files: ['Dockerfile']
    registryUrl: https://deb.debian.org/debian?suite=bookworm&components=main&binaryArch=amd64
```

```dockerfile
FROM debian:bookworm
# renovate: datasource=deb depName=curl
RUN apt-get install -y curl=7.88.1-10+deb12u12
```

For `artifactory` it is the Artifactory repository URL the dep's name (a folder path) lies below; several, separated by whitespace, are listed and merged. Without one, the name is the full `https://` folder URL.

```yaml
custom:
  - files: ['tools.env']
    match: 'CLI_VERSION=(?P<version>\S+)'
    datasource: artifactory
    name: tools/cli
    registryUrl: https://example.jfrog.io/artifactory/generic-local
```

For the `gitlab-*`, `gitea-*` and `forgejo-*` releases and tags datasources it is the forge instance (default `https://gitlab.com`, `https://gitea.com` and `https://code.forgejo.org`).

## Custom.jsonata

Renovate's `customType: jsonata` managers. The file is parsed as [`fileFormat`](#customfileformat) and the [JSONata](https://docs.jsonata.org/overview.html) query runs over it (a full JSONata 2.x implementation, so queries written for Renovate work unchanged). The query returns one dep object or a list of them; each object is one dep:

| Field | Aliases | Meaning |
|---|---|---|
| `currentValue` | `version` | required; the replaced version |
| `depName` | `packageName` (wins), `name` | overrides `name` |
| `datasource` | | overrides `datasource` |
| `versioning` | | scheme name |
| `currentDigest` | `digest` | replaced together with the version |
| `extractVersion` | | as a Renovate marker's `extractVersion=` |
| `registryUrl` | | overrides `registryUrl` |

Other fields (`depType`, `indentation`) are ignored. Fields must be strings: a number skips the dep as `not a string: <field>`, and empty ones as `empty version`, `empty name` or `empty datasource`. A query that returns nothing yields no deps; a query error or a result that is no object is a file error. Each evaluation stops after 10 seconds.

The results carry no byte positions, so deps finds each version where the file spells it: every occurrence of the value gets a distinct numeric marker appended, the marked file is queried again, and the occurrence whose marker comes back on that result is the one replaced. So equal versions of different deps each get their own bytes, and values the query cuts out of a longer string (`$split(image, ":")[1]`, `$substringAfter`) are found too. A version the file does not spell (built by the query, or escaped) is skipped as `version not found in file`.

```yaml
custom:
  - files: [package.json]
    fileFormat: json
    jsonata: '$each(dependencies, function($v, $k) { {"depName": $k, "currentValue": $v} })'
    datasource: npm
  - files: ['**/tools.yaml']
    fileFormat: yaml
    jsonata: 'tools.{"depName": repo, "currentValue": version, "datasource": "github-releases"}'
```

## Custom.fileFormat

`json` (comments and trailing commas allowed, as Renovate's JSONC parser), `yaml` or `toml`. A YAML file is the list of its documents, as in Renovate, so `packages.{…}` maps over every document; `$[0]` is the first one. Keys keep their file order, so `$each`, `$keys` and `*` list deps in file order. YAML timestamps are strings; TOML datetimes are strings, its integers, floats and booleans keep their type.

## Custom.versioning

Accepts the schemes of [`rules[].versioning`](#rulesversioning). Without it, each dep uses its datasource's default: `rpm` for rpm deps, `apk` for apk deps, `deb` for deb deps, `hex` for hex deps, `glasskube` for glasskube-packages deps, else `semver`. A marker's `versioning=`, a `versioning` group or a JSONata result's `versioning` overrides the entry's.

## Custom.checksum

The regex entry keeps a sha256/sha512 checksum next to the version. This needs a `(?P<checksum>…)` group plus a `checksum:` block with exactly one source:

- `asset` (github-releases only): the release asset's API `digest`. This costs no extra request.
- `url`: a document downloaded once per URL (1 MiB cap). With `file`, the matching `<hex>  <name>` / `<hex> *<name>` line is used, else the first hex token in the document.

The only template variable is `{{version}}`, the new version as written into the file. Others are errors (`unknown template variable {{x}} (only {{version}})`). The new checksum must have the same length as the current one and keeps its letter case. If fetching it fails, the dep is not updated, so version and checksum always change together.

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

One version with a checksum per platform takes one entry per checksum, each matching the same version. Entries with the same datasource and name share it: the version is written once, every checksum moves with it, and the updates form one PR. If one checksum cannot be fetched, none of them moves (`1 of 2 deps of goreleaser/nfpm in .env resolved to 2.48.0, the others did not; updated none`). See *Shared versions* under the [custom manager](managers.md#custom).

```yaml
custom:
  - files: [.env]
    match: 'NFPM_VERSION=(?P<version>\S+)\n(?:.*\n)*?NFPM_SHA256_LINUX_X86_64=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: goreleaser/nfpm
    checksum: {asset: 'nfpm_{{version}}_Linux_x86_64.tar.gz'}
  - files: [.env]
    match: 'NFPM_VERSION=(?P<version>\S+)\n(?:.*\n)*?NFPM_SHA256_LINUX_ARM64=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: goreleaser/nfpm
    checksum: {asset: 'nfpm_{{version}}_Linux_arm64.tar.gz'}
```

## Config.customDatasources

Names must match `^[A-Za-z0-9][A-Za-z0-9._-]*$`. The document is fetched once per run (unauthenticated, at most 10 MiB) and shared by every dep. It has no release times (so with a `minimumReleaseAge` its versions pass under `minimumReleaseAgeBehaviour: timestamp-optional` and are held back under `timestamp-required`) and no digests. Errors read `custom.<name>: GET <url>: HTTP 404` or `custom.<name>: <url>: selector ".a[]": cannot iterate over null`; an empty result is `no versions`.

```yaml
customDatasources:
  envtest:
    url: https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/master/envtest-releases.yaml
    format: yaml
    versions: .releases | keys
    versionRegex: '^v?(?P<version>\d+\.\d+\.\d+)$'
custom:
  - files: ['.env']
    match: 'ENVTEST_K8S_VERSION=(?P<version>\d+\.\d+\.\d+)'
    datasource: custom.envtest
    name: envtest
```

## CustomDatasource.url

Renovate's `defaultRegistryUrlTemplate`.

## CustomDatasource.versions

Not allowed with `format: plain` (`not allowed with format plain`). **Selector grammar** (a jq subset):

- A selector is a sequence of stages joined by `|`.
- A stage is either `keys` (the sorted keys of an object) or a path of steps.
- Steps:
  - `.` (identity);
  - `.field`, where field is `[A-Za-z_][A-Za-z0-9_-]*`;
  - `[]` or `.[]` (array elements or object values). A path may also start with `[]`.
- As in jq:
  - a missing field, or a field of null, is null; nulls are dropped;
  - iterating null, or indexing or iterating the wrong type, is an error.

Examples: `.releases[].version`, `.releases | keys`, `[].tag_name`.

## Config.lockFileMaintenance

Renovate's `lockFileMaintenance`. With `enabled: true`, `run` keeps one PR on the branch `<branchPrefix>lock-file-maintenance`, titled `chore(deps): lock file maintenance`, that refreshes every lockfile deps maintains:

- Supported lockfiles, each refreshed on a temporary copy so that every locked dependency, transitive ones included, moves to the newest version its manifest admits (see each manager's "Lock file maintenance" section):
  - cargo: every `Cargo.lock` of a scanned `Cargo.toml` (the workspace's): `cargo update`.
  - npm: the `npm-shrinkwrap.json`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock` or `bun.lock` of every lock root (a workspace and its members share one), regenerated from scratch as Renovate does: the lockfile is removed (`yarn.lock` emptied) and the usual install runs (`npm install --package-lock-only`, `pnpm install --lockfile-only`, `yarn install`, `bun install --lockfile-only`).
  - pep621, poetry, pipenv: `uv.lock` (`uv lock --upgrade`), `pdm.lock` (`pdm update --no-sync --update-all`), `poetry.lock` (`poetry update --lock`) and `Pipfile.lock` (`pipenv lock`), one lockfile per directory (a uv workspace once, through its root).
  - gradle: the lockfiles of every Gradle build that uses dependency locking: `dependencies --write-locks` for every locked project, re-resolving dynamic versions; `gradle/verification-metadata.xml` is regenerated with them.
  - terraform: every `.terraform.lock.hcl` next to a scanned Terraform/OpenTofu configuration: `terraform init -upgrade -backend=false` (`tofu` for OpenTofu), with fresh hashes.
  - bundler: every `Gemfile.lock` next to a scanned `Gemfile`: `bundle lock --update`.
  - composer: every `composer.lock` next to a scanned `composer.json`: `composer update --no-install` (no scripts or plugins, platform requirements ignored as [`composer.ignorePlatformReqs`](#composerignoreplatformreqs) says).
  - julia: every manifest of a scanned `Project.toml` (a workspace's once, through its root): `Pkg.update()`.
  - mix: every mix.lock of a scanned `mix.exs` (an umbrella's once): `mix deps.update --all`.
  - gleam: every `manifest.toml` next to a scanned `gleam.toml`: `gleam deps update`.
  - pub: every `pubspec.lock` of a scanned package or workspace root: `dart pub upgrade`.
  - nix: every `flake.lock` next to a scanned `flake.nix`: `nix flake update`, as Renovate does.
  - deno: every `deno.lock` of a scanned `deno.json`/`deno.jsonc` (a workspace's once, through its root): regenerated with `deno install --lockfile-only`, keeping the `remote` hashes, as Renovate regenerates it.
  - bazel-module: every `MODULE.bazel.lock` next to a scanned `MODULE.bazel`: deleted and regenerated with `bazel mod deps --lockfile_mode=update`, as Renovate does.
  - pip_requirements: every pip-compile or `uv pip compile` output whose header command can be re-run (its inputs exist): the command runs again with `--upgrade`. The compiled file is its own lockfile; the inputs are unchanged.
  - gomod: Go has no lockfile, so the `// indirect` requirements of every `go.mod` with a `go.sum` count as its locked versions: `go get <module>@upgrade` for each (except replaced modules), then `go mod tidy`. The `go`/`toolchain` directives and the direct requirements stay; a module whose upgrade would move them keeps its version.
- The manifests are unchanged, so the PR changes only lockfiles (and, for Go, the `// indirect` requirements of `go.mod`). Directories without a lockfile get none.
- The PR is rebuilt from the base branch on every run in schedule, like other PRs: it is updated when the result changes (new releases), and closed when the lockfiles are already current. The dashboard lists it with the other PRs, and its checkboxes create, recreate or rebase it.
- `minimumReleaseAge` does not apply: the tools pick the newest releases regardless of age. Lockfile-only and other updates of the same lockfiles stay in their own PRs.
- `schedule` (cron triggers and time windows, as the top-level `schedule`) overrides the top-level schedule; `[]` means every run. Renovate's default is a weekly window; `schedule: ["before 4am on monday"]` restores it, given a workflow cron inside the window (e.g. `0 3 * * *`).
- It follows the top-level `automerge` and `automergeStrategy`; rules do not apply to it.
- `plan` and `apply` only list it (`update  Cargo.lock  lock file maintenance  (lockFileMaintenance, separate PR)`); they never write it.

```yaml
lockFileMaintenance:
  enabled: true
  schedule: ["0 3 * * 1"]   # the Monday run
```

## LockFileMaintenance.schedule

Validated like the top-level `schedule` (`lockFileMaintenance.schedule[0]: "60 5 * * *": minute: value 60 out of range 0-59`); its time windows use [`timezone`](#timezone).

## Config.composer

See [`composer.ignorePlatformReqs`](#composerignoreplatformreqs).

## Composer.ignorePlatformReqs

Renovate's `composerIgnorePlatformReqs`: the platform requirements (`php`, `ext-*`, `lib-*`, `composer-plugin-api`) `composer update` ignores when deps updates a `composer.lock` (`apply`, `run`, lock file maintenance; see the [composer manager](managers.md#composer)).

- **Unset** (deps' default): every platform requirement is ignored (`--ignore-platform-reqs`), since the runner's PHP and extensions are not the project's. When composer.json sets `config.platform.php`, which stands in for the PHP version, only `ext-*` and `lib-*` are (`--ignore-platform-req=ext-* --ignore-platform-req=lib-*`).
- **A list:** exactly these are ignored, each passed as `--ignore-platform-req=<req>`; patterns such as `ext-*` work as in Composer. The others are checked against the runner's PHP and extensions (or `config.platform`), and a package version they exclude fails the update.
- **`[]` or `null`:** none is ignored.
- Renovate's default `[]` means `ext-*` and `lib-*` there (with a PHP matching the project's installed by Renovate), and `null` none; `deps migrate` writes `[ext-*, lib-*]` for an explicit `[]` and `[]` for `null`.

```yaml
composer:
  ignorePlatformReqs: [ext-*, lib-*]
```
