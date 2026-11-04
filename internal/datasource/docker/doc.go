package dockerds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the docker datasource.
var Doc = refdoc.Datasource{
	Name:         "docker",
	NameForm:     "image reference",
	Endpoints:    "registry `tags/list` (all pages; Docker Hub with page size 10000), manifest HEAD for digests (index digest for multi-platform images); for release times a manifest GET by tag (Docker Hub: by the HEAD's digest, once per digest)",
	Auth:         "Docker config and credential helpers (`authn.DefaultKeychain`)",
	ReleaseTimes: "image config `created`; else, and for non-image artifacts (Helm charts), the manifest annotation `org.opencontainers.image.created`",
}
