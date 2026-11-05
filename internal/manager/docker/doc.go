package docker

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the docker manager (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container image references in Dockerfiles, Compose files, workflows, actions, Kubernetes manifests and Helm values.",
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
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the reference, or a variable ARG default (Compose: see `interpolated image`)"},
		{Reason: "interpolated image", When: "Compose interpolation other than `${VAR:-default}` / `${VAR-default}`, or a version spanning a variable boundary"},
		{Reason: "template value", When: "`{{` in Kubernetes or Helm values"},
		{Reason: "invalid reference", When: "the reference does not parse"},
		{Reason: "no tag", When: "the reference has neither tag nor digest"},
		{Reason: "digest without tag", When: "the reference has a digest but no tag"},
		{Reason: "unparsable compose file", When: "a Compose file name whose content is not valid YAML (e.g. an indented fragment); one dep-less skip at line 1"},
		{Reason: "cannot locate value", When: "the value's bytes could not be found"},
	},
	Sections: []refdoc.Section{
		{Title: "Dockerfile details", Items: []string{
			"`scratch`, earlier stage names (`AS <name>`) and numeric stage indexes are not deps.",
			"A reference that is exactly `$X` or `${X}` resolves to `ARG X=<image>`, declared before the first `FROM` or in the current stage. The dep sits at the ARG default, so the update rewrites the ARG line.",
		}},
		{Title: "Compose details", Items: []string{
			"A YAML file with another name is a Compose file when every top-level key is a Compose key (`version`, `name`, `include`, `services`, `networks`, `volumes`, `configs`, `secrets`, `models`) or an `x-*` extension, and `services` is a mapping of mappings, at least one with `image` or `build`. Kubernetes manifests, workflows and Helm values fail this check.",
			"Anchored values are extracted once, at the anchor definition: services merging `<<: *runner` share the `x-runner` dep; a service that sets its own `image` after the merge is its own dep.",
			"Interpolation: `${VAR:-default}` and `${VAR-default}` resolve to the default, and the update rewrites the default bytes (`${REGISTRY:-docker.io}/nginx:${TAG:-1.27}` → `${TAG:-1.28}`). `$$` is a literal `$`.",
			"`build.args` values are not images. Dockerfiles referenced by `build.dockerfile` are found by the Dockerfile rules on their own.",
			"`include:` and `extends:` need no handling: the referenced files are scanned like every other file.",
		}},
		{Title: "YAML files", Body: "Other YAML is parsed document by document. A parse error (typically a Helm template) yields no deps and no error."},
		{Title: "Names", Body: "References are parsed as docker does (names without a registry are on docker.io, single-component ones under library/). The dep name is the familiar name (`postgres`, `ghcr.io/org/img`); lookups use the canonical name."},
		{Title: "Versioning", Body: "`docker`. Candidates need the same prefix, the same number of numeric parts and the same suffix."},
		{Title: "Non-version tags", Body: "Non-version tags (`latest`, `bookworm`, commit hashes such as `3a22eba`) only get digest maintenance: a pinned ref → `digest` refresh; unpinned with `pin: digest` → `pin`; otherwise skipped as `non-version tag`."},
		{Title: "Replacements", Body: "Image references written as one token (Dockerfiles, compose, workflows, Kubernetes manifests) and Helm values images (`repository`, and `registry` when set: the new name is split at its registry) can be [replaced](configuration.md#rulesreplacementname) by another image name; the tag and a pinned digest (of the new image) are written with it."},
		{Title: "Edits", Items: []string{
			"The tag is replaced in place.",
			"`tag@sha256:…` is refreshed together with the tag.",
			"With `pin: digest`, `@sha256:<digest>` is appended. For multi-platform images this is the index digest.",
		}},
	},
}
