package osv

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the OSV advisory lookup.
var Doc = refdoc.Section{
	Title: "Security advisories (OSV)",
	Items: []string{
		"deps sends one `POST https://api.osv.dev/v1/querybatch` per 1000 packages, then one `GET /v1/vulns/{id}` per advisory.",
		"Ecosystems: `Go` (gomod `direct`/`indirect`), `npm`, and `GitHub Actions` (github.com actions; ranges are evaluated locally).",
		"Withdrawn advisories are skipped, and aliases are reported once.",
	},
}
