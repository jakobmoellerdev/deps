package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releasesJSON renders a GitHub REST release list; assets maps a tag to its asset digests.
func releasesJSON(tags []string, assets map[string]map[string]string) string {
	rels := make([]string, len(tags))
	for i, tag := range tags {
		var as []string
		for name, digest := range assets[tag] {
			as = append(as, fmt.Sprintf(`{"name":%q,"digest":%q}`, name, digest))
		}
		rels[i] = fmt.Sprintf(`{"tag_name":%q,"draft":false,"prerelease":false,"published_at":"2026-01-0%dT00:00:00Z","assets":[%s]}`,
			tag, len(tags)-i, strings.Join(as, ","))
	}
	return "[" + strings.Join(rels, ",") + "]"
}

func serveBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }
}

// M5: OCM's sigstore handler .env keeps the cosign fork version and the sha256 of the
// release's cosign_checksums.txt side by side; one custom regex entry with the fork's regex
// versioning moves both together (docs/examples-ocm.deps.yaml, cosign entry and rule 13).
func TestApplyCosignVersionAndChecksum(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	oldSum, newSum := strings.Repeat("a", 64), strings.Repeat("b", 64)
	f.mux.HandleFunc("GET api.github.com/repos/open-component-model/cosign/releases", serveBody(releasesJSON(
		[]string{"v3.1.4+ocm.6", "v3.1.4+ocm.5", "v3.1.3+ocm.9", "v3.1.4"},
		map[string]map[string]string{
			"v3.1.4+ocm.6": {"cosign_checksums.txt": "sha256:" + newSum, "cosign-linux-amd64": "sha256:" + strings.Repeat("c", 64)},
			"v3.1.4+ocm.5": {"cosign_checksums.txt": "sha256:" + oldSum},
		})))
	dir := t.TempDir()
	const env = "bindings/go/sigstore/signing/handler/internal/.env"
	src := "# cosign fork\nCOSIGN_VERSION=v3.1.4+ocm.5\nCOSIGN_CHECKSUMS_SHA256=" + oldSum + "\nOTHER=1\n"
	writeFiles(t, dir, map[string]string{env: src, ".deps.yaml": `custom:
  - files: ['bindings/go/sigstore/signing/handler/internal/.env']
    match: 'COSIGN_VERSION=(?P<version>v\d+\.\d+\.\d+\+ocm\.\d+)\s+COSIGN_CHECKSUMS_SHA256=(?P<checksum>[0-9a-f]{64})'
    datasource: github-release-attachments
    name: open-component-model/cosign
    checksum: {asset: cosign_checksums.txt}
rules:
  - names: ['open-component-model/cosign']
    group: OCM cosign
    versioning: 'regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$'
`})
	stdout, stderr, code := execute(t, "plan", "--dir", dir)
	if code != 0 {
		t.Fatalf("plan exit %d\n%s", code, stderr)
	}
	for _, want := range []string{"-COSIGN_VERSION=v3.1.4+ocm.5\n-COSIGN_CHECKSUMS_SHA256=" + oldSum + "\n", "+COSIGN_VERSION=v3.1.4+ocm.6\n+COSIGN_CHECKSUMS_SHA256=" + newSum + "\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "update  "+env+":2  open-component-model/cosign  v3.1.4+ocm.5 -> v3.1.4+ocm.6  (patch)\n") ||
		!strings.Contains(stderr, "1 updates in 1 files, 0 skipped, 0 errors\n") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if _, stderr, code := execute(t, "apply", "--dir", dir); code != 0 {
		t.Fatalf("apply exit %d\n%s", code, stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(env)))
	if err != nil {
		t.Fatal(err)
	}
	if want := "# cosign fork\nCOSIGN_VERSION=v3.1.4+ocm.6\nCOSIGN_CHECKSUMS_SHA256=" + newSum + "\nOTHER=1\n"; string(got) != want {
		t.Errorf(".env = %q, want %q", got, want)
	}
}

const envtestReleases = `releases:
  v1.35.0: {envtest-v1.35.0-linux-amd64.tar.gz: {hash: aa}}
  v1.36.0: {envtest-v1.36.0-linux-amd64.tar.gz: {hash: bb}}
  v1.37.0: {envtest-v1.37.0-linux-amd64.tar.gz: {hash: cc}}
  v1.38.0-rc.0: {}
`

const nodeIndex = `[
{"version":"v27.0.0-alpha.0.0.0","date":"2026-10-29","files":["linux-x64"],"lts":false},
{"version":"v27.1.0","date":"2026-10-20","files":["linux-x64"],"lts":false},
{"version":"v26.13.0","date":"2026-09-28","files":["linux-x64"],"lts":"Iron"},
{"version":"v26.5.0","date":"2026-05-01","files":["linux-x64"],"lts":"Iron"}
]`

// M3 + M4: OCM's Renovate markers for the envtest custom datasource (controller .env and
// envtest.go) and node-version (netlify.toml), translated as in docs/examples-ocm.deps.yaml.
// Each document is fetched once although three deps use them.
func TestPlanEnvtestAndNodeVersion(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	hits := map[string]int{}
	count := func(name, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			f.mu.Lock()
			hits[name]++
			f.mu.Unlock()
			io.WriteString(w, body)
		}
	}
	f.mux.HandleFunc("GET envtest.test/envtest-releases.yaml", count("envtest", envtestReleases))
	f.mux.HandleFunc("GET node.test/mirror/index.json", count("node", nodeIndex))
	t.Setenv("DEPS_NODE_DIST_URL", "https://node.test/mirror")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"bindings/go/kubernetes/controller/.env": "# renovate: datasource=custom.envtest depName=envtest\nENVTEST_K8S_VERSION=1.36.0\nENVTEST_VERSION=release-0.21\n" +
			"# renovate: datasource=custom.unknown depName=x\nX=1.0.0\n",
		"bindings/go/kubernetes/controller/internal/test/envtest.go": "package test\n\n// renovate: datasource=custom.envtest depName=envtest\nconst DefaultEnvTestVersion = \"1.36.0\"\n",
		"website/netlify.toml": "[build.environment]\n  # renovate: datasource=node-version depName=node\n  NODE_VERSION = \"26.5.0\"\n  # renovate: datasource=node depName=node\n  NODE_V = \"v26.5.0\"\n",
		".deps.yaml": `customDatasources:
  envtest:
    url: https://envtest.test/envtest-releases.yaml
    format: yaml
    versions: .releases | keys
    versionRegex: '^v?(?P<version>\d+\.\d+\.\d+)$'
custom:
  - files: ['**/.env', '**/netlify.toml', '**/envtest.go']
ignorePaths: []
`,
	})
	stdout, stderr, code := execute(t, "plan", "--show-skipped", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"+ENVTEST_K8S_VERSION=1.37.0\n",
		"+const DefaultEnvTestVersion = \"1.37.0\"\n",
		"+  NODE_VERSION = \"26.13.0\"\n",
		"+  NODE_V = \"v26.13.0\"\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff lacks %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{
		"update  bindings/go/kubernetes/controller/.env:2  envtest  1.36.0 -> 1.37.0  (minor)\n",
		"update  bindings/go/kubernetes/controller/internal/test/envtest.go:4  envtest  1.36.0 -> 1.37.0  (minor)\n",
		"update  website/netlify.toml:3  node  26.5.0 -> 26.13.0  (minor)\n",
		"update  website/netlify.toml:3  node  26.5.0 -> 27.1.0  (major, separate PR)\n",
		"update  website/netlify.toml:5  node  v26.5.0 -> v26.13.0  (minor)\n",
		"update  website/netlify.toml:5  node  v26.5.0 -> v27.1.0  (major, separate PR)\n",
		`skip    bindings/go/kubernetes/controller/.env:5  x  unknown datasource "custom.unknown"` + "\n",
		"6 updates in 3 files, 1 skipped, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if hits["envtest"] != 1 || hits["node"] != 1 {
		t.Errorf("requests = %v, want one per document", hits)
	}
}

// Setup-action version inputs (M7) without "v" are written back without it, both for the
// in-PR (Rank) and the separate major update; partial values keep their precision. pin:
// digest leaves them alone.
func TestPlanSetupInputsKeepStyle(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	f.mux.HandleFunc("GET api.github.com/repos/helm/helm/releases", serveBody(releasesJSON([]string{"v4.0.0", "v3.17.0", "v3.16.0"}, nil)))
	f.mux.HandleFunc("GET node.test/dist/index.json", serveBody(nodeIndex))
	t.Setenv("DEPS_NODE_DIST_URL", "https://node.test/dist")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".github/workflows/ci.yml": `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: azure/setup-helm@v4
        with:
          version: 3.16.0
      - uses: actions/setup-node@v4
        with:
          node-version: 26
`,
		// pin: digest must not try to pin setup inputs: they are values, not refs.
		".deps.yaml": "pin: digest\nrules: [{managers: [github-actions], depTypes: [action], ignore: true}]\n",
	})
	stdout, stderr, code := execute(t, "plan", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{"+          version: 3.17.0\n", "+          node-version: 27\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff lacks %q:\n%s", want, stdout)
		}
	}
	for _, want := range []string{
		"update  .github/workflows/ci.yml:8  helm/helm  3.16.0 -> 3.17.0  (minor)\n",
		"update  .github/workflows/ci.yml:8  helm/helm  3.16.0 -> 4.0.0  (major, separate PR)\n",
		"update  .github/workflows/ci.yml:11  node  26 -> 27  (major)\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}
