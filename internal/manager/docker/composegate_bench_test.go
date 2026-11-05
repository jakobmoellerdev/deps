package docker

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func BenchmarkExtractComposeGateNonComposeYAML(b *testing.B) {
	files := []string{
		"deploy/charts/app/values.yaml",
		"config/settings.yml",
		"k8s/overlays/production/kustomization.yaml",
		"docs/mkdocs.yml",
		".golangci.yml",
	}
	src := []byte("name: app\nreplicas: 3\nlabels:\n  team: core\n")
	b.ReportAllocs()
	for b.Loop() {
		for _, f := range files {
			if _, err := New().Extract(f, src, manager.Env{}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
