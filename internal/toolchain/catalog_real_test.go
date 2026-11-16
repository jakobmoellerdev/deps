package toolchain

import (
	"net/http"
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
		Erlang:    "27",     // a major, so Resolve picks the newest build
		Elixir:    "1.18",   // a line; Resolve pairs it with an OTP major
		Gleam:     "1.19.2",
		JB:        "0.6.0",
		// Self-digested; the versions must be ones digests.json covers.
		Ruby: PinnedVersions(Ruby)[0],
		PHP:  PinnedVersions(PHP)[0],
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
			if e.Download == nil {
				// A build-only entry (jb) has no artifact to resolve; `go install` fetches
				// and verifies it through the module proxy and the checksum database.
				return
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
			// The artifact itself, not only its checksum: a publisher can keep the checksum
			// file of a release while dropping the archive (Apache's dlcdn mirror does), and
			// a checksum-only check would call that healthy.
			req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, d.URL, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := m.client().Do(req)
			if err != nil {
				t.Fatalf("HEAD %s: %v", d.URL, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("HEAD %s: HTTP %d", d.URL, resp.StatusCode)
			}
		})
	}
}
