package toolchain

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// TestCatalogResolvesRealDownloads asks every catalog entry for a download of a released
// version and checks that the publisher answers with an artifact and a checksum of the
// expected length. It makes no download, so it is cheap, but it is the only check that the
// URL shapes and checksum artifacts are still the ones the publishers serve.
func TestCatalogResolvesRealDownloads(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	t.Parallel()
	versions := map[Tool]string{
		Go:        "1.25",  // a line, so Resolve is exercised too
		Node:      "lts/*", // an LTS line
		Bun:       "1.1.38",
		Deno:      "2.1.4",
		UV:        "0.5.11",
		Rust:      "1.83.0",
		Java:      "21",
		Gradle:    "8.10.2",
		Maven:     "3.9.9",
		Terraform: "1.9.8",
		OpenTofu:  "1.8.6",
		Julia:     "1.11.2",
		Dart:      "3.6.0",
		Flutter:   "3.27.1",
		DotNet:    "9.0",
		Bazelisk:  "1.30.0", // the first releases shipped no checksum asset
	}
	for _, tool := range Tools() {
		t.Run(string(tool), func(t *testing.T) {
			t.Parallel()
			version, ok := versions[tool]
			if !ok {
				t.Fatalf("no version in the test table for %s", tool)
			}
			m := &Manager{Client: httpx.NewClient(), GOOS: "linux", GOARCH: "amd64"}
			e := Catalog[tool]
			v, err := m.resolve(t.Context(), e, version)
			if err != nil {
				t.Fatalf("resolve %s: %v", version, err)
			}
			d, err := e.Download(t.Context(), m, v, "linux", "amd64")
			if err != nil {
				t.Fatalf("download %s: %v", v, err)
			}
			if !strings.HasPrefix(d.URL, "https://") {
				t.Errorf("URL = %q", d.URL)
			}
			want := 64
			if d.Hash == SHA512 {
				want = 128
			}
			if len(d.Sum) != want || !isHex(d.Sum) {
				t.Errorf("%s checksum = %q, want %d hex characters", d.Hash, d.Sum, want)
			}
		})
	}
}
