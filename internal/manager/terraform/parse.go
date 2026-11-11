package terraform

import (
	"bytes"
	"slices"
	"strings"
)

// bom is the UTF-8 byte order mark, which editors on Windows write before a configuration's
// first byte; Terraform skips it.
var bom = []byte("\ufeff")

// blankBOM returns src with a leading byte order mark replaced by blanks, which a JSON
// decoder reads as the whitespace before the document. Blanking rather than trimming keeps
// every byte offset, and so every span, that of src.
func blankBOM(src []byte) []byte {
	if !bytes.HasPrefix(src, bom) {
		return src
	}
	out := slices.Clone(src)
	for i := range bom {
		out[i] = ' '
	}
	return out
}

// isConfigFile reports whether name is a configuration file of the terraform manager
// (.tf, .tofu, .tf.json, .tofu.json).
func isConfigFile(name string) bool {
	name = strings.TrimSuffix(name, ".json")
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tofu")
}

// parseConfig parses a configuration file into its body.
func parseConfig(file string, src []byte) (body, error) {
	if strings.HasSuffix(file, ".json") {
		return parseJSON(src)
	}
	return parseHCL(src)
}

// isTofu reports whether file is an OpenTofu-only configuration file, whose hostless
// addresses and required_version refer to OpenTofu.
func isTofu(file string) bool {
	return strings.HasSuffix(file, ".tofu") || strings.HasSuffix(file, ".tofu.json")
}
