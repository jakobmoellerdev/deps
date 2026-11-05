package dockerds

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the docker datasource.
var Doc = refdoc.Datasource{
	Name:         "docker",
	NameForm:     "image reference",
	Endpoints:    "registry `tags/list` (Docker Hub with page size 10000, others 1000; at most 20 pages, 1000 for ghcr.io, quay.io, cgr.dev and the Red Hat registries, which list oldest first or per build), manifest HEAD for digests (index digest for multi-platform images); gcr.io and Artifact Registry (also behind registry.k8s.io) list each tag's digest with it, so their digests need no HEAD; elsewhere, for release times, a manifest GET by tag (Docker Hub: by the HEAD's digest, once per digest)",
	Auth:         "Docker config (`$DOCKER_CONFIG/config.json`, default `~/.docker/config.json`): `credHelpers`, else the `credsStore` helper when configured, else `auths`, namespace keys (`ghcr.io/org`) first; Basic, or Bearer tokens from the registry's token endpoint (anonymous without credentials)",
	ReleaseTimes: "on gcr.io and Artifact Registry the `tags/list` times (`timeCreatedMs`, else `timeUploadedMs`, as for indexes); elsewhere, for an index whose first platform descriptor is annotated with `org.opencontainers.image.created`, that annotation (the build start, slightly before the config `created`; no platform manifest or config is fetched); else the image config `created`; else, and for non-image artifacts (Helm charts), the manifest annotation `org.opencontainers.image.created`",
}
