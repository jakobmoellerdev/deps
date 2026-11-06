package rpm

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the rpm datasource.
var Doc = refdoc.Datasource{
	Name: Name,
	NameForm: "RPM package name as in the repository (`kernel`, `docker-ce`, `golang`); the dep's source is the repository URL from `registryUrl` (custom[] entry field, " +
		"`registryUrl` regex group or Renovate marker field): its `repodata/` directory, as Renovate takes it, or the repository root (dnf's `baseurl`). " +
		"Several whitespace-separated URLs are merged, as Renovate's `merge` registry strategy does (a distribution's release and updates repositories). " +
		"A version is `<ver>-<rel>` (`<ver>` for a package without a release), without the epoch, as Renovate writes it; use the `rpm` versioning, the default of rpm deps",
	Endpoints: "`GET <repository>/repodata/repomd.xml`, then the primary metadata it names (`<location href>`, against its `xml:base` when set), " +
		"gzip, zstd, xz, bzip2 or plain, detected by content; once per repository and run, stream-scanned so only the asked-for packages are kept (a package asked for later costs one more scan). " +
		"Every `<package type=\"rpm\">` counts, the builds of every architecture merged. A package no repository lists fails the dep with `rpm package <name> not found in <repositories>`",
	Auth:         "userinfo of the repository URL (`https://user:token@host/…`) as basic auth; redacted in errors and logs",
	ReleaseTimes: "earliest `<time file>` of the version's builds: when the build was added to the repository",
}
