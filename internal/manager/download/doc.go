package download

import "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"

// DocURLs documents the URL forms Locate recognizes, with the dep name of each, for the
// reference documentation of the managers using it.
const DocURLs = "`https://<host>/<owner>/<repo>/archive/[refs/tags/]<tag>.tar.gz` (also `.zip`, `.tgz`; GitHub, Gitea, Forgejo): name `<owner>/<repo>`, tags of the repository; " +
	"`https://<host>/<path>/-/archive/<tag>/<file>` (GitLab, `.tar.gz`, `.tar.bz2`, `.tar`, `.tgz`, `.zip`): name the project path, its tags; " +
	"`https://<host>/<owner>/<repo>/releases/download/<tag>/<asset>` (GitHub, Gitea, Forgejo): name `<owner>/<repo>`, its releases; " +
	"`https://<host>/<path>/-/releases/<tag>/downloads/<asset>` (GitLab release asset links): name the project path, its releases; " +
	"`https://registry.npmjs.org/[@<scope>/]<package>/-/<package>-<version>.tgz`: name the package. " +
	"GitHub is `github.com`; Gitea and Forgejo are the checkout's forge (the platform's server, else the origin remote's host), `gitea.com`, `codeberg.org` and `code.forgejo.org`; GitLab is any host"

// DocDatasource documents the datasource of each URL form.
const DocDatasource = "archives: `git-tags`; releases: `github-releases`, `gitea-releases` (`forgejo-releases` on codeberg.org and code.forgejo.org, registry the host), `gitlab-releases` (registry the host); npm tarball: `npm`"

// DocVersioning documents the versioning of the deps Locate fills.
const DocVersioning = "forges: `loose`; npm: `semver`"

// Datasources are the datasources of the deps Locate fills.
var Datasources = []string{"git-tags", "github-releases", forge.GiteaReleases, forge.ForgejoReleases, forge.GitLabReleases, "npm"}
