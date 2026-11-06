package releasenotes

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the release notes in PR bodies.
var Doc = refdoc.Section{
	Title: "Release notes",
	Body:  "Release notes appear in PR bodies only.",
	Items: []string{
		"deps fetches one page of the source repository's GitHub or Gitea/Forgejo releases per run.",
		"The source repository comes from the action, the module's forge repository (including a small vanity table), npm's `repository` field, the PyPI project URLs, the Maven POM's `<scm>` (or its parent's), the OCI `org.opencontainers.image.source` label, or the repository of a `gitea-*`/`forgejo-*` releases or tags dep.",
		"Bodies are capped at 2 KiB each and 40 KiB in total, and include a compare link.",
		"Failures only drop the notes.",
	},
}
