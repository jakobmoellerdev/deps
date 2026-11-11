package bitbucketpipelines

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const sha1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

type want struct {
	line                   int
	depT, ds, name, source string
	version, skip          string
}

func extract(t *testing.T, src string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("bitbucket-pipelines.yml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

// checkSpans asserts that every dep of the manager locates its version and digest in src.
func checkSpans(t *testing.T, src string, deps []dep.Dep) {
	t.Helper()
	for i, d := range deps {
		if d.Manager != Name {
			t.Errorf("%d: manager %q", i, d.Manager)
		}
		if d.Version != "" && !docker.Untagged(d) && src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
		if d.Digest != "" && src[d.DigestSpan.Start:d.DigestSpan.End] != d.Digest {
			t.Errorf("%d: src[DigestSpan] = %q, want %q", i, src[d.DigestSpan.Start:d.DigestSpan.End], d.Digest)
		}
	}
}

func check(t *testing.T, src string, deps []dep.Dep, wants []want) {
	t.Helper()
	checkSpans(t, src, deps)
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.DepType != w.depT || d.Datasource != w.ds || d.Line != w.line || d.Name != w.name ||
			d.Source != w.source || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got %s/%s line=%d name=%q source=%q version=%q skip=%q\nwant %+v",
				i, d.DepType, d.Datasource, d.Line, d.Name, d.Source, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		"bitbucket-pipelines.yml":       true,
		"sub/bitbucket-pipelines.yml":   true,
		"shared-pipelines.yml":          true,
		"bitbucket-pipelines.yaml":      true,
		"azure-pipelines.yml":           false, // azure-pipelines
		"ci/azure/deploy-pipelines.yml": false,
		"my-app-ci-pipelines.yml":       false, // the prefix is one word
		"pipelines.yml":                 false,
		"bitbucket-pipelines.json":      false,
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
	)
	src := `image: golang:1.26
definitions:
  services:
    postgres:
      image:
        name: postgres:17@` + sha1 + `
        username: u
    docker:
      memory: 2048
  steps:
    - step: &build
        image: &img node:24
        script:
          - pipe: atlassian/aws-s3-deploy:1.1.0
          - pipe: 'docker://acme/notify:2.0'
          - pipe: atlassian/slack-notify
          - pipe: $PIPE
          - pipe: not a pipe
pipelines:
  default:
    - step: *build
    - step:
        <<: *build
        image: *img
    - parallel:
        steps:
          - step:
              image: ${IMAGE}
          - step:
              image: python
    - stage:
        steps:
          - step:
              script:
                - pipe: atlassian/aws-ecr-push-image:2
`
	check(t, src, extract(t, src), []want{
		{line: 1, depT: TypeImage, ds: d, name: "golang", source: "docker.io/library/golang", version: "1.26"},
		{line: 6, depT: TypeService, ds: d, name: "postgres", source: "docker.io/library/postgres", version: "17"},
		{line: 12, depT: TypeImage, ds: d, name: "node", source: "docker.io/library/node", version: "24"}, // once, at the anchor
		{line: 14, depT: TypePipe, ds: gt, name: "atlassian/aws-s3-deploy", source: "https://bitbucket.org/atlassian/aws-s3-deploy", version: "1.1.0"},
		{line: 15, depT: TypePipe, ds: d, name: "acme/notify", source: "docker.io/acme/notify", version: "2.0"},
		{line: 16, depT: TypePipe, ds: gt, name: "atlassian/slack-notify", skip: "no version"},
		{line: 17, depT: TypePipe, ds: gt, name: "$PIPE", skip: "variable reference"},
		{line: 18, depT: TypePipe, ds: gt, name: "not a pipe", skip: "invalid pipe"},
		{line: 28, depT: TypeImage, ds: d, name: "${IMAGE}", skip: "variable reference"},
		{line: 30, depT: TypeImage, ds: d, name: "python", source: "docker.io/library/python", version: "latest"},
		{line: 35, depT: TypePipe, ds: gt, name: "atlassian/aws-ecr-push-image", source: "https://bitbucket.org/atlassian/aws-ecr-push-image", version: "2"},
	})
}

func TestExtractInvalidYAML(t *testing.T) {
	if _, err := New().Extract("bitbucket-pipelines.yml", []byte("image: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixtures are real-world files: near-daos/astro-ui (global image, step image, pipes),
// enroute-mobi/ara (image mappings with credentials, service definitions, anchored steps) and
// integer-net/magento2-sansec-watch (merge keys, pipes in after-script).
func TestExtractFixtures(t *testing.T) {
	const (
		d  = "docker"
		gt = "git-tags"
	)
	tests := []struct {
		file  string
		n     int
		wants map[int]want // by dep index
	}{
		{"testdata/astro-ui.yml", 5, map[int]want{
			0: {line: 1, depT: TypeImage, ds: d, name: "node", source: "docker.io/library/node", version: "14.16.0"},
			1: {line: 57, depT: TypePipe, ds: gt, name: "atlassian/aws-ecr-push-image", source: "https://bitbucket.org/atlassian/aws-ecr-push-image", version: "1.4.2"},
			2: {line: 70, depT: TypeImage, ds: d, name: "alpine/helm", source: "docker.io/alpine/helm", version: "3.6.3"},
			4: {line: 120, depT: TypePipe, ds: gt, name: "atlassian/aws-s3-deploy", source: "https://bitbucket.org/atlassian/aws-s3-deploy", version: "0.3.8"},
		}},
		{"testdata/ara.yml", 17, map[int]want{
			0: {line: 8, depT: TypeService, ds: d, name: "us-east1-docker.pkg.dev/enroute-interne/enroute-interne/enroute-postgresql",
				source: "us-east1-docker.pkg.dev/enroute-interne/enroute-interne/enroute-postgresql", version: "dev"},
			1: {line: 16, depT: TypeService, ds: d, name: "redis", source: "docker.io/library/redis", version: "latest"},
			2: {line: 21, depT: TypeImage, ds: d, name: "us-east1-docker.pkg.dev/enroute-interne/enroute-interne/enroute-golang",
				source: "us-east1-docker.pkg.dev/enroute-interne/enroute-interne/enroute-golang", version: "dev"},
			12: {line: 190, depT: TypePipe, ds: gt, name: "atlassian/datadog-send-event", source: "https://bitbucket.org/atlassian/datadog-send-event", version: "2.3.1"},
		}},
		{"testdata/sansec-watch.yml", 10, map[int]want{
			0: {line: 14, depT: TypePipe, ds: gt, name: "atlassian/checkstyle-report", source: "https://bitbucket.org/atlassian/checkstyle-report", version: "0.5.1"},
			4: {line: 68, depT: TypeImage, ds: d, name: "integerhub/php", source: "docker.io/integerhub/php", version: "8.3-fpm-local"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			deps, err := New().Extract("bitbucket-pipelines.yml", src, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			checkSpans(t, string(src), deps)
			if len(deps) != tt.n {
				t.Fatalf("got %d deps, want %d", len(deps), tt.n)
			}
			for i, w := range tt.wants {
				d := deps[i]
				if d.DepType != w.depT || d.Datasource != w.ds || d.Line != w.line || d.Name != w.name ||
					d.Source != w.source || d.Version != w.version || d.Skip != w.skip {
					t.Errorf("%d: got %s/%s line=%d name=%q source=%q version=%q skip=%q\nwant %+v",
						i, d.DepType, d.Datasource, d.Line, d.Name, d.Source, d.Version, d.Skip, w)
				}
			}
		})
	}
}

func TestEdits(t *testing.T) {
	src := `image: golang:1.26
pipelines:
  default:
    - step:
        script:
          - pipe: atlassian/aws-s3-deploy:1.1.0
          - pipe: docker://acme/notify:2.0
`
	sha2 := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	deps := extract(t, src)
	var edits []patch.Edit
	for _, u := range []dep.Update{
		{Dep: deps[0], NewVersion: "1.27", NewDigest: sha2},
		{Dep: deps[1], NewVersion: "1.2.0"},
		{Dep: deps[2], NewVersion: "2.1"},
	} {
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
	wantSrc := `image: golang:1.27@` + sha2 + `
pipelines:
  default:
    - step:
        script:
          - pipe: atlassian/aws-s3-deploy:1.2.0
          - pipe: docker://acme/notify:2.1
`
	if string(got) != wantSrc {
		t.Errorf("got\n%s\nwant\n%s", got, wantSrc)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "1.2.0", NewDigest: sha2}); err == nil {
		t.Error("pipe digest edit: want error")
	}
}

func TestRename(t *testing.T) {
	src := "image: golang:1.26\npipelines:\n  default:\n    - step:\n        script:\n          - pipe: atlassian/x:1.0.0\n"
	deps := extract(t, src)
	r := New().(manager.Renamer)
	d, err := r.Rename(deps[0], "ghcr.io/acme/golang")
	if err != nil || d.Name != "ghcr.io/acme/golang" || d.Source != "ghcr.io/acme/golang" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
	if _, err := r.Rename(deps[1], "atlassian/y"); err == nil {
		t.Error("pipe rename: want error")
	}
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "1.26", NewName: "ghcr.io/acme/golang"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := patch.Apply([]byte(src), edits)
	if want := "image: ghcr.io/acme/golang:1.26\n"; string(got[:len(want)]) != want {
		t.Errorf("renamed = %q", got)
	}
}
