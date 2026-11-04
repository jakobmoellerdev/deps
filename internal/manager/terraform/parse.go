package terraform

import (
	"regexp"
	"strings"
)

// matchRe matches the configuration files of the terraform manager.
var matchRe = regexp.MustCompile(`\.(tf|tofu)(\.json)?$`)

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
