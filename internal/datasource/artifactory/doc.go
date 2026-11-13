package artifactory

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the artifactory datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "the folder path below the registry URL (custom `registryUrl`, regex group `registryUrl` or Renovate marker `registryUrl=`; whitespace-separated URLs are all listed and merged), " +
		"or the full `https://` folder URL without one; every link of the listing but `../` is a version (a folder name without its trailing `/`, or a file name for `extractVersion`)",
	Endpoints:    "`GET <registryUrl>/<name>/`, the HTML directory listing, once per folder; at most 32 MiB. Of several registry URLs, one that answers 404/410 or lists nothing is skipped (debug log), while one that could not be read (a transport error, 401/403, 5xx) fails the dependency: a mirror must not decide the version on its own",
	Auth:         "the platform token on its host (Basic `x-access-token`), else anonymous; after HTTP 401 `git credential fill` for the host",
	ReleaseTimes: "the date in the column after each link (`21-Jul-2021 20:08`), as UTC",
}
