package helmds

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// syntheticIndex returns a block-style index.yaml shaped like a large public repository's
// (bitnami): charts with versions carrying annotations, dependencies and maintainers.
func syntheticIndex(charts, versions int) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nentries:\n")
	for c := range charts {
		name := fmt.Sprintf("chart-%03d", c)
		fmt.Fprintf(&b, "  %s:\n", name)
		for v := range versions {
			fmt.Fprintf(&b, "  - annotations:\n      category: Database\n      images: |\n        - name: %s\n          image: docker.io/bitnami/%s:1.%d.0-debian-12-r0\n", name, name, v)
			b.WriteString("      licenses: Apache-2.0\n    apiVersion: v2\n")
			fmt.Fprintf(&b, "    appVersion: 1.%d.0\n    created: \"2025-08-21T03:20:%02d.91572711Z\"\n", v, v%60)
			b.WriteString("    dependencies:\n    - name: common\n      repository: oci://registry-1.docker.io/bitnamicharts\n      tags:\n      - bitnami-common\n      version: 2.x.x\n")
			b.WriteString("    description: A synthetic chart with a description long enough to wrap onto a\n      second line like the ones helm repo index writes.\n")
			fmt.Fprintf(&b, "    digest: %064x\n    home: https://bitnami.com\n    keywords:\n    - database\n", c*versions+v)
			b.WriteString("    maintainers:\n    - name: Broadcom, Inc. All Rights Reserved.\n      url: https://github.com/bitnami/charts\n")
			fmt.Fprintf(&b, "    name: %s\n    urls:\n    - oci://registry-1.docker.io/bitnamicharts/%s:%d.0.0\n    version: %d.0.0\n", name, name, v, v)
		}
	}
	b.WriteString("generated: \"2025-08-21T03:20:00Z\"\n")
	return []byte(b.String())
}

// BenchmarkScanIndex looks up one chart of a 30 MB index, as a lookup of one dependency does.
func BenchmarkScanIndex(b *testing.B) {
	data := syntheticIndex(300, 100)
	wanted := map[string]bool{"chart-150": true}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		out, err := scanIndex(bytes.NewReader(data), wanted)
		if err != nil || len(out["chart-150"]) != 100 {
			b.Fatal(err, len(out["chart-150"]))
		}
	}
}
