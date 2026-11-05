package osv

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the OSV advisory lookup.
var Doc = refdoc.Section{
	Title: "Security advisories (OSV)",
	Items: []string{
		"deps sends `POST https://api.osv.dev/v1/querybatch` requests of 100 packages each, concurrently, then one `GET /v1/vulns/{id}` per advisory as soon as the batch naming it returns.",
		"Ecosystems: `Go` (gomod `direct`/`indirect`; the `go` and `toolchain` directives as packages `stdlib` and `toolchain`, so a vulnerable Go release gets the smallest fixed one of its line; a 2-part `go 1.22` is queried as `1.22.0` and only fixes outside its line count), `npm` (the locked version when package-lock.json has one), `crates.io` (crates.io deps of cargo, with the version Cargo.lock locks; without a lock a partial requirement such as `0.4` is queried as `0.4.0` and only fixes outside its range count), `PyPI` (`pypi` datasource, PEP 503-normalized names, the locked version when the lockfile has one), `Maven` (maven-datasource deps named `group:artifact`, with the version gradle.lockfile locks), and `GitHub Actions` (github.com actions; ranges are evaluated locally).",
		"Not queried, because OSV has no ecosystem for them (checked 2026-11-05): container images (OSV covers the distribution packages inside an image, such as `Debian` or `Alpine`, not the image or its tag), Terraform providers and modules, and Helm charts.",
		"Withdrawn advisories are skipped, and aliases are reported once.",
	},
}
