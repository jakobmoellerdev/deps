package docker

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the docker manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container image references in Dockerfiles, Compose files, workflows, actions, Kubernetes manifests and Helm values, and the apk and apt package pins of Dockerfile `RUN` instructions.",
	Files: []string{
		"`Dockerfile`, `Containerfile`, `Dockerfile.<x>`, `<x>.Dockerfile` (DepType `dockerfile`)",
		"Compose files (DepType `compose`): `compose.y(a)ml`, `docker-compose.y(a)ml`, overrides and variants (`compose.override.yml`, `compose.<env>.yaml`, `docker-compose.<env>.yml`, `<x>.compose.y(a)ml`), and any other YAML shaped like one (see Compose details)",
		"workflow YAML, as for github-actions (DepType `workflow`)",
		"`action.y(a)ml` (DepType `action`)",
		"other `*.y(a)ml` containing `image:` (DepType `kubernetes`) or `repository:` (DepType `helm`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "dockerfile", What: "`FROM` (flags skipped, line continuations and `# escape=` honored), `COPY --from=<image>`, `RUN --mount=…,from=<image>`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "compose", What: "`services.*.image` (also through `<<:` merges and aliases), `image` in mappings at any depth of top-level `x-*` extension fields, `services.*.build.additional_contexts` entries `docker-image://<ref>`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "workflow", What: "`jobs.*.container` (string or `.image`), `jobs.*.services.*.image`, `uses: docker://…`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "action", What: "`runs.image: docker://…` (with `runs.using: docker`), `uses: docker://…` in `runs.steps`", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "kubernetes", What: "scalar `image` in `containers`, `initContainers` and `ephemeralContainers` items (not items with a `container` key: Azure Pipelines container resources, see azure-pipelines)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: "helm", What: "`*image` mappings with `repository` + `tag` (optional `registry`, digest in `tag: x@sha256:…` or sibling `digest:`)", Versioning: "`docker`", Datasources: []string{"docker"}},
		{Name: TypeInstall, What: "version-pinned packages of `apk add` (`bash=5.2.37-r2`) and `apt-get install` / `apt install` (`curl=8.14.1-2`) commands in `RUN` instructions (shell form; see Installed packages)", Versioning: "`apk`, `deb`", Datasources: []string{"apk", "deb"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "a `$` that no ARG default (Dockerfile) or [`registryAliases`](configuration.md#registryaliases) entry resolves, a variable ARG default, or a Dockerfile tag or digest spanning an ARG boundary (`node:${NODE}-alpine`); Compose: see `interpolated image`"},
		{Reason: "interpolated image", When: "Compose interpolation other than `${VAR:-default}` / `${VAR-default}` (and no `registryAliases` key), or a version spanning a variable boundary"},
		{Reason: "template value", When: "`{{` in a Kubernetes image (also on the `image:` lines of YAML that does not parse) or in Helm values"},
		{Reason: "invalid reference", When: "the reference does not parse"},
		{Reason: "no tag", When: "Helm values with an empty `tag` (the chart's `appVersion` applies, not `latest`)"},
		{Reason: "unparsable compose file", When: "a Compose file name whose content is not valid YAML (e.g. an indented fragment); one dep-less skip at line 1"},
		{Reason: "cannot locate value", When: "the value's bytes could not be found"},
		{Reason: dep.SkipUnknownRegistry, When: "an `apk add` pin whose stage's base image names no Alpine release (`alpine:latest`, `node:22-alpine`), with a repository tag (`bash@edge=…`) or with `--repository`/`-X`, `--repositories-file`, `--root`, `--arch`; every `apt-get install` pin. A rule's [`registryUrls`](configuration.md#rulesregistryurls) lifts it"},
		{Reason: skipConstraint, When: "an `apk add` constraint other than `=` (`~`, `=~`, `<`, `>=`, `><`) or an apt wildcard version (`8.14.*`)"},
	},
	Sections: []refdoc.Section{
		{Title: "Dockerfile details", Items: []string{
			"`scratch`, earlier stage names (`AS <name>`) and numeric stage indexes are not deps.",
			"A reference that is exactly `$X` or `${X}` resolves to `ARG X=<image>`, declared before the first `FROM` or in the current stage. The dep sits at the ARG default, so the update rewrites the ARG line.",
			"Other ARG references in a reference are replaced by their defaults, as Renovate does: `alpine:${ALPINE_VERSION}`, `${BASE}:3.13-slim`, `${REGISTRY}/app:${APP_VERSION}`. The tag and the digest are edited where their bytes are, in the instruction or in the ARG default; deps whose tag is one ARG default share it (VersionRef `ARG <name>`, one version for all). A tag that spans an ARG boundary (`node:${NODE_VERSION}-alpine`) cannot be edited and is a `variable reference`. Variables without a literal default stay as written, for `registryAliases` to resolve.",
		}},
		{Title: "Installed packages", Items: []string{
			"`RUN apk add` and `RUN apt-get install` (also `apt install`, `/sbin/apk`) pins are extracted from every command of the shell form (`&&`, `||`, `;`, `|` separate commands; quotes around one package are stripped), as Renovate's dockerfile manager does. Options are skipped, with their values (`--virtual .build-deps`, `-t bookworm-backports`, `-o Opt=1`).",
			"apk packages are looked up in the repositories of the stage's base image, which Renovate leaves to `packageRules` (`unknown-registry`): `alpine:<X.Y>[.Z]` and `alpine:edge` read `https://dl-cdn.alpinelinux.org/alpine?branch=v<X.Y>&components=main,community&arch=<arch>`, any other image with an `alpine<X.Y>` tag (`node:22-alpine3.21`) the same, and Chainguard images (`cgr.dev/chainguard/*`, `chainguard/wolfi-base`) Wolfi's `https://packages.wolfi.dev/os?arch=<arch>`. The arch follows `FROM --platform=linux/<arch>` (default x86_64). A stage `FROM <earlier stage>` keeps its registry; ARG defaults and `registryAliases` are resolved first.",
			"Debian packages need a suite deps does not derive from the base image (security and point-release updates live in other suites): they are skipped as `unknown registry` until a rule sets `registryUrls`, e.g. `{datasources: [deb], paths: [Dockerfile], registryUrls: ['https://deb.debian.org/debian?suite=trixie&components=main&binaryArch=amd64']}`. The same rule field replaces the derived apk registry.",
			"Packages without a version, version variables (`bash=${BASH_VERSION}`, `variable reference`), virtual and provider names (`.build-deps`, `so:libc.so`, `cmd:sh`), apt release pins (`curl/trixie`), architecture-qualified names (`libc6:i386`), removals, patterns and package files are not updated. `dnf`/`yum` pins are not extracted: `name-version` cannot be split without the repository, and Renovate has none either.",
			"Group the pins with their base image (a rule with `group`) so that an Alpine release update and the matching package versions land together.",
		}},
		{Title: "Compose details", Items: []string{
			"A YAML file with another name is a Compose file when every top-level key is a Compose key (`version`, `name`, `include`, `services`, `networks`, `volumes`, `configs`, `secrets`, `models`) or an `x-*` extension, and `services` is a mapping of mappings, at least one with `image` or `build`. Kubernetes manifests, workflows and Helm values fail this check.",
			"Anchored values are extracted once, at the anchor definition: services merging `<<: *runner` share the `x-runner` dep; a service that sets its own `image` after the merge is its own dep.",
			"Interpolation: `${VAR:-default}` and `${VAR-default}` resolve to the default, and the update rewrites the default bytes (`${REGISTRY:-docker.io}/nginx:${TAG:-1.27}` → `${TAG:-1.28}`). `$$` is a literal `$`.",
			"`build.args` values are not images. Dockerfiles referenced by `build.dockerfile` are found by the Dockerfile rules on their own.",
			"`include:` and `extends:` need no handling: the referenced files are scanned like every other file.",
		}},
		{Title: "YAML files", Body: "Other YAML is parsed document by document. A file that does not parse (typically a Helm template) is read line by line instead: each block-style `image: <value>` line is a `kubernetes` dep (a plain or quoted value; `{{` makes it a `template value`), and the parse error is logged (info level when such lines exist, else debug)."},
		{Title: "Names", Body: "References are parsed as docker does (names without a registry are on docker.io, single-component ones under library/). The dep name is the familiar name (`postgres`, `ghcr.io/org/img`); lookups use the canonical name."},
		{Title: "Versioning", Body: "`docker`. Candidates need the same prefix, the same number of numeric parts and the same suffix. Installed packages use `apk` and `deb`."},
		{Title: "Non-version tags", Body: "Non-version tags (`latest`, `bookworm`, commit hashes such as `3a22eba`) only get digest maintenance: a pinned ref → `digest` refresh; unpinned with `pin: digest` → `pin`; otherwise skipped as `non-version tag`. A reference without a tag is its implicit `latest`, as docker pulls it and Renovate looks it up: `FROM alpine` is pinned to `alpine@sha256:<latest's digest>` under `pin: digest` (else `non-version tag`), and `image@sha256:…` gets `digest` updates to the digest `latest` has now."},
		{Title: "Registry aliases", Body: "[`registryAliases`](configuration.md#registryaliases) resolve a prefix such as `$CI_REGISTRY/` or `${REGISTRY:-}` before the reference is parsed (Compose: before interpolation, Dockerfiles: after ARG substitution). The dep is named and looked up by the resolved image; the file keeps the alias, only the tag and digest are edited, and the image is not renamed by replacements."},
		{Title: "Replacements", Body: "Image references written as one token (Dockerfiles, compose, workflows, Kubernetes manifests) and Helm values images (`repository`, and `registry` when set: the new name is split at its registry) can be [replaced](configuration.md#rulesreplacementname) by another image name; the tag and a pinned digest (of the new image) are written with it."},
		{Title: "Edits", Items: []string{
			"The tag (an installed package: its version) is replaced in place.",
			"`tag@sha256:…` is refreshed together with the tag.",
			"With `pin: digest`, `@sha256:<digest>` is appended. For multi-platform images this is the index digest.",
		}},
	},
}
