package nix

import (
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/download"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// sriPrefix starts a sha256 SRI hash (`sha256-<base64>`).
const sriPrefix = "sha256-"

// downloads returns the deps of the fixed-output downloads of a flake.nix: a dep for every
// set with a `url` the download locator supports (other than an input's, in inputs) and a
// `hash` or `sha256`, skipped without one; and a dep for each `version` attribute equal to
// the version of such a download in the same or a nested set, which moves with it.
func downloads(file string, src []byte, env manager.Env, inputs []binding) []dep.Dep {
	sets := parseSets(src)
	var out []dep.Dep
	var of []int // per dep of out, the index of its set
	for i, s := range sets {
		u, ok := s.attr("url")
		if !ok || u.span == (dep.Span{}) || slices.ContainsFunc(inputs, func(in binding) bool { return in.span == u.span }) {
			continue
		}
		h, hashed := s.attr("hash")
		if !hashed {
			h, hashed = s.attr("sha256")
		}
		d := dep.Dep{Manager: Name, DepType: DepTypeDownload, File: file, Line: u.line}
		if !download.Locate(&d, u.value, u.span.Start, env) {
			if hashed {
				out = append(out, dep.Dep{Manager: Name, DepType: DepTypeDownload, File: file, Line: u.line, Name: u.value, Skip: skipDownloadURL})
				of = append(of, i)
			}
			continue
		}
		switch sum, ok := parseHash(h.value); {
		case !hashed:
			d.Skip = skipNoHash
		case s.unpacked || s.callee != "fetchurl" && strings.HasPrefix(s.callee, "fetch"):
			d.Skip = skipUnpacked
		case !ok:
			d.Skip = skipHashFormat
		default:
			d.Checksum, d.ChecksumSpan = sum, h.span
		}
		out, of = append(out, d), append(of, i)
	}
	n := len(out)
	for i, s := range sets {
		v, ok := s.attr("version")
		if !ok || v.span == (dep.Span{}) {
			continue
		}
		for j, d := range out[:n] {
			if d.Skip == "" && d.Version == v.value && within(sets, of[j], i) {
				if f, ok := download.Follow(d, DepTypeVersion, v.value, v.span.Start, v.line); ok {
					out = append(out, f)
				}
				break
			}
		}
	}
	return out
}

// parseHash returns the hex sha256 digest of a `hash` or `sha256` value: 64 hex characters,
// or a canonical SRI hash (`sha256-<base64 of 32 bytes>`); false for other forms (nix32,
// `sha256:` prefixes, other algorithms).
func parseHash(s string) (string, bool) {
	if manager.IsSHA256(s) {
		return s, true
	}
	b64, ok := strings.CutPrefix(s, sriPrefix)
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != b64 {
		return "", false
	}
	return hex.EncodeToString(b), true
}

// sri encodes a hex sha256 digest as an SRI hash.
func sri(sum string) string {
	b, err := hex.DecodeString(sum)
	if err != nil {
		return sum
	}
	return sriPrefix + base64.StdEncoding.EncodeToString(b)
}

// downloadEdits replaces every occurrence of the version in the URL (or version attribute) and
// the hash, written back in its form: hex, or SRI when the file's hash is longer than the hex
// digest the dep carries.
func downloadEdits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		for _, s := range [...]dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
			if s.End > s.Start {
				edits = append(edits, patch.Edit{Start: s.Start, End: s.End, Old: d.Version, New: u.NewVersion})
			}
		}
	}
	if d.ChecksumSpan.End-d.ChecksumSpan.Start != len(d.Checksum) {
		u.Dep.Checksum = sri(d.Checksum)
		if u.NewChecksum != "" {
			u.NewChecksum = sri(u.NewChecksum)
		}
	}
	return manager.ChecksumEdits(edits, u)
}

// isDownload reports whether d is a download or its version attribute, not a flake input.
func isDownload(d dep.Dep) bool { return d.DepType == DepTypeDownload || d.DepType == DepTypeVersion }
