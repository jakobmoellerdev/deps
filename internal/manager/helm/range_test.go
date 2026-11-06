package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// TestLockerRangeUpdate rewrites the `common` range of the helm-generated testdata/aliased
// chart: its lock entry takes the update's NewVersion and the digest follows.
func TestLockerRangeUpdate(t *testing.T) {
	chart, lock := readTestdata(t, chartFile), readTestdata(t, lockFile)
	dir := chartDir(t, chart, lock)
	patched := strings.Replace(chart, `version: ">=2.0.0 <3"`, `version: ">=3.1.0 <4"`, 1)
	ups := map[string][]dep.Update{chartFile: {{Dep: dep.Dep{Name: "common", Version: "2.0.0", Range: ">=2.0.0 <3"}, NewVersion: "3.1.0"}}}
	out, err := Locker{}.ProcessUpdates(t.Context(), dir, map[string][]byte{chartFile: []byte(patched)}, map[string][]byte{lockFile: []byte(lock)}, ups, nil)
	if err != nil {
		t.Fatal(err)
	}
	reqs, _ := chartRequirements([]byte(patched))
	l, _ := parseLock([]byte(lock))
	l.deps[2].Version = "3.1.0"
	digest, err := hashReq(resolveAliases(reqs, l.deps), l.deps)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer("version: 2.41.0", "version: 3.1.0", l.digest.Value, digest).Replace(lock)
	if got := string(out[lockFile]); got != want {
		t.Fatalf("Chart.lock =\n%s\nwant\n%s", got, want)
	}
	// Without its update the range keeps its locked version.
	out, err = Locker{}.Process(t.Context(), dir, map[string][]byte{chartFile: []byte(patched)}, map[string][]byte{lockFile: []byte(lock)})
	if err != nil || !strings.Contains(string(out[lockFile]), "version: 2.41.0") {
		t.Errorf("no update: %s, %v", out[lockFile], err)
	}
}

func TestFluxGitRepository(t *testing.T) {
	files := map[string]string{"clusters/git.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: podinfo
spec:
  url: https://github.com/stefanprodan/podinfo
  ref:
    tag: "6.7.1"
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: ssh://git@gitlab.com/group/repo.git
  ref: {branch: main}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: https://github.com/org/pinned
  ref: {tag: v1.0.0, commit: 0123456789012345678901234567890123456789}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: https://github.com/org/ranged
  ref: {semver: ">=1.0.0"}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: https://github.com/org/named
  ref: {name: refs/tags/v1}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: https://github.com/org/default
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: https://github.com/org/templated
  ref: {tag: "{{ .tag }}"}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: nohost
  ref: {tag: v1}
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: ""
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
spec:
  url: https://github.com/org/escaped
  ref: {tag: "v1.\u0030"}
`}
	check(t, linked(t, New(), files), []string{
		"clusters/git.yaml:8 flux stefanprodan/podinfo https://github.com/stefanprodan/podinfo 6.7.1 ",
		"clusters/git.yaml:13 flux group/repo https://gitlab.com/group/repo  non-version ref",
		"clusters/git.yaml:19 flux org/pinned https://github.com/org/pinned  commit ref",
		"clusters/git.yaml:25 flux org/ranged https://github.com/org/ranged  unsupported range",
		"clusters/git.yaml:31 flux org/named https://github.com/org/named  non-version ref",
		"clusters/git.yaml:37 flux org/default https://github.com/org/default  non-version ref",
		"clusters/git.yaml:43 flux org/templated https://github.com/org/templated {{ .tag }} template value",
		"clusters/git.yaml:48 flux nohost   unrecognized git URL",
		"clusters/git.yaml:60 flux org/escaped https://github.com/org/escaped v1.0 cannot locate value",
	})
	deps := linkedDeps(t, New(), files)[0]
	e, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "6.8.0"})
	if err != nil || len(e) != 1 || e[0].Old != "6.7.1" || e[0].New != "6.8.0" {
		t.Errorf("edits = %+v, %v", e, err)
	}
}

func TestFluxRegistryAliases(t *testing.T) {
	files := map[string]string{"apps/release.yaml": `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
spec:
  chart:
    spec:
      chart: podinfo
      version: 6.5.0
      sourceRef: {kind: HelmRepository, name: podinfo}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: redis
spec:
  chart:
    spec:
      chart: redis
      version: 18.0.0
      sourceRef: {kind: HelmRepository, name: bitnami}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: other
spec:
  chart:
    spec:
      chart: other
      version: 1.0.0
      sourceRef: {kind: HelmRepository, name: nope}
`}
	env := manager.Env{RegistryAliases: map[string]string{"podinfo": "https://stefanprodan.github.io/podinfo/", "bitnami": "oci://registry-1.docker.io/bitnamicharts"}}
	check(t, linkedEnv(t, New(), files, env), []string{
		"apps/release.yaml:9 flux podinfo https://stefanprodan.github.io/podinfo 6.5.0 ",
		"apps/release.yaml:20 flux redis oci://registry-1.docker.io/bitnamicharts 18.0.0 ",
		"apps/release.yaml:31 flux other  1.0.0 unknown HelmRepository /nope",
	})
}

// TestChartAliases resolves Chart.yaml repository aliases: registryAliases at extraction, a
// repositories.yaml in Link, then the Chart.lock and Renovate's default `stable` in ReadLocks.
func TestChartAliases(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"charts/app/Chart.yaml": `apiVersion: v2
name: app
version: 0.1.0
dependencies:
  - {name: cert-manager, version: v1.14.0, repository: "@jetstack"}
  - {name: redis, version: 17.11.3, repository: "@bitnami"}
  - {name: legacy, version: 1.0.0, repository: "@stable"}
  - {name: mine, version: 1.0.0, repository: "alias:mine"}
  - {name: twice, version: 1.0.0, repository: "@dup"}
  - {name: nowhere, version: 1.0.0, repository: "@nowhere"}
  - {name: locked-twice, version: 1.0.0, repository: "@lt"}
`,
		"charts/app/requirements.yaml": `dependencies:
  - {name: old, version: 1.0.0, repository: "@stable"}
`,
		"config/repositories.yaml": `apiVersion: ""
generated: "2026-01-01T00:00:00Z"
repositories:
  - {name: mine, url: "https://charts.example.com/mine/"}
  - {name: dup, url: https://a.example.com}
  - {name: bad, url: s3://bucket}
`,
		"other/repositories.yaml": `repositories:
  - {name: dup, url: https://b.example.com}
`,
		"flux/repositories.yaml": `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata: {name: x}
spec: {url: https://x.example.com}
`,
		"broken/repositories.yaml": "repositories: [\n",
	}
	for p, s := range files {
		write(p, s)
	}
	write("charts/app/Chart.lock", `dependencies:
- name: cert-manager
  repository: https://charts.jetstack.io
  version: v1.14.0
- name: legacy
  repository: https://legacy.example.com
  version: 1.0.0
- name: locked-twice
  repository: https://a.example.com
  version: 1.0.0
- name: locked-twice
  repository: https://b.example.com
  version: 1.0.0
digest: sha256:0
generated: "2026-01-01T00:00:00Z"
`)
	m := New()
	env := manager.Env{RegistryAliases: map[string]string{"bitnami": "oci://registry-1.docker.io/bitnamicharts"}}
	per := linkedDepsEnv(t, m, files, env)
	if errs := m.(manager.LockReader).ReadLocks(root, per); errs != nil {
		t.Fatal(errs)
	}
	var got []string
	for _, ds := range per {
		for _, d := range ds {
			got = append(got, d.Name+" "+d.Source+" "+d.Skip)
		}
	}
	check(t, got, []string{
		"cert-manager https://charts.jetstack.io ",
		"redis oci://registry-1.docker.io/bitnamicharts ",
		"legacy https://legacy.example.com ",
		"mine https://charts.example.com/mine ",
		"twice @dup repository alias",
		"nowhere @nowhere repository alias",
		"locked-twice @lt repository alias",
		"old https://charts.helm.sh/stable ",
	})
}
