package gitlabci

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const sha1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

const commit = "1111111111111111111111111111111111111111"

type want struct {
	line                   int
	depT, ds, name, source string
	version, digest, skip  string
}

func extract(t *testing.T, src string) []dep.Dep {
	t.Helper()
	return extractEnv(t, src, manager.Env{})
}

func extractEnv(t *testing.T, src string, env manager.Env) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".gitlab-ci.yml", []byte(src), env)
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

func check(t *testing.T, src string, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Manager != Name || d.DepType != w.depT || d.Datasource != w.ds || d.Line != w.line || d.Name != w.name ||
			d.Source != w.source || d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip {
			t.Errorf("%d: got %s/%s line=%d name=%q source=%q version=%q digest=%q skip=%q\nwant %+v",
				i, d.DepType, d.Datasource, d.Line, d.Name, d.Source, d.Version, d.Digest, d.Skip, w)
			continue
		}
		if d.Datasource != "docker" && d.Digest != "" {
			if got := src[d.RefSpan.Start:d.RefSpan.End]; got != w.digest {
				t.Errorf("%d: src[RefSpan] = %q, want %q", i, got, w.digest)
			}
			if got := src[d.VersionSpan.Start:d.VersionSpan.End]; got != w.version {
				t.Errorf("%d: src[VersionSpan] = %q, want %q", i, got, w.version)
			}
			continue
		}
		if d.Version != "" && !docker.Untagged(d) {
			if got := src[d.RefSpan.Start:d.RefSpan.End]; got != w.version {
				t.Errorf("%d: src[RefSpan] = %q, want %q", i, got, w.version)
			}
		}
		if d.Digest != "" {
			if got := src[d.DigestSpan.Start:d.DigestSpan.End]; got != w.digest {
				t.Errorf("%d: src[DigestSpan] = %q, want %q", i, got, w.digest)
			}
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		".gitlab-ci.yml":               true,
		"sub/.gitlab-ci.yaml":          true,
		"build.gitlab-ci.yml":          true,
		".gitlab/ci/build.yml":         true,
		".gitlab/ci/nested/test.yaml":  true,
		"gitlab-ci.txt":                false,
		".gitlab/issue_templates/x.md": false,
		"ci/build.yml":                 false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	const (
		d  = "docker"
		gt = "git-tags"
		gr = "git-refs"
	)
	tests := []struct {
		name  string
		env   manager.Env
		src   string
		wants []want
	}{
		{
			name: "images and services",
			src: `image: golang:1.26
default:
  image:
    name: "alpine:3.20"
    entrypoint: [""]
services:
  - postgres:16
build:
  image: node:22@` + sha1 + `
  services:
    - name: 'redis:7.2'
      alias: cache
    - docker:dind
  script: [make]
.tmpl:
  image: $CI_REGISTRY_IMAGE:latest
variables:
  image: notanimage:1
`,
			wants: []want{
				{line: 1, depT: TypeImage, ds: d, name: "golang", source: "docker.io/library/golang", version: "1.26"},
				{line: 4, depT: TypeImage, ds: d, name: "alpine", source: "docker.io/library/alpine", version: "3.20"},
				{line: 7, depT: TypeService, ds: d, name: "postgres", source: "docker.io/library/postgres", version: "16"},
				{line: 9, depT: TypeImage, ds: d, name: "node", source: "docker.io/library/node", version: "22", digest: sha1},
				{line: 11, depT: TypeService, ds: d, name: "redis", source: "docker.io/library/redis", version: "7.2"},
				{line: 13, depT: TypeService, ds: d, name: "docker", source: "docker.io/library/docker", version: "dind"},
				{line: 16, depT: TypeImage, ds: d, name: "$CI_REGISTRY_IMAGE:latest", skip: "variable reference"},
			},
		},
		{
			name: "single include mapping",
			src: `include:
  project: my-group/my-project
  ref: v1.2.3
  file: /templates/ci.yml
`,
			wants: []want{
				{line: 3, depT: TypeInclude, ds: gt, name: "my-group/my-project", source: "https://gitlab.com/my-group/my-project", version: "v1.2.3"},
			},
		},
		{
			name: "include list",
			src: `include:
  - local: /ci/local.yml
  - '/ci/other.yml'
  - remote: https://example.com/ci.yml
  - template: Auto-DevOps.gitlab-ci.yml
  - project: group/sub/proj
    ref: "2.0.0"
    file: [a.yml, b.yml]
  - project: group/branchy
    ref: main
  - project: group/noref
    file: x.yml
  - project: $CI_PROJECT_PATH
    ref: v1
`,
			wants: []want{
				{line: 7, depT: TypeInclude, ds: gt, name: "group/sub/proj", source: "https://gitlab.com/group/sub/proj", version: "2.0.0"},
				{line: 10, depT: TypeInclude, ds: gr, name: "group/branchy", source: "https://gitlab.com/group/branchy", version: "main"},
				{line: 11, depT: TypeInclude, ds: gt, name: "group/noref", skip: "no ref"},
				{line: 13, depT: TypeInclude, ds: gt, name: "$CI_PROJECT_PATH", skip: "variable reference"},
			},
		},
		{
			name: "string include ignored",
			src:  "include: /ci/local.yml\n",
		},
		{
			name: "remote includes",
			src: `include:
  - remote: https://gitlab.com/gitlab-org/gitlab-foss/-/raw/v16.0.0/lib/gitlab/ci/templates/Jobs/Build.gitlab-ci.yml
  - 'https://raw.githubusercontent.com/actions/checkout/v4.2.2/action.yml'
  - remote: https://gitlab.com/group/proj/-/raw/main/ci.yml
  - https://gitlab.com/group/proj/-/raw/` + commit + `/ci.yml # v1.2.3
  - remote: https://example.com/ci.yml
  - remote: https://gitlab.com/group/proj/-/raw/v1/x.yml?inline=false
  - remote: https://$HOST/group/proj/-/raw/v1/x.yml
`,
			wants: []want{
				{line: 2, depT: TypeRemote, ds: gt, name: "gitlab.com/gitlab-org/gitlab-foss", source: "https://gitlab.com/gitlab-org/gitlab-foss", version: "v16.0.0"},
				{line: 3, depT: TypeRemote, ds: gt, name: "github.com/actions/checkout", source: "https://github.com/actions/checkout", version: "v4.2.2"},
				{line: 4, depT: TypeRemote, ds: gr, name: "gitlab.com/group/proj", source: "https://gitlab.com/group/proj", version: "main"},
				{line: 5, depT: TypeRemote, ds: gt, name: "gitlab.com/group/proj", source: "https://gitlab.com/group/proj", version: "v1.2.3", digest: commit},
				{line: 8, depT: TypeRemote, ds: gt, name: "$HOST/group/proj", skip: "variable reference"},
			},
		},
		{
			name: "commit pins",
			src: `include:
  - project: group/proj
    ref: ` + commit + ` # v1.2.3
  - project: group/branch
    ref: "` + commit + `"  # main
  - project: group/bare
    ref: ` + commit + `
  - component: gitlab.com/group/proj/build@` + commit + ` # 2.0.0
`,
			wants: []want{
				{line: 3, depT: TypeInclude, ds: gt, name: "group/proj", source: "https://gitlab.com/group/proj", version: "v1.2.3", digest: commit},
				{line: 5, depT: TypeInclude, ds: gr, name: "group/branch", source: "https://gitlab.com/group/branch", version: "main", digest: commit},
				{line: 7, depT: TypeInclude, ds: gt, name: "group/bare", source: "https://gitlab.com/group/bare", skip: "pinned without version comment"},
				{line: 8, depT: TypeComponent, ds: gt, name: "gitlab.com/group/proj/build", source: "https://gitlab.com/group/proj", version: "2.0.0", digest: commit},
			},
		},
		{
			name: "components",
			src: `spec:
  inputs:
    stage:
      default: test
---
include:
  - component: gitlab.com/components/sast/sast@2.1.0
    inputs:
      stage: test
  - component: "gitlab.example.com/group/sub/proj/build@v1.0"
  - component: $CI_SERVER_FQDN/group/proj/lint@1.0.0
  - component: gitlab.com/components/opentofu/full-pipeline@~latest
  - component: gitlab.com/components/opentofu/full-pipeline@main
  - component: gitlab.com/components/sast/sast
  - component: gitlab.com/x@1.0.0
`,
			wants: []want{
				{line: 7, depT: TypeComponent, ds: gt, name: "gitlab.com/components/sast/sast", source: "https://gitlab.com/components/sast", version: "2.1.0"},
				{line: 10, depT: TypeComponent, ds: gt, name: "gitlab.example.com/group/sub/proj/build", source: "https://gitlab.example.com/group/sub/proj", version: "v1.0"},
				{line: 11, depT: TypeComponent, ds: gt, name: "$CI_SERVER_FQDN/group/proj/lint", source: "https://gitlab.com/group/proj", version: "1.0.0"},
				{line: 12, depT: TypeComponent, ds: gt, name: "gitlab.com/components/opentofu/full-pipeline", source: "https://gitlab.com/components/opentofu", skip: "latest version"},
				{line: 13, depT: TypeComponent, ds: gr, name: "gitlab.com/components/opentofu/full-pipeline", source: "https://gitlab.com/components/opentofu", version: "main"},
				{line: 14, depT: TypeComponent, ds: gt, name: "gitlab.com/components/sast/sast", skip: "no version"},
				{line: 15, depT: TypeComponent, ds: gt, name: "gitlab.com/x", skip: "invalid component"},
			},
		},
		{
			name: "self-managed instance with path prefix and port",
			env:  manager.Env{GitLabURL: "http://git.example:8080/gitlab"},
			src: `include:
  - project: group/proj
    ref: v1.0.0
  - component: $CI_SERVER_FQDN/group/proj/lint@1.0.0
  - component: ${CI_SERVER_FQDN}/group/sub/proj/build@v2
  - component: $CI_SERVER_HOST/group/proj/test@1.1.0
  - component: git.example:8080/group/proj/fmt@1.2.0
  - component: gitlab.com/components/sast/sast@2.1.0
  - component: $CI_SERVER_FQDN/$GROUP/proj/x@1.0.0
  - component: $CI_SERVER_URL/group/proj/x@1.0.0
  - component: $CI_SERVER_FQDN@1.0.0
`,
			wants: []want{
				{line: 3, depT: TypeInclude, ds: gt, name: "group/proj", source: "http://git.example:8080/gitlab/group/proj", version: "v1.0.0"},
				{line: 4, depT: TypeComponent, ds: gt, name: "$CI_SERVER_FQDN/group/proj/lint", source: "http://git.example:8080/gitlab/group/proj", version: "1.0.0"},
				{line: 5, depT: TypeComponent, ds: gt, name: "${CI_SERVER_FQDN}/group/sub/proj/build", source: "http://git.example:8080/gitlab/group/sub/proj", version: "v2"},
				{line: 6, depT: TypeComponent, ds: gt, name: "$CI_SERVER_HOST/group/proj/test", source: "http://git.example:8080/gitlab/group/proj", version: "1.1.0"},
				{line: 7, depT: TypeComponent, ds: gt, name: "git.example:8080/group/proj/fmt", source: "http://git.example:8080/gitlab/group/proj", version: "1.2.0"},
				{line: 8, depT: TypeComponent, ds: gt, name: "gitlab.com/components/sast/sast", source: "https://gitlab.com/components/sast", version: "2.1.0"},
				{line: 9, depT: TypeComponent, ds: gt, name: "$CI_SERVER_FQDN/$GROUP/proj/x", skip: "variable reference"},
				{line: 10, depT: TypeComponent, ds: gt, name: "$CI_SERVER_URL/group/proj/x", skip: "variable reference"},
				{line: 11, depT: TypeComponent, ds: gt, name: "$CI_SERVER_FQDN", skip: "variable reference"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, tt.src, extractEnv(t, tt.src, tt.env), tt.wants)
		})
	}
}

func TestEdits(t *testing.T) {
	const src = `include:
  - project: group/proj
    ref: 'v1.2.3' # pinned
  - component: gitlab.com/components/sast/sast@2.1.0
image: "golang:1.26" # build
services:
  - name: redis:7.2@` + sha1 + `
`
	sha2 := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	deps := extract(t, src)
	updates := []dep.Update{
		{Dep: deps[0], NewVersion: "v1.3.0"},
		{Dep: deps[1], NewVersion: "2.2.0"},
		{Dep: deps[2], NewVersion: "1.27"},
		{Dep: deps[3], NewVersion: "7.4", NewDigest: sha2},
	}
	var edits []patch.Edit
	for _, u := range updates {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	wantSrc := `include:
  - project: group/proj
    ref: 'v1.3.0' # pinned
  - component: gitlab.com/components/sast/sast@2.2.0
image: "golang:1.27" # build
services:
  - name: redis:7.4@` + sha2 + `
`
	if string(got) != wantSrc {
		t.Errorf("got\n%s\nwant\n%s", got, wantSrc)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "v2"}); err != nil {
		t.Fatal(err)
	}
}

func TestEditsDigest(t *testing.T) {
	const sha2 = "2222222222222222222222222222222222222222"
	src := `include:
  - project: group/proj
    ref: 'v1.2.3' # pinned
  - project: group/branch
    ref: main
  - component: gitlab.com/group/proj/build@2.0.0
  - remote: https://gitlab.com/group/proj/-/raw/v1.0.0/ci.yml
  - project: group/old
    ref: ` + commit + ` # v1.0.0
  - project: group/flow
    ref: ` + commit + ` # main
  - {project: group/flowtag, ref: v1.0.0}
`
	deps := extract(t, src)
	updates := []dep.Update{
		{Dep: deps[0], NewVersion: "v1.2.3", NewDigest: sha2},
		{Dep: deps[1], NewVersion: "main", NewDigest: sha2},
		{Dep: deps[2], NewVersion: "2.1.0", NewDigest: sha2},
		{Dep: deps[3], NewVersion: "v1.0.0", NewDigest: sha2},
		{Dep: deps[4], NewVersion: "v1.1.0", NewDigest: sha2},
		{Dep: deps[5], NewVersion: "main", NewDigest: sha2},
	}
	var edits []patch.Edit
	for _, u := range updates {
		e, err := New().Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	got, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	wantSrc := `include:
  - project: group/proj
    ref: '` + sha2 + `' # v1.2.3 # pinned
  - project: group/branch
    ref: ` + sha2 + ` # main
  - component: gitlab.com/group/proj/build@` + sha2 + ` # 2.1.0
  - remote: https://gitlab.com/group/proj/-/raw/` + sha2 + `/ci.yml # v1.0.0
  - project: group/old
    ref: ` + sha2 + ` # v1.1.0
  - project: group/flow
    ref: ` + sha2 + ` # main
  - {project: group/flowtag, ref: v1.0.0}
`
	if string(got) != wantSrc {
		t.Errorf("got\n%s\nwant\n%s", got, wantSrc)
	}
	if deps[6].InsertAt != 0 {
		t.Errorf("flow mapping InsertAt = %d, want 0", deps[6].InsertAt)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[6], NewVersion: "v1.0.0", NewDigest: sha2}); err == nil {
		t.Error("flow mapping pin: want error")
	}
	if _, err := New().Edits(dep.Update{Dep: deps[4], NewVersion: "v1.1.0"}); err == nil {
		t.Error("unpin: want error")
	}
}

// The Dependency Proxy prefixes are Docker Hub, registryAliases resolve the others, and an
// image without a tag is its implicit latest.
func TestExtractImageAliases(t *testing.T) {
	src := `a:
  image: ${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/node:22
  services:
    - $CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX/postgres:17
    - name: $CI_REGISTRY/group/db:1.2
    - $CI_REGISTRY_IMAGE/x:1
    - redis
`
	d := "docker"
	check(t, src, extractEnv(t, src, manager.Env{RegistryAliases: map[string]string{"$CI_REGISTRY": "registry.gitlab.com"}}), []want{
		{line: 2, depT: TypeImage, ds: d, name: "node", source: "docker.io/library/node", version: "22"},
		{line: 4, depT: TypeService, ds: d, name: "postgres", source: "docker.io/library/postgres", version: "17"},
		{line: 5, depT: TypeService, ds: d, name: "registry.gitlab.com/group/db", source: "registry.gitlab.com/group/db", version: "1.2"},
		{line: 6, depT: TypeService, ds: d, name: "$CI_REGISTRY_IMAGE/x:1", skip: "variable reference"},
		{line: 7, depT: TypeService, ds: d, name: "redis", source: "docker.io/library/redis", version: "latest"},
	})
}
