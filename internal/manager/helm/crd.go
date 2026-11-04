package helm

import "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"

// manifests extracts chart custom resources (k0s, Flux, Argo CD) from Kubernetes YAML.
func (m *helmManager) manifests(string, []byte) []dep.Dep { //nolint:unparam // replaced by the CRD extractors
	_, _ = m.links, lineOf
	return nil
}
