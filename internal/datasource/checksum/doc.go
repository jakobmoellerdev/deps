package checksum

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the checksum lookups.
var Doc = refdoc.Section{
	Title: "Checksums",
	Body: "`checksum.asset` reads the asset digest from the already-fetched GitHub release list. `checksum.url` is downloaded once per URL, with a 1 MiB cap. " +
		"Where the repository publishes no SHA-256 file (the Maven Wrapper on Maven Central), the artifact itself is hashed: it is streamed through SHA-256 or SHA-512 (by the length of the pinned checksum), once per URL and algorithm, with a 256 MiB cap.",
}
