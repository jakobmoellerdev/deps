package helm

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// testdataFiles reads the testdata files under dir into a linked() file map keyed by their
// slash path below testdata.
func testdataFiles(t *testing.T, names ...string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(n)))
		if err != nil {
			t.Fatal(err)
		}
		files[n] = string(b)
	}
	return files
}

func TestFleet(t *testing.T) {
	files := map[string]string{
		"apps/podinfo/fleet.yaml": `defaultNamespace: podinfo
helm:
  releaseName: podinfo
  repo: https://stefanprodan.github.io/podinfo/
  chart: podinfo
  version: ~6.5.0
targetCustomizations:
- name: values-only
  helm:
    values:
      replicaCount: 2
- name: pinned
  helm:
    version: 6.7.1
- name: other-chart
  helm:
    chart: podinfo-ng
- name: oci
  helm:
    chart: oci://ghcr.io/stefanprodan/charts/podinfo
    version: 6.7.0
`,
		"apps/local/fleet.yml": `helm:
  chart: ./chart
  version: 1.0.0
---
helm:
  chart: "{{ .Values.chart }}"
  repo: https://charts.example.com
  version: 1.0.0
---
helm:
  chart: oci://ghcr.io
  version: 1.0.0
---
helm:
  chart: a/b
  repo: https://charts.example.com
  version: 1.0.0
---
helm:
  chart: x
  repo: s3://bucket/charts
  version: 1.0.0
---
helm:
  chart: cached
  repo: https://cache.example.com/jetstack
  version: v1.14.0
`,
		"clusters/gitrepos.yaml": `kind: GitRepo
apiVersion: fleet.cattle.io/v1alpha1
metadata:
  name: tagged
spec:
  repo: https://github.com/rancher/fleet-examples
  revision: v0.10.0
  paths: [simple]
---
kind: GitRepo
apiVersion: fleet.cattle.io/v1alpha1
metadata:
  name: branch
spec:
  repo: https://github.com/rancher/fleet-examples
  branch: master
---
kind: GitRepo
apiVersion: fleet.cattle.io/v1alpha1
metadata:
  name: commit
spec:
  repo: git@github.com:rancher/fleet-examples.git
  revision: 0123456789abcdef0123456789abcdef01234567
---
kind: GitRepo
apiVersion: fleet.cattle.io/v1alpha1
metadata:
  name: other
spec:
  repo: https://github.com/rancher/fleet-examples
  revision: release-1.0
---
kind: GitRepo
apiVersion: fleet.cattle.io/v1alpha1
spec:
  repo: nohost
  revision: v1.0.0
---
kind: GitRepo
apiVersion: fleet.cattle.io/v1alpha1
spec:
  revision: v1.0.0
`,
	}
	env := manager.Env{RegistryAliases: map[string]string{"https://cache.example.com/jetstack": "https://charts.jetstack.io"}}
	check(t, linkedEnv(t, New(), files, env), []string{
		"apps/local/fleet.yml:3 fleet ./chart  1.0.0 local chart",
		"apps/local/fleet.yml:8 fleet {{ .Values.chart }} https://charts.example.com 1.0.0 template value",
		"apps/local/fleet.yml:12 fleet oci://ghcr.io  1.0.0 invalid chart reference",
		"apps/local/fleet.yml:17 fleet a/b https://charts.example.com 1.0.0 invalid chart reference",
		"apps/local/fleet.yml:22 fleet x  1.0.0 unknown chart repository s3://bucket/charts",
		"apps/local/fleet.yml:27 fleet cached https://charts.jetstack.io v1.14.0 ",
		"apps/podinfo/fleet.yaml:6 fleet podinfo https://stefanprodan.github.io/podinfo 6.5.0  range=~6.5.0",
		"apps/podinfo/fleet.yaml:14 fleet podinfo https://stefanprodan.github.io/podinfo 6.7.1 ",
		"apps/podinfo/fleet.yaml:17 fleet podinfo-ng https://stefanprodan.github.io/podinfo  no version",
		"apps/podinfo/fleet.yaml:21 fleet podinfo oci://ghcr.io/stefanprodan/charts 6.7.0 ",
		"clusters/gitrepos.yaml:7 fleet rancher/fleet-examples https://github.com/rancher/fleet-examples v0.10.0 ",
		"clusters/gitrepos.yaml:15 fleet rancher/fleet-examples https://github.com/rancher/fleet-examples  non-version ref",
		"clusters/gitrepos.yaml:24 fleet rancher/fleet-examples https://github.com/rancher/fleet-examples 0123456789abcdef0123456789abcdef01234567 commit ref",
		"clusters/gitrepos.yaml:32 fleet rancher/fleet-examples https://github.com/rancher/fleet-examples release-1.0 non-version ref",
		"clusters/gitrepos.yaml:37 fleet nohost   unrecognized git URL",
	})
	deps := linkedDepsEnv(t, New(), files, env)
	// Only scalars written in a block are renamed: the inherited repo is not a NamePart, the
	// aliased repo neither.
	for _, ds := range deps {
		for _, d := range ds {
			parts := []string{}
			for _, p := range d.NameParts {
				parts = append(parts, p.Text)
			}
			got := strings.Join(parts, ",")
			want := map[string]string{
				"apps/podinfo/fleet.yaml:6":  "podinfo,https://stefanprodan.github.io/podinfo/",
				"apps/podinfo/fleet.yaml:14": "",
				"apps/podinfo/fleet.yaml:17": "",
				"apps/podinfo/fleet.yaml:21": "podinfo,oci://ghcr.io/stefanprodan/charts",
				"apps/local/fleet.yml:27":    "cached",
			}[d.File+":"+strconv.Itoa(d.Line)]
			if strings.HasPrefix(d.File, "apps/") && d.Skip == "" && got != want {
				t.Errorf("%s:%d: name parts %q, want %q", d.File, d.Line, got, want)
			}
		}
	}
	if _, err := New().Extract("fleet.yaml", []byte("helm: [\n"), manager.Env{}); err == nil {
		t.Error("want a parse error")
	}
}

// The fixtures are rancher/fleet-examples bundles: a chart repository chart, an OCI chart
// without version, and an archive URL whose customizations only set values.
func TestFleetFixtures(t *testing.T) {
	files := testdataFiles(t, "fleet/rancher-monitoring/fleet.yaml", "fleet/appco/fleet.yaml", "fleet/helm-external/fleet.yaml")
	check(t, linked(t, New(), files), []string{
		"fleet/appco/fleet.yaml:4 fleet istio oci://dp.apps.rancher.io/charts  no version",
		"fleet/helm-external/fleet.yaml:3 fleet https://github.com/rancher/fleet-examples/releases/download/example/guestbook-0.0.1.tgz   chart archive URL",
		"fleet/rancher-monitoring/fleet.yaml:6 fleet rancher-monitoring https://charts.rancher.io 104.1.4+up57.0.3 ",
	})
}

func TestHelmsman(t *testing.T) {
	files := map[string]string{
		"dsf/apps.yaml": `namespaces:
  staging:
helmRepos:
  jetstack: https://charts.jetstack.io
  s3repo: s3://my-bucket/charts
  private: https://$USER:$PASS@charts.example.com
apps:
  cert-manager:
    namespace: staging
    chart: jetstack/cert-manager
    version: v1.14.0
  ingress:
    namespace: staging
    chart: ingress/ingress-nginx
    version: ">=4.9.0 <5"
  oci:
    chart: oci://registry-1.docker.io/bitnamicharts/redis
    version: 18.0.0
  local:
    chart: ./charts/app
    version: 1.0.0
  s3:
    chart: s3repo/x
    version: 1.0.0
  private:
    chart: private/x
    version: 1.0.0
  env:
    chart: jetstack/cert-manager
    version: $CERT_MANAGER_VERSION
  invalid:
    chart: nochart
    version: 1.0.0
  nochart:
    version: 1.0.0
  unknown:
    chart: nope/x
    version: 1.0.0
  ambiguous:
    chart: amb/x
    version: 1.0.0
`,
		"dsf/repos.yaml": `helmRepos:
  ingress: https://kubernetes.github.io/ingress-nginx
  amb: https://a.example.com
`,
		"dsf/repos2.yaml": `helmRepos:
  ingress: https://kubernetes.github.io/ingress-nginx/
  amb: https://b.example.com
`,
		"other/config.yaml": "apps:\n  x: {chart: a/b, version: 1.0.0}\n",
	}
	check(t, linked(t, New(), files), []string{
		"dsf/apps.yaml:11 helmsman cert-manager https://charts.jetstack.io v1.14.0 ",
		"dsf/apps.yaml:15 helmsman ingress-nginx https://kubernetes.github.io/ingress-nginx 4.9.0  range=>=4.9.0 <5",
		"dsf/apps.yaml:18 helmsman redis oci://registry-1.docker.io/bitnamicharts 18.0.0 ",
		"dsf/apps.yaml:21 helmsman ./charts/app  1.0.0 local chart",
		"dsf/apps.yaml:24 helmsman x  1.0.0 unknown chart repository s3repo",
		"dsf/apps.yaml:27 helmsman x  1.0.0 variable reference",
		"dsf/apps.yaml:30 helmsman jetstack/cert-manager  $CERT_MANAGER_VERSION variable reference",
		"dsf/apps.yaml:33 helmsman nochart  1.0.0 invalid chart reference",
		"dsf/apps.yaml:38 helmsman x  1.0.0 unknown chart repository nope",
		"dsf/apps.yaml:41 helmsman x  1.0.0 ambiguous chart repository amb",
	})
	if deps, ok := New().(*helmManager).helmsman("x.yaml", []byte("helmRepos: [\n")); ok || deps != nil {
		t.Errorf("unparsable = %v, %v", deps, ok)
	}
}

// The fixtures are Helmsman's examples: a full desired state file, composed files whose apps
// use another file's helmRepos, and apps built from merge-key templates (appsTemplates).
func TestHelmsmanFixtures(t *testing.T) {
	check(t, linked(t, New(), testdataFiles(t, "helmsman/example.yaml")), []string{
		"helmsman/example.yaml:88 helmsman argo https://argoproj.github.io/argo-helm 0.8.5 ",
		"helmsman/example.yaml:114 helmsman artifactory https://charts.jfrog.io 8.3.2 ",
	})
	files := testdataFiles(t, "helmsman/composition/main.yaml", "helmsman/composition/argo.yaml", "helmsman/composition/artifactory.yaml", "helmsman/composition/kyverno.yaml")
	check(t, linked(t, New(), files), []string{
		"helmsman/composition/argo.yaml:24 helmsman argo https://argoproj.github.io/argo-helm 0.8.5 ",
		"helmsman/composition/artifactory.yaml:21 helmsman artifactory https://charts.jfrog.io 8.3.2 ",
		"helmsman/composition/kyverno.yaml:13 helmsman kyverno https://kyverno.github.io/kyverno 2.4.1 ",
		"helmsman/composition/kyverno.yaml:18 helmsman kyverno-policies https://kyverno.github.io/kyverno 2.4.0 ",
		"helmsman/composition/kyverno.yaml:23 helmsman kyverno-reporter https://kyverno.github.io/kyverno 2.9.0 ",
	})
	check(t, linked(t, New(), testdataFiles(t, "helmsman/templates/helmsman.yaml")), []string{
		"helmsman/templates/helmsman.yaml:63 helmsman puppetserver-helm-chart https://puppetlabs.github.io/puppetserver-helm-chart 3.0.2 ",
		"helmsman/templates/helmsman.yaml:70 helmsman tomcat https://charts.bitnami.com/bitnami 6.5.3 ",
	})
}

func TestVendir(t *testing.T) {
	files := map[string]string{
		"vendir.yml": `apiVersion: vendir.k14s.io/v1alpha1
kind: Config
directories:
- path: vendor
  contents:
  - path: oci
    helmChart:
      name: podinfo
      version: 6.7.0
      repository:
        url: oci://ghcr.io/stefanprodan/charts
  - path: norepo
    helmChart:
      name: stable/redis
  - path: slash
    helmChart:
      name: a/b
      version: 1.0.0
      repository: {url: https://charts.example.com}
  - path: branch
    git:
      url: https://github.com/cloudfoundry/cf-for-k8s
      ref: origin/main
  - path: noref
    git:
      url: https://github.com/cloudfoundry/cf-for-k8s
  - path: release
    githubRelease:
      slug: carvel-dev/kapp-controller
      tag: latest
  - path: none
    githubRelease:
      slug: carvel-dev/kapp-controller
  - path: http
    http:
      url: https://example.com/x.zip
`,
		"other/vendir.yml":  "apiVersion: v1\nkind: ConfigMap\n",
		"broken/vendir.yml": "directories: [\n",
	}
	check(t, linked(t, New(), files), []string{
		"vendir.yml:9 vendir podinfo oci://ghcr.io/stefanprodan/charts 6.7.0 ",
		"vendir.yml:14 vendir stable/redis   no repository",
		"vendir.yml:18 vendir a/b https://charts.example.com 1.0.0 invalid chart reference",
		"vendir.yml:23 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s main non-version ref",
		"vendir.yml:26 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s  non-version ref",
		"vendir.yml:30 vendir carvel-dev/kapp-controller https://github.com/carvel-dev/kapp-controller latest non-version ref",
		"vendir.yml:33 vendir carvel-dev/kapp-controller https://github.com/carvel-dev/kapp-controller  no version",
	})
}

// The fixtures are vendir's examples: classic repository charts, git refs (a commit, a tag,
// a tag with origin/), GitHub releases, and semver ref/tag selections.
func TestVendirFixtures(t *testing.T) {
	files := testdataFiles(t, "vendir/helm-chart/vendir.yml", "vendir/git/vendir.yml", "vendir/github-release/vendir.yml", "vendir/versionselection/vendir.yml")
	check(t, linked(t, New(), files), []string{
		"vendir/git/vendir.yml:9 vendir cloudfoundry/cf-k8s-networking https://github.com/cloudfoundry/cf-k8s-networking 2b009b61fa8afb330a4302c694ee61b11104c54c commit ref",
		"vendir/git/vendir.yml:17 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s v0.4.0 ",
		"vendir/git/vendir.yml:24 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s v0.4.0 ",
		"vendir/github-release/vendir.yml:11 vendir carvel-dev/kapp-controller https://github.com/carvel-dev/kapp-controller v0.1.0 ",
		"vendir/github-release/vendir.yml:17 vendir cert-manager/cert-manager https://github.com/cert-manager/cert-manager v1.8.0 ",
		"vendir/github-release/vendir.yml:27 vendir carvel-dev/kapp-controller https://github.com/carvel-dev/kapp-controller v0.35.0 ",
		"vendir/github-release/vendir.yml:36 vendir cloudfoundry-incubator/eirini-release https://github.com/cloudfoundry-incubator/eirini-release v1.2.0 ",
		"vendir/github-release/vendir.yml:47 vendir kubernetes/kubernetes https://github.com/kubernetes/kubernetes  unsupported range",
		"vendir/helm-chart/vendir.yml:14 vendir contour https://charts.bitnami.com/bitnami 7.10.1 ",
		"vendir/versionselection/vendir.yml:9 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s  unsupported range",
		"vendir/versionselection/vendir.yml:18 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s  unsupported range",
		"vendir/versionselection/vendir.yml:27 vendir cloudfoundry/cf-for-k8s https://github.com/cloudfoundry/cf-for-k8s  unsupported range",
		"vendir/versionselection/vendir.yml:38 vendir carvel-dev/kapp-controller https://github.com/carvel-dev/kapp-controller  unsupported range",
	})
}

// A tag after origin/ is edited without the prefix, a chart version and a release tag in place.
func TestVendirEdits(t *testing.T) {
	src := "apiVersion: vendir.k14s.io/v1alpha1\nkind: Config\ndirectories:\n- path: v\n  contents:\n  - path: a\n    git: {url: https://github.com/o/r, ref: origin/v1.0.0}\n  - path: b\n    helmChart: {name: c, version: 1.0.0, repository: {url: https://charts.example.com}}\n  - path: c\n    githubRelease: {slug: o/r, tag: v2.0.0}\n"
	deps, err := New().Extract("vendir.yml", []byte(src), manager.Env{})
	if err != nil || len(deps) != 3 {
		t.Fatalf("deps = %+v, %v", deps, err)
	}
	var edits []patch.Edit
	for i, v := range []string{"v1.1.0", "1.2.0", "v2.1.0"} {
		e, err := New().Edits(dep.Update{Dep: deps[i], NewVersion: v})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply([]byte(src), edits)
	want := strings.NewReplacer("origin/v1.0.0", "origin/v1.1.0", "version: 1.0.0", "version: 1.2.0", "tag: v2.0.0", "tag: v2.1.0").Replace(src)
	if err != nil || string(got) != want {
		t.Errorf("got %q, %v\nwant %q", got, err, want)
	}
}
