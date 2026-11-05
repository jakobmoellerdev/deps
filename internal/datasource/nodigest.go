package datasource

import (
	"fmt"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// UnsupportedDigest is the error a datasource named name returns from Digest when its
// releases are not digest-pinned.
func UnsupportedDigest(name string, d dep.Dep) error {
	return fmt.Errorf("%s: digests are not supported for %s", name, d.Name)
}
