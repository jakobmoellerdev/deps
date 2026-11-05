package packagist

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the packagist datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "Composer package name `vendor/package` (case-insensitive, looked up lower-cased); the dep's source is its whitespace-separated Composer repositories in lookup order (\"\" = Packagist)",
	Endpoints: "Packagist (also when composer.json names `https://packagist.org` or `https://repo.packagist.org`): `GET <DEPS_PACKAGIST_URL or https://repo.packagist.org>/p2/<name>.json` (Composer 2 metadata, minified entries expanded); " +
		"other Composer repositories: `GET <url>/packages.json` once per repository (the URL itself when it ends in `.json`), then its `metadata-url` with `%package%` replaced (packages.drupal.org, Private Packagist, Satis), or else the packages it lists inline and in its `includes` (older Satis); " +
		"packages its `available-packages`/`available-package-patterns` do not name are not looked up there; a repository serving only the Composer 1 `providers-url` is an error. " +
		"Branches (`dev-main`, `2.x-dev`) are not versions. The first repository that has the package answers (Composer's canonical repositories), none → `package <name> not found`. " +
		"Source repository (release notes): the newest version's http(s) `source.url`",
	Auth:         "userinfo of the repository URL as basic auth, else Composer's `COMPOSER_AUTH` variable (auth.json as JSON): `http-basic` (`{\"repo.example.com\": {\"username\": …, \"password\": …}}`) and `bearer` (`{\"repo.example.com\": \"<token>\"}`) by host; redacted in errors and logs",
	ReleaseTimes: "the version's `time` (the tag's commit date as the repository records it); packages.drupal.org records none",
}
