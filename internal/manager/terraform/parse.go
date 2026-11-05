package terraform

import "strings"

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
