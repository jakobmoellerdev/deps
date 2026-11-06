package galaxy

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the galaxy (role) datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "role `namespace.name` (`geerlingguy.java`)",
	Endpoints: "`GET <DEPS_GALAXY_URL or https://galaxy.ansible.com>/api/v1/roles/?owner__username=<namespace>&name=<name>`, once per role; " +
		"with several results the one whose `github_user` (or `username`) is the namespace. The versions are `summary_fields.versions[].name` that are version tags; " +
		"no result skips the dep (`role not found on <server>`). Source repository: `https://github.com/<github_user>/<github_repo>`",
	Auth:         "none",
	ReleaseTimes: "each version's `release_date` (else `created`)",
}

// CollectionDoc is the reference documentation of the galaxy-collection datasource.
var CollectionDoc = refdoc.Datasource{
	Name:     CollectionName,
	NameForm: "collection `namespace.name` (`community.general`); the dep's source is the Galaxy server of its requirements entry (`source`), \"\" = `DEPS_GALAXY_URL` or https://galaxy.ansible.com",
	Endpoints: "the server's API root once per server, as `ansible-galaxy` discovers it: `GET <server>/api/` (unless the server URL already has an `/api/` path, e.g. Automation Hub's `…/api/automation-hub/content/published/`), else `GET <server>/`, " +
		"whose `available_versions.v3` is the v3 API; then once per (server, collection) `GET <v3>collections/<namespace>/<name>/` (`deprecated`; a 404 skips the dep) " +
		"and `GET <v3>collections/<namespace>/<name>/versions/?limit=100`, following `links.next`. " +
		"Source repository (release notes): `metadata.repository` of `GET <v3>collections/<namespace>/<name>/versions/<version>/`, only for versions that end up in a PR",
	Auth:         "none",
	ReleaseTimes: "each version's `created_at`",
}
