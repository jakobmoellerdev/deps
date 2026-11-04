package osv

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the OSV advisory lookup.
var Doc = refdoc.Section{
	Title: "Security advisories (OSV)",
	Items: []string{
		"deps sends `POST https://api.osv.dev/v1/querybatch` requests of 100 packages each, concurrently, then one `GET /v1/vulns/{id}` per advisory as soon as the batch naming it returns.",
		"Ecosystems: `Go` (gomod `direct`/`indirect`), `npm` (the locked version when package-lock.json has one), `crates.io` (crates.io deps of cargo, with the version Cargo.lock locks; without a lock a partial requirement such as `0.4` is queried as `0.4.0` and only fixes outside its range count), `PyPI` (`pypi` datasource, PEP 503-normalized names, the locked version when the lockfile has one), and `GitHub Actions` (github.com actions; ranges are evaluated locally).",
		"Withdrawn advisories are skipped, and aliases are reported once.",
	},
}
