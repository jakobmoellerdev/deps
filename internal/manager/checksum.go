package manager

import (
	"errors"
	"regexp"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

var sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// IsSHA256 reports whether s is a hex-encoded SHA-256 sum.
func IsSHA256(s string) bool { return sha256Re.MatchString(s) }

// ChecksumEdits checks that the checksum of u changes together with its version and
// appends the checksum edit, if the checksum changes, to edits. A dep pins a checksum
// when its ChecksumSpan is non-empty.
func ChecksumEdits(edits []patch.Edit, u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	hasChecksum := d.ChecksumSpan.End > d.ChecksumSpan.Start
	switch {
	case u.NewChecksum != "" && !hasChecksum:
		return nil, errors.New("dependency has no checksum to update")
	case u.NewChecksum == "" && hasChecksum && u.NewVersion != d.Version:
		return nil, errors.New("checksum must be updated together with the version, but no checksum was resolved")
	}
	if u.NewChecksum != "" && u.NewChecksum != d.Checksum {
		edits = append(edits, patch.Edit{Start: d.ChecksumSpan.Start, End: d.ChecksumSpan.End, Old: d.Checksum, New: u.NewChecksum})
	}
	return edits, nil
}
