package engine

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// namedReleases lists release names and records the names passed to the per-version lookups.
type namedReleases struct {
	names []string
	mu    sync.Mutex
	asked []string // "<lookup> <name>"
}

func (f *namedReleases) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	rels := make([]Release, len(f.names))
	for i, n := range f.names {
		rels[i] = Release{Version: n}
	}
	return rels, nil
}

func (f *namedReleases) record(s string) {
	f.mu.Lock()
	f.asked = append(f.asked, s)
	f.mu.Unlock()
}

func (f *namedReleases) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	f.record("digest " + v)
	return "", nil
}

func (f *namedReleases) ReleaseTime(_ context.Context, _ dep.Dep, v string) (time.Time, error) {
	f.record("time " + v)
	return time.Now().Add(-30 * 24 * time.Hour), nil
}

// releaseChecksums records the version and release name of each checksum lookup.
type releaseChecksums struct {
	mu  sync.Mutex
	got []string
}

func (c *releaseChecksums) Checksum(_ context.Context, _ dep.Dep, v, release string) (string, error) {
	c.mu.Lock()
	c.got = append(c.got, v+" "+release)
	c.mu.Unlock()
	return strings.Repeat("b", 64), nil
}

func TestResolveExtractVersion(t *testing.T) {
	h := strings.Repeat("a", 64)
	const cfgYAML = `minimumReleaseAge: 1 day
custom:
  - files: [Dockerfile]
    match: 'ARG (?P<name>[\w/]+)_VERSION=(?P<version>\S+)\nARG \w+_SHA256=(?P<checksum>[0-9a-f]+)'
    datasource: github-releases
    checksum: {asset: 'jq-{{version}}-linux-amd64'}
rules:
  - {manager: custom, names: [jqlang/jq], extractVersion: '^jq-(?<version>\d.*)$'}
  - {names: [o/kustomize], extractVersion: '^kustomize/v(?<version>\d.*)$'}
`
	src := "ARG jqlang/jq_VERSION=1.7.1\nARG X_SHA256=" + h + "\n" +
		"ARG o/kustomize_VERSION=5.4.3\nARG X_SHA256=" + h + "\n"
	root, cfg := setup(t, cfgYAML, map[string]string{"Dockerfile": src})
	ms, err := Managers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 9.9.9 and api/v0.19.0 match neither expression: they are not candidates.
	ds := &namedReleases{names: []string{"9.9.9", "jq-1.8.1", "jq-1.7.1", "kustomize/v5.5.0", "kustomize/v5.4.3", "api/v0.19.0"}}
	cs := &releaseChecksums{}
	o := Options{Managers: ms, Datasources: map[string]Datasource{"github-releases": ds}, Checksums: cs}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) != 0 {
		t.Fatal(err, res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, u.Dep.Name+" "+u.NewVersion)
	}
	if want := []string{"jqlang/jq 1.8.1", "o/kustomize 5.5.0"}; !slices.Equal(got, want) {
		t.Fatalf("updates = %q, want %q", got, want)
	}
	// Lookups by version go to the datasource under the release name; templates get the
	// version as written.
	slices.Sort(cs.got)
	if want := []string{"1.8.1 jq-1.8.1", "5.5.0 kustomize/v5.5.0"}; !slices.Equal(cs.got, want) {
		t.Errorf("checksum lookups = %q, want %q", cs.got, want)
	}
	for _, a := range ds.asked {
		if name := a[strings.IndexByte(a, ' ')+1:]; !strings.HasPrefix(name, "jq-") && !strings.HasPrefix(name, "kustomize/") {
			t.Errorf("lookup %q by extracted version, want release name", a)
		}
	}
	if !slices.Contains(ds.asked, "time jq-1.8.1") {
		t.Errorf("release time of jq-1.8.1 not asked: %q", ds.asked)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) != 0 || len(changes) != 1 {
		t.Fatalf("Build = %v, %v", changes, errs)
	}
	b := strings.Repeat("b", 64)
	if want := "ARG jqlang/jq_VERSION=1.8.1\nARG X_SHA256=" + b + "\nARG o/kustomize_VERSION=5.5.0\nARG X_SHA256=" + b + "\n"; string(changes[0].New) != want {
		t.Errorf("Build:\n%s\nwant:\n%s", changes[0].New, want)
	}
}

// An action in a monorepo subdirectory is versioned by its per-action tags when the
// repository has any (grafana/shared-workflows tags actions/get-vault-secrets
// `get-vault-secrets/v1.3.1`), written as in the file with or without the prefix and
// pinned by the prefixed tag's commit; repo-wide tags (v9.0.0) are not its versions. An
// action whose repository has no such tags keeps the repository's tags.
func TestResolveActionMonorepoTags(t *testing.T) {
	const (
		sw     = "https://github.com/grafana/shared-workflows"
		codeql = "https://github.com/github/codeql-action"
		vault  = "grafana/shared-workflows/actions/get-vault-secrets"
	)
	monorepo := fakeTags{
		sw: {"v9.0.0", "get-vault-secrets/v1.3.1", "get-vault-secrets/v1.4.0", "dockerhub-login/v1.0.2",
			"dockerhub-login/v1.0.4", "get-vault-secrets-v2.0.0"},
		codeql: {"v3.1.0", "v3.2.0"},
	}
	wf := "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n" +
		"      - uses: " + vault + "@" + sha(sw+"@get-vault-secrets/v1.3.1") + " # v1.3.1\n" +
		"      - uses: grafana/shared-workflows/actions/dockerhub-login@dockerhub-login/v1.0.2\n" +
		"      - uses: github/codeql-action/init@v3.1.0\n"
	root, cfg := setup(t, "", map[string]string{".github/workflows/ci.yml": wf})
	res, err := Resolve(t.Context(), root, cfg, env, sources(monorepo, nil))
	if err != nil || len(res.Errors) != 0 {
		t.Fatal(err, res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, u.Dep.Name+" "+u.NewVersion+" "+u.Dep.ExtractVersion)
	}
	want := []string{
		vault + " v1.4.0 " + `^(?:actions/get-vault-secrets|get-vault-secrets)/(?<version>.+)$`,
		"grafana/shared-workflows/actions/dockerhub-login v1.0.4 " + `^(?:actions/dockerhub-login|dockerhub-login)/(?<version>.+)$`,
		"github/codeql-action/init v3.2.0 ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %q, want %q", got, want)
	}
	updates, _ := Local(res.Updates)
	changes, errs := Build(t.Context(), root, managers, updates, nil)
	if len(errs) != 0 || len(changes) != 1 {
		t.Fatalf("Build = %v, %v", changes, errs)
	}
	for n, want := range map[int]string{
		6: "      - uses: " + vault + "@" + sha(sw+"@get-vault-secrets/v1.4.0") + " # v1.4.0",
		7: "      - uses: grafana/shared-workflows/actions/dockerhub-login@dockerhub-login/v1.0.4",
		8: "      - uses: github/codeql-action/init@v3.2.0",
	} {
		if l := line(n, string(changes[0].New)); l != want {
			t.Errorf("line %d:\n got %s\nwant %s", n, l, want)
		}
	}
}
