package releasenotes

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the release notes in PR bodies.
var Doc = refdoc.Section{
	Title: "Release notes",
	Body:  "Release notes appear in PR bodies only.",
	Items: []string{
		"deps fetches one page of the source repository's GitHub, GitLab or Gitea/Forgejo releases per run.",
		"The source repository comes from the action, the module's forge repository (including a small vanity table), npm's `repository` field, the PyPI project URLs, the Maven POM's `<scm>` (or its parent's; also for clojure deps), the OCI `org.opencontainers.image.source` label, the GitHub, GitLab or Codeberg `homepage` of a vcpkg port's manifest in its registry, or the repository of a `gitlab-*`/`gitea-*`/`forgejo-*` releases or tags dep.",
		"Repositories on gitlab.com and on the configured GitLab instance (`gitlabci.url`, `CI_SERVER_URL`, or the GitLab platform's server) use the GitLab releases API with the whole project path (subgroups included) and the token registered for the host (`Authorization: Bearer`, the CI job token as `JOB-TOKEN`); a Go module there is taken to be its project's root (compare link only when that project's releases were found). Issue (`#1`) and merge request (`!2`) references in GitLab release notes link to the project.",
		"Bodies are capped at 2 KiB each and 40 KiB in total, and include a compare link (GitLab: `/-/compare/<from>...<to>`).",
		"An update that only moves a digest (a digest-pinned action, a git-refs or git-tags dep, a submodule) has no releases in between and gets only the commit-to-commit compare link of its two digests. OCI digests (docker) name a manifest, not a commit, so they get none.",
		"Failures only drop the notes.",
	},
}
