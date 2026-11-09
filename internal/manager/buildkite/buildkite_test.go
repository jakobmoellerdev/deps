package buildkite

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                              int
	depT, name, source, version, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".buildkite/pipeline.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name {
			t.Errorf("%d: manager %s", i, d.Manager)
		}
		if d.DepType == TypePlugin && (d.Datasource != "git-tags" || d.Versioning != "tag") {
			t.Errorf("%d: %s/%s", i, d.Datasource, d.Versioning)
		}
		if d.DepType == TypePlugin && d.Skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, n int, wants map[int]want) {
	t.Helper()
	if len(deps) != n {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), n, deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.DepType != w.depT || d.Line != w.line || d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got %s line=%d name=%q source=%q version=%q skip=%q\nwant %+v", i, d.DepType, d.Line, d.Name, d.Source, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		".buildkite/pipeline.yml":          true,
		".buildkite/pipeline.yaml":         true,
		"sub/.buildkite/steps/release.yml": true,
		"buildkite.yml":                    true,
		"ci/pipeline.buildkite.yaml":       true,
		".buildkite/hooks/post-checkout":   false,
		".buildkite/pipeline.yml.tmpl":     false,
		"buildkite/pipeline.yml":           false,
		".github/workflows/ci.yml":         false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`image: node:24
common:
  - login: &login
      elastic/vault-docker-login#v0.8.0:
        path: x
steps:
  - command: make
    image: golang:1.27
    agents:
      queue: default
      image: family/ubuntu-2404
    plugins:
      - docker#v5.12.0:
          image: alpine:3.22
      - shellcheck#v1.4.0
      - "foo/bar-buildkite-plugin#v2"
      - https://github.com/acme/thing.git#v1.2.3: ~
      - ssh://git@bitbucket.org/acme/other-buildkite-plugin.git#v0.3.0
      - https://gitlab.example.com/group/sub/plugin#2.0.0
      - github.com/acme/thing#v1
      - ${REPO}#${COMMIT}
      - ./local-plugin
      - *login
  - group: nested
    steps:
      - plugins:
          docker-compose#v4.14.0:
            run: app
`)
	check(t, extract(t, src), 13, map[int]want{
		0:  {line: 1, depT: TypeImage, name: "node", source: "docker.io/library/node", version: "24"},
		1:  {line: 4, depT: TypePlugin, name: "elastic/vault-docker-login", source: "https://github.com/elastic/vault-docker-login-buildkite-plugin", version: "v0.8.0"},
		2:  {line: 8, depT: TypeImage, name: "golang", source: "docker.io/library/golang", version: "1.27"},
		3:  {line: 15, depT: TypePlugin, name: "shellcheck", source: "https://github.com/buildkite-plugins/shellcheck-buildkite-plugin", version: "v1.4.0"},
		4:  {line: 16, depT: TypePlugin, name: "foo/bar-buildkite-plugin", source: "https://github.com/foo/bar-buildkite-plugin", version: "v2"},
		5:  {line: 18, depT: TypePlugin, name: "acme/other-buildkite-plugin", source: "https://bitbucket.org/acme/other-buildkite-plugin", version: "v0.3.0"},
		6:  {line: 19, depT: TypePlugin, name: "group/sub/plugin", source: "https://gitlab.example.com/group/sub/plugin", version: "2.0.0"},
		7:  {line: 20, depT: TypePlugin, name: "github.com/acme/thing", skip: "invalid plugin"},
		8:  {line: 21, depT: TypePlugin, name: "${REPO}", skip: "variable reference"},
		9:  {line: 13, depT: TypePlugin, name: "docker", source: "https://github.com/buildkite-plugins/docker-buildkite-plugin", version: "v5.12.0"},
		10: {line: 14, depT: TypeImage, name: "alpine", source: "docker.io/library/alpine", version: "3.22"},
		11: {line: 17, depT: TypePlugin, name: "acme/thing", source: "https://github.com/acme/thing", version: "v1.2.3"},
		12: {line: 27, depT: TypePlugin, name: "docker-compose", source: "https://github.com/buildkite-plugins/docker-compose-buildkite-plugin", version: "v4.14.0"},
	})
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{"", "- a\n", "steps: foo\n", "plugins: x\n", "a#b c: 1\n"} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
	if deps := extract(t, []byte("plugins:\n  - \"docker\\x23v5\"\n")); len(deps) != 1 || deps[0].Skip != "cannot locate value" {
		t.Errorf("escaped plugin: %+v", deps)
	}
	if _, err := New().Extract("buildkite.yml", []byte("steps: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixtures are real pipelines: buildkite/agent (anchored plugins with options),
// buildkite-plugins/docker-buildkite-plugin (scalar plugins, a plugin under test referenced by
// variables, the docker plugin's image), m3db/m3 (agent-stack-k8s sidecar and container images,
// plugins as a mapping) and elastic/beats (plugin configurations anchored outside steps, VM
// images under agents).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/agent.yml", 31, map[int]want{
			0:  {line: 29, depT: TypePlugin, name: "aws-assume-role-with-web-identity", source: "https://github.com/buildkite-plugins/aws-assume-role-with-web-identity-buildkite-plugin", version: "v1.4.0"},
			13: {line: 287, depT: TypePlugin, name: "artifacts", source: "https://github.com/buildkite-plugins/artifacts-buildkite-plugin", version: "v1.9.4"},
		}},
		{"testdata/docker-plugin.yml", 9, map[int]want{
			2: {line: 17, depT: TypePlugin, name: "plugin-tester", source: "https://github.com/buildkite-plugins/plugin-tester-buildkite-plugin", version: "v2.0.0"},
			5: {line: 33, depT: TypePlugin, name: "${BUILDKITE_PULL_REQUEST_REPO:-$BUILDKITE_REPO}", skip: "variable reference"},
			6: {line: 34, depT: TypeImage, name: "ubuntu", source: "docker.io/library/ubuntu", version: "22.04"},
		}},
		{"testdata/m3.yml", 16, map[int]want{
			0: {line: 7, depT: TypeImage, name: "us-west1-docker.pkg.dev/ci-compute/buildkite-images/buildkite-dind", source: "us-west1-docker.pkg.dev/ci-compute/buildkite-images/buildkite-dind", version: "v1"},
			7: {line: 164, depT: TypePlugin, name: "gopath-checkout", source: "https://github.com/buildkite-plugins/gopath-checkout-buildkite-plugin", version: "v1.0.1"},
		}},
		{"testdata/beats-packaging.yml", 3, map[int]want{
			2: {line: 30, depT: TypePlugin, name: "elastic/oblt-google-auth", source: "https://github.com/elastic/oblt-google-auth-buildkite-plugin", version: "v1.3.1"},
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			check(t, extract(t, src), tt.n, tt.wants)
		})
	}
}

func TestEditsAndRename(t *testing.T) {
	src := []byte("steps:\n  - image: golang:1.26\n    plugins:\n      - docker#v5.12.0:\n          image: alpine\n")
	deps := extract(t, src)
	pe, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "v5.13.0"})
	if err != nil {
		t.Fatal(err)
	}
	ie, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "1.27"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, append(pe, ie...))
	if want := "steps:\n  - image: golang:1.27\n    plugins:\n      - docker#v5.13.0:\n          image: alpine\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "v5.13.0", NewDigest: "abc"}); err == nil {
		t.Error("want digest error for plugins")
	}
	r := New().(manager.Renamer)
	if d, err := r.Rename(deps[0], "ghcr.io/acme/go"); err != nil || d.Source != "ghcr.io/acme/go" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
	if _, err := r.Rename(deps[1], "x"); err == nil {
		t.Error("want rename error for plugins")
	}
}
