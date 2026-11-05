package dockerds

import "time"

// Media types of the manifests and configs deps reads.
const (
	mediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex           = "application/vnd.oci.image.index.v1+json"
	mediaTypeOCIConfig          = "application/vnd.oci.image.config.v1+json"
	mediaTypeOCIArtifact        = "application/vnd.oci.artifact.manifest.v1+json"
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerConfig       = "application/vnd.docker.container.image.v1+json"
)

// manifestAccept is the Accept header of manifest requests: every manifest type, so that the
// registry answers a tag with the index of a multi-platform image rather than converting it.
const manifestAccept = mediaTypeDockerManifest + ", " + mediaTypeDockerManifestList + ", " +
	mediaTypeOCIManifest + ", " + mediaTypeOCIIndex + ", " + mediaTypeOCIArtifact

// annotationCreated is the OCI annotation of an artifact's creation time (RFC 3339).
const annotationCreated = "org.opencontainers.image.created"

// descriptor references content by media type, digest and size.
type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

// manifest is an OCI image manifest or Docker v2 manifest.
type manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// index is an OCI image index or Docker manifest list.
type index struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Manifests     []descriptor      `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// imageConfig is the part of an image config deps reads: its creation time and labels.
type imageConfig struct {
	Created *time.Time `json:"created,omitempty"`
	Config  struct {
		Labels map[string]string `json:"Labels,omitempty"`
	} `json:"config"`
}

// isManifest reports whether mediaType is a manifest (fetched from /manifests/) rather than a
// blob.
func isManifest(mediaType string) bool {
	switch mediaType {
	case mediaTypeOCIManifest, mediaTypeOCIIndex, mediaTypeOCIArtifact, mediaTypeDockerManifest, mediaTypeDockerManifestList:
		return true
	}
	return false
}
