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
		"`(docker-)compose*.y(a)ml` (DepType `compose`)",
		"workflow YAML, as for github-actions (DepType `workflow`)",
		"`action.y(a)ml` (DepType `action`)",
		"other `*.y(a)ml` containing `image:` (DepType `kubernetes`) or `repository:` (DepType `helm`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "dockerfile", What: "`FROM` (flags skipped, line continuations and `# escape=` honored), `COPY --from=<image>`, `RUN --mount=…,from=<image>`", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "compose", What: "`services.*.image`", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "workflow", What: "`jobs.*.container` (string or `.image`), `jobs.*.services.*.image`, `uses: docker://…`", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "action", What: "`runs.image: docker://…` (with `runs.using: docker`), `uses: docker://…` in `runs.steps`", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "kubernetes", What: "scalar `image` in `containers`, `initContainers` and `ephemeralContainers` items", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "helm", What: "`*image` mappings with `repository` + `tag` (optional `registry`, digest in `tag: x@sha256:…` or sibling `digest:`)", Datasource: "`docker`", Versioning: "`docker`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` in the reference, or a variable ARG default"},
		{Reason: "template value", When: "`{{` in Kubernetes or Helm values"},
		{Reason: "invalid reference", When: "the reference does not parse"},
		{Reason: "no tag", When: "the reference has neither tag nor digest"},
		{Reason: "digest without tag", When: "the reference has a digest but no tag"},
		{Reason: "cannot locate value", When: "the value's bytes could not be found"},
	},
	Sections: []refdoc.Section{
		{Title: "Dockerfile details", Items: []string{
			"`scratch`, earlier stage names (`AS <name>`) and numeric stage indexes are not deps.",
			"A reference that is exactly `$X` or `${X}` resolves to `ARG X=<image>`, declared before the first `FROM` or in the current stage. The dep sits at the ARG default, so the update rewrites the ARG line.",
		}},
		{Title: "YAML files", Body: "Other YAML is parsed document by document. A parse error (typically a Helm template) yields no deps and no error."},
		{Title: "Names", Body: "References are parsed with distribution/reference. The dep name is the familiar name (`postgres`, `ghcr.io/org/img`); lookups use the canonical name."},
		{Title: "Versioning", Body: "`docker`. Candidates need the same prefix, the same number of numeric parts and the same suffix."},
		{Title: "Non-version tags", Body: "Non-version tags (`latest`, `bookworm`) only get digest maintenance: a pinned ref → `digest` refresh; unpinned with `pin: digest` → `pin`; otherwise skipped as `non-version tag`."},
		{Title: "Edits", Items: []string{
			"The tag is replaced in place.",
			"`tag@sha256:…` is refreshed together with the tag.",
			"With `pin: digest`, `@sha256:<digest>` is appended. For multi-platform images this is the index digest.",
		}},
	},
}
