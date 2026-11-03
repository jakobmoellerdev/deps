# Managers and datasources

A **manager** finds dependencies in files and edits them. It records byte spans, and an update replaces only those bytes, so files are never reformatted. A **datasource** lists the available versions of a dep.

Every discovered file goes to every manager whose file pattern matches. Discovery always prunes `.git`, `node_modules`, `vendor` and `testdata`, plus anything matched by [`ignorePaths`](configuration.md#ignorepaths).

The tables give the dep types (for `rules[].depTypes`) and skip reasons. Skip reasons are exact strings, shown by `deps plan -v` as `skip    <file>:<line>  <name>  <reason>`.

Engine-level skips apply to all managers:

| Reason | When |
|---|---|
| `ignored by rule #<i>` | a pre-policy rule set `ignore: true` |
| `ignored by default (<type>; enable with a rule setting ignore: false)` | dep types `indirect`, `go`, `peerDependencies`, `engines` |
| `non-version value "<v>"` | the current value is not valid in the dep's versioning scheme |
| `non-version tag` | a docker-datasource dep with a non-version tag (`latest`, `bookworm`), not pinned, and `pin: keep` |

## github-actions

**Files:**
- `(workflow-templates|.github|.gitea|.forgejo)/(workflows|actions)/**/*.y(a)ml`;
- any `action.y(a)ml`.

**Extracted:**
- every scalar `uses:` outside `with:` and `env:`. This covers steps, job-level reusable workflows, `parallel:` and composite `runs.steps`.
- `owner/repo[/path]@ref` uses the [default host](configuration.md#actionsdefaulthost). `https://host/owner/repo@ref` names its host explicitly.
- The dep name is `owner/repo[/path]`. The source is `https://<host>/<owner>/<repo>`.

| DepType | Datasource | Versioning |
|---|---|---|
| `action` | `git-tags` | `tag` |
| `setup` | per action (below) | `semver` or `tag` |

**Ref forms:**
- Tag: `@v4`, `@v4.2.0`.
- Pinned: `@<40/64-hex sha> # v4.2.0`, or `# ratchet:owner/repo@v4`.
- Anything else is skipped.

**Edits:**
- A tag is replaced by the new tag at the same precision (`@v4` → `@v5`, `@v4.1.0` → `@v4.2.0`).
- A pinned ref gets the new SHA, and the comment version is replaced.
- With `pin: digest`, a tag becomes `@<sha> # <tag>`.
- A floating pin (`@<sha> # v4`) whose tag moved gets a digest refresh.

**Skips:**

| Reason | When |
|---|---|
| `local action` | `./…`, `$/…` |
| `docker reference (docker manager)` | `docker://…`, handled by the docker manager |
| `unrecognized uses` | no `@`, missing owner/repo/ref, unparsable URL |
| `non-version ref` | a branch or other ref that is not a tag (`@main`) |
| `pinned without version comment` | a SHA without a parsable `# vX` comment |
| `cannot locate value` | the value's bytes could not be found on its line |

### Setup action inputs

For these actions, the version input in `with:` is also a dep (DepType `setup`):

| Action | Input | Datasource | Name |
|---|---|---|---|
| `actions/setup-node` | `node-version` | `node-version` | `node` |
| `actions/setup-go` | `go-version` | `go-toolchain` | `go` |
| `azure/setup-helm` | `version` | `github-releases` | `helm/helm` |
| `azure/setup-kubectl` | `version` | `github-releases` | `kubernetes/kubernetes` |
| `hashicorp/setup-terraform` | `terraform_version` | `github-releases` | `hashicorp/terraform` |
| `golangci/golangci-lint-action` | `version` | `github-releases` | `golangci/golangci-lint` |

- Only literal values are extracted.
- A full `[v]X.Y.Z` uses `semver` and keeps the `v` style.
- A partial `26`, `1.27` or `v2.1` uses `tag` versioning, and its precision is kept (`26` → `27`).
- setup-python and setup-java are not extracted.
- Skips: `expression` (`${{ … }}`), `non-version value` (empty or a word such as `lts/*`, `stable`), `unsupported range`, `cannot locate value`.

## gomod

**Files:** `go.mod`. Parsed with `golang.org/x/mod/modfile`.

| DepType | What | Datasource / versioning | Default |
|---|---|---|---|
| `direct` | `require` without `// indirect` | `go` / `go` | updated |
| `indirect` | `require … // indirect` | `go` / `go` | ignored (opt in with `ignore: false`) |
| `go` | `go 1.x` directive | `go-toolchain` / `tag` | ignored (opt in with `ignore: false`) |
| `toolchain` | `toolchain go1.x.y`; the span covers the version after `go` | `go-toolchain` / `tag` | updated |

**Versioning:**
- `go` versioning excludes pseudo-versions, prereleases (unless the current version is one), `+incompatible` (unless the current version has it), and other majors (except v0 → v1).
- Directives move at their precision: `go 1.26` → `go 1.27`, `go 1.26.1` → `go 1.27.1`.

**Skips:**

| Reason | When |
|---|---|
| `replaced` | a `replace` directive targets the module |
| `pseudo-version` | the current version is a pseudo-version |
| `vendored module (not supported)` | `vendor/modules.txt` exists next to `go.mod` |
| `prerelease toolchain` | `go1.27rc1` style directive |
| `cannot locate value` | the version bytes were not found |

**Post-processing (`apply`, `run`):**
- After any gomod change, deps runs `go mod tidy -modfile=<tmp>/go.mod` in the module directory, on temporary copies of `go.mod` and `go.sum`, with `GOWORK=off` and `GOFLAGS` cleared.
- The tidied `go.mod` and `go.sum` replace the patched files. The real files are only written by `apply`.
- At most 4 post-processors (tidy and npm installs together) run concurrently.
- A failure drops that module's change: `go mod tidy in <dir>: <last 20 lines of stderr>`.
- `plan` does not run tidy, and prints `note    go.sum changes are computed by apply/run`.

## docker

| DepType | Files | Extracted |
|---|---|---|
| `dockerfile` | `Dockerfile`, `Containerfile`, `Dockerfile.<x>`, `<x>.Dockerfile` | `FROM` (flags skipped, line continuations and `# escape=` honored), `COPY --from=<image>`, `RUN --mount=…,from=<image>` |
| `compose` | `(docker-)compose*.y(a)ml` | `services.*.image` |
| `workflow` | workflow YAML (as github-actions) | `jobs.*.container` (string or `.image`), `jobs.*.services.*.image`, `uses: docker://…` |
| `action` | `action.y(a)ml` | `runs.image: docker://…` (with `runs.using: docker`), `uses: docker://…` in `runs.steps` |
| `kubernetes` | other `*.y(a)ml` containing `image:` | scalar `image` in `containers`, `initContainers` and `ephemeralContainers` items |
| `helm` | other `*.y(a)ml` containing `repository:` | `*image` mappings with `repository` + `tag` (optional `registry`, digest in `tag: x@sha256:…` or sibling `digest:`) |

- **Dockerfile details:**
  - `scratch`, earlier stage names (`AS <name>`) and numeric stage indexes are not deps.
  - A reference that is exactly `$X` or `${X}` resolves to `ARG X=<image>`, declared before the first `FROM` or in the current stage. The dep sits at the ARG default, so the update rewrites the ARG line.
- **YAML files:** other YAML is parsed document by document. A parse error (typically a Helm template) yields no deps and no error.
- **Names:** references are parsed with distribution/reference. The dep name is the familiar name (`postgres`, `ghcr.io/org/img`); lookups use the canonical name.
- **Versioning:** `docker`. Candidates need the same prefix, the same number of numeric parts and the same suffix.
- **Non-version tags** (`latest`, `bookworm`) only get digest maintenance: a pinned ref → `digest` refresh; unpinned with `pin: digest` → `pin`; otherwise skipped as `non-version tag`.
- **Edits:**
  - The tag is replaced in place.
  - `tag@sha256:…` is refreshed together with the tag.
  - With `pin: digest`, `@sha256:<digest>` is appended. For multi-platform images this is the index digest.

**Skips:** `variable reference` (`$` in the reference, or a variable ARG default), `template value` (`{{` in Kubernetes or Helm values), `invalid reference`, `no tag`, `digest without tag`, `cannot locate value`.

## npm

**Files:** `package.json`, stream-parsed. Each value's byte span must equal the quoted value in the file; otherwise, for example with `\u` escapes, the dep is skipped as `cannot locate value`.

| DepType | What | Datasource | Default |
|---|---|---|---|
| `dependencies` | `dependencies` | `npm` | updated (commit type `fix`) |
| `devDependencies`, `optionalDependencies` | same-named sections | `npm` | updated |
| `peerDependencies` | `peerDependencies` | `npm` | ignored (opt in with `ignore: false`) |
| `packageManager` | `"packageManager": "npm@x"`, `pnpm@x`, `yarn@x`; the name is the tool | `npm` | updated |
| `engines` | `engines.{node,npm,pnpm,yarn}` | node: `node-version`; others: `npm` | ignored (opt in with `ignore: false`) |
| `volta` | `volta.{node,npm,pnpm,yarn}` | same | updated |

`overrides`, `resolutions` and `bundleDependencies` are not extracted.

**Ranges:**
- Accepted: an optional `^`, `~`, `>=` or `=`, followed by a full `MAJOR.MINOR.PATCH[-prerelease]`.
- Only the version bytes are replaced, so the operator is kept and the range is **bumped**: `^1.2.3` → `^1.4.0` (Renovate's `rangeStrategy: bump`).
- For `engines` and `volta`, a partial `^26` or `10.2` is also accepted. It uses `tag` versioning on the numeric part (`^26` → `^27`).
- Versioning is `semver`. Prerelease candidates are only considered from a prerelease, and deprecated versions are dropped.

**Workspaces:** a dep naming a member of the manifest's own workspace is never updated.

**Skips:**

| Reason | Values |
|---|---|
| `npm alias` | `npm:` |
| `local dependency` | `file:`, `link:`, `workspace:`, `./`, `../`, `/`, `~/` |
| `git dependency` | `github:`, `gitlab:`, `bitbucket:`, `gist:`, `git+…`, `git://`, `git@`, `owner/repo[#ref]` |
| `url dependency` | `http(s)://` tarballs |
| `tag` | dist-tags such as `latest`, `next` |
| `unsupported range` | `\|\|`, spaces, `x`/`*` wildcards, partial versions (outside engines/volta), `<`, hyphen ranges, `v1.2.3`, empty |
| `packageManager with hash` | `pnpm@9.0.0+sha512…` |
| `unsupported packageManager` | a tool other than npm, pnpm or yarn |
| `cannot locate value` | escaped value |

### Lockfile post-processing (`apply`, `run`)

1. **Lock root.** The lock root is the nearest directory at or above the `package.json` (within `--dir`) that holds `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock`. It is accepted if it is the manifest's own directory, or if its `package.json` `workspaces` (an array or `{packages}`) or `pnpm-workspace.yaml` `packages` globs match the manifest directory (`!glob` excludes). Otherwise the manifest is only patched; no lockfile is created.
2. **Temporary copy.** The root and all member `package.json` files (patched where changed), the lockfile, `.npmrc`, `pnpm-workspace.yaml`, `.yarnrc.yml`, and the files referenced by `yarnPath`/`plugins` are copied to a temp dir.
3. **Install.**

   | Tool | Command |
   |---|---|
   | npm | `npm install --package-lock-only --ignore-scripts --no-audit --no-fund` |
   | pnpm | `pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile` |
   | yarn berry (`.yarnrc.yml`, or `__metadata` in the lock) | `yarn install --mode=update-lockfile`, with `YARN_ENABLE_SCRIPTS=0 YARN_ENABLE_IMMUTABLE_INSTALLS=0` |
   | yarn classic | `yarn install --ignore-scripts --ignore-engines --non-interactive --no-progress` |

   The environment always includes `npm_config_update_notifier=false` and `COREPACK_ENABLE_DOWNLOAD_PROMPT=0`.
4. **Tool lookup.** deps uses the tool binary from `PATH`. Failing that:
   - if the root's `packageManager` names the tool, `npx --yes <tool>@<version>` (for yarn berry, `npx --yes --package=@yarnpkg/cli-dist@<version> -- yarn`);
   - else `npx --yes <tool>`;
   - else error `<tool> not found (needed to update <lock>)`.
5. **Several lockfiles.** If a root has more than one lockfile, the `packageManager` tool's lockfile wins, else `package-lock.json`, then pnpm, then yarn. `npm-shrinkwrap.json` is not handled.
6. **Failure.** A failed install reports `<tool> install in <dir>: <last 20 output lines>`, and the root's changes are dropped.

`plan` does not run installs, and prints `note    package-lock.json changes are computed by apply/run`.

## custom

**Files:** `custom[].files` globs. Each entry is one manager instance; see [configuration.md](configuration.md#custom-managers).

| DepType | Mode |
|---|---|
| `annotation` | `# deps: <datasource> <name>` or `# renovate: datasource=… depName=…` on the previous line |
| `regex` | `match` RE2 over the whole file |

**Skips:** `no version on next line`, `renovate marker without datasource`, `renovate marker without depName`, `empty version`, `empty name`, `empty datasource`, `checksum is not 64 or 128 hex digits`.

**Edits:** the version is replaced. A captured digest (`@sha256:` for docker markers, or a `digest` group) and a `checksum` group are replaced together with it.

## Datasources

All network calls share one HTTP/2 client and one semaphore of `--concurrency` slots (default 32).
- Idempotent requests are retried up to 3 times on connection errors and 429/500/502/503/504, with jittered exponential backoff, honoring `Retry-After`.
- Rate limits that reset within 60s are waited out. Longer ones fail fast with `rate limited by <host> until <time>`.
- Lookups are deduped within a run. There is no persistent cache.

| Datasource | Name form | Endpoints | Auth | Release times |
|---|---|---|---|---|
| `git-tags` | repository URL (`https://host/path` or `host/path`) | `GET <url>/info/refs?service=git-upload-pack`, then `POST <url>/git-upload-pack` (protocol v2 `ls-refs`, `ref-prefix refs/tags/`, `peel`); v0 servers via the advertisement | Basic `x-access-token:<token>` for the platform host; `DEPS_GITHUB_COM_TOKEN` for github.com; on 401, `git credential fill` once per host | github.com via the release list; Gitea/Forgejo via their releases API |
| `github-releases` | `owner/repo` (github.com) or `https://host/owner/repo` (GHES) | with a token: GraphQL `releases` (`https://api.github.com/graphql`, GHES `<host>/api/graphql`); without: `GET /repos/{repo}/releases?per_page=100`. At most 5 pages, and only until the current release is listed. Drafts and prereleases are dropped. | token for the host | `publishedAt` |
| `go` | module path | GOPROXY `<proxy>/<escaped path>/@v/list`, following `,` / `\|` fallback semantics (`direct` → `go list -m -versions -json`); GONOPROXY/GOPRIVATE modules via `go list` | none for proxy requests; `go list` uses the go command's own auth (git credentials, `.netrc`) | `@v/<v>.info`, read from `$GOMODCACHE/cache/download` first, then the proxy (`go list -m -json` for private modules) |
| `go-toolchain` | `go` / `toolchain` | `https://go.dev/dl/?mode=json&include=all`, once per run; a derived `X.Y` per minor | none | none |
| `docker` | image reference | registry `tags/list` (all pages; Docker Hub with page size 10000), manifest HEAD for digests (index digest for multi-platform images) | Docker config and credential helpers (`authn.DefaultKeychain`) | image config `created` |
| `npm` | package name (`@scope/name` sent as `@scope%2fname`) | `GET <registry>/<name>` with `Accept: application/vnd.npm.install-v1+json`, stream-decoded; deprecated versions dropped; 404 → `package <name> not found` | `.npmrc` `//host/path/:_authToken=` (longest prefix), `${VAR}` expansion | packument `time` |
| `node-version` | any (`node`) | `<DEPS_NODE_DIST_URL or https://nodejs.org/dist>/index.json`, once per run; published versions plus a derived `X.Y` and `X` per stable line | none | `date` |
| `custom.<name>` | free-form | `customDatasources.<name>.url`, once per run, at most 10 MiB | none | none |

**npm registry selection.** deps reads the nearest `.npmrc` per `package.json` directory, walking up to `--dir`.
- A scoped name uses `@scope:registry=` from that file; any other name uses `registry=`.
- Without one, deps uses `NPM_CONFIG_REGISTRY` (or `npm_config_registry`), else `https://registry.npmjs.org/`.

**Go proxy environment.** `GOPROXY`, `GONOPROXY`, `GOPRIVATE` and `GOMODCACHE` are read once via `go env -json`. If `go` is not on `PATH`, deps falls back to the process environment, with default `GOPROXY=https://proxy.golang.org,direct`.

**OSV (security).**
- deps sends one `POST https://api.osv.dev/v1/querybatch` per 1000 packages, then one `GET /v1/vulns/{id}` per advisory.
- Ecosystems: `Go` (gomod `direct`/`indirect`), `npm`, and `GitHub Actions` (github.com actions; ranges are evaluated locally).
- Withdrawn advisories are skipped, and aliases are reported once.

**Checksums.** `checksum.asset` reads the asset digest from the already-fetched GitHub release list. `checksum.url` is downloaded once per URL, with a 1 MiB cap.

**Release notes** (PR bodies only).
- deps fetches one page of the source repository's GitHub or Gitea/Forgejo releases per run.
- The source repository comes from the action, the module's forge repository (including a small vanity table), npm's `repository` field, or the OCI `org.opencontainers.image.source` label.
- Bodies are capped at 2 KiB each and 40 KiB in total, and include a compare link.
- Failures only drop the notes.
