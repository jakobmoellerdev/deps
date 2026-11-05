package rubygems

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the rubygems datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "gem name (case-sensitive, as on the gem server); the dep's source is its whitespace-separated gem servers in lookup order (\"\" = rubygems.org)",
	Endpoints: "rubygems.org (also when a Gemfile names `https://rubygems.org`): `GET <DEPS_RUBYGEMS_URL or https://rubygems.org>/api/v1/versions/<name>.json`; " +
		"other gem servers: the compact index `GET <source>/info/<name>` (what Bundler reads), and on a 404 `GET <source>/api/v1/versions/<name>.json`; " +
		"once per (server, gem); platform builds (`1.18.10-x86_64-linux-gnu`) count as their version; yanked versions are not listed by either API; " +
		"the first server that has the gem answers, none → `gem <name> not found`. " +
		"Source repository (release notes, rubygems.org only): `GET <base>/api/v1/gems/<name>.json` `source_code_uri`, else a forge `homepage_uri`, only for versions in a pull request",
	Auth:         "userinfo of the source URL (`https://user:pass@gems.example.com`) as basic auth, else Bundler's `BUNDLE_<HOST>` variable (`gems.example-corp.com` → `BUNDLE_GEMS__EXAMPLE___CORP__COM`) holding `user:password` or a token; redacted in errors and logs",
	ReleaseTimes: "rubygems.org `created_at` (the earliest of the version's platform builds); the compact index of other servers has none",
}
