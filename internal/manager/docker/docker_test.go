package docker

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const (
	sha1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	sha2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// want describes one extracted dep; ref/digest are the expected texts at RefSpan/DigestSpan
// and insert the text starting at InsertAt.
type want struct {
	line                        int
	name, source, version, depT string
	digest, skip, insert        string
}

func check(t *testing.T, src []byte, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Manager != Name || d.Datasource != "docker" || d.Versioning != "docker" || d.DepType != w.depT {
			t.Errorf("%d: kind = %s/%s/%s/%s", i, d.Manager, d.Datasource, d.Versioning, d.DepType)
		}
		if d.Line != w.line || d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Digest != w.digest || d.Skip != w.skip {
			t.Errorf("%d: got line=%d name=%q source=%q version=%q digest=%q skip=%q\nwant %+v", i, d.Line, d.Name, d.Source, d.Version, d.Digest, d.Skip, w)
			continue
		}
		if d.Skip != "" && d.Version == "" {
			continue
		}
		if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != w.version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, got, w.version)
		}
		if got := string(src[d.DigestSpan.Start:d.DigestSpan.End]); got != w.digest {
			t.Errorf("%d: src[DigestSpan] = %q, want %q", i, got, w.digest)
		}
		if !strings.HasPrefix(string(src[d.InsertAt:]), w.insert) {
			t.Errorf("%d: src[InsertAt:] = %q, want prefix %q", i, src[d.InsertAt:min(len(src), d.InsertAt+20)], w.insert)
		}
	}
}

func extract(t *testing.T, file, src string) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

// variants returns src with LF and CRLF line endings.
func variants(src string) map[string]string {
	return map[string]string{"lf": src, "crlf": strings.ReplaceAll(src, "\n", "\r\n")}
}

const dockerfile = `# syntax=docker/dockerfile:1
ARG GO=1.22
FROM golang:1.22.3-alpine AS build
from --platform=$BUILDPLATFORM node:20 as Web
FROM \
    --platform=linux/amd64 \
    # a comment

    python:3.12-slim@` + sha1 + ` AS py
FROM build AS final
FROM web
FROM scratch
FROM golang:${GO}
FROM alpine
FROM ubuntu:latest
FROM ghcr.io/org/img@` + sha1 + `
FROM docker.io/library/redis:7.2 AS cache
FROM --platform=linux/arm64
FROM UPPER:1
COPY . .
RUN echo FROM nope:1.0 \
    && FROM fake:1.0
  FROM indented:1.0
FROM last:2.0`

func TestExtractDockerfile(t *testing.T) {
	for name, src := range variants(dockerfile) {
		t.Run(name, func(t *testing.T) {
			deps := extract(t, "build/Dockerfile", src)
			check(t, []byte(src), deps, []want{
				{line: 3, name: "golang", source: "docker.io/library/golang", version: "1.22.3-alpine", depT: TypeDockerfile, insert: " AS build"},
				{line: 4, name: "node", source: "docker.io/library/node", version: "20", depT: TypeDockerfile, insert: " as Web"},
				{line: 9, name: "python", source: "docker.io/library/python", version: "3.12-slim", digest: sha1, depT: TypeDockerfile, insert: "@sha256:1111"},
				{line: 13, name: "golang:${GO}", depT: TypeDockerfile, skip: "variable reference"},
				{line: 14, name: "alpine", source: "docker.io/library/alpine", depT: TypeDockerfile, skip: "no tag"},
				{line: 15, name: "ubuntu", source: "docker.io/library/ubuntu", version: "latest", depT: TypeDockerfile},
				{line: 16, name: "ghcr.io/org/img", source: "ghcr.io/org/img", digest: sha1, depT: TypeDockerfile, skip: "digest without tag"},
				{line: 17, name: "redis", source: "docker.io/library/redis", version: "7.2", depT: TypeDockerfile, insert: " AS cache"},
				{line: 19, name: "UPPER:1", depT: TypeDockerfile, skip: "invalid reference"},
				{line: 23, name: "indented", source: "docker.io/library/indented", version: "1.0", depT: TypeDockerfile},
				{line: 24, name: "last", source: "docker.io/library/last", version: "2.0", depT: TypeDockerfile},
			})
		})
	}
}

func TestExtractDockerfileEscapeDirective(t *testing.T) {
	src := "# escape=`\n\nFROM `\n  mcr.microsoft.com/dotnet/sdk:8.0 AS b\nFROM alpine:3.20 \\\n"
	deps := extract(t, "Containerfile", src)
	check(t, []byte(src), deps, []want{
		{line: 4, name: "mcr.microsoft.com/dotnet/sdk", source: "mcr.microsoft.com/dotnet/sdk", version: "8.0", depT: TypeDockerfile, insert: " AS b"},
		// the backslash is no escape character here, so it is an extra argument
		{line: 5, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeDockerfile, insert: " \\"},
	})
	// an escape character inside a token is no continuation
	src = "FROM --platform=a\\b alpine:3.20\n"
	check(t, []byte(src), extract(t, "Dockerfile", src), []want{
		{line: 1, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeDockerfile},
	})
	// escape at EOF ends the instruction; directives after the first instruction are comments
	src = "FROM alpine:3.20 \\"
	check(t, []byte(src), extract(t, "Dockerfile", src), []want{
		{line: 1, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeDockerfile, insert: " \\"},
	})
	src = "FROM a:1\n# escape=`\nFROM b:2 `\n"
	if deps := extract(t, "Dockerfile", src); len(deps) != 2 || deps[1].Name != "b" {
		t.Errorf("late escape directive: %+v", deps)
	}
}

const compose = `services:
  db:
    image: postgres:16.2-alpine
  cache:
    image: "redis:7"
  app:
    image: 'ghcr.io/org/app:1.2.3@` + sha1 + `'
  web:
    build: .
  x:
    image: ${REGISTRY}/x:1.0
  y:
    image: nginx
  "ünïcødé": {image: caddy:2.8}
  esc:
    image: "alpine:\x33.19"
  block:
    image: |
      alpine:3.19
  alias:
    image: *img
  empty:
    image: ""
  notmap: [1]
volumes:
  image: not:1.0
`

func TestExtractCompose(t *testing.T) {
	for name, src := range variants(strings.Replace(compose, "*img", "&img busybox:1.36", 1)) {
		t.Run(name, func(t *testing.T) {
			deps := extract(t, "deploy/docker-compose.prod.yml", src)
			check(t, []byte(src), deps, []want{
				{line: 3, name: "postgres", source: "docker.io/library/postgres", version: "16.2-alpine", depT: TypeCompose},
				{line: 5, name: "redis", source: "docker.io/library/redis", version: "7", depT: TypeCompose, insert: `"`},
				{line: 7, name: "ghcr.io/org/app", source: "ghcr.io/org/app", version: "1.2.3", digest: sha1, depT: TypeCompose, insert: "@" + sha1 + "'"},
				{line: 11, name: "${REGISTRY}/x:1.0", depT: TypeCompose, skip: "interpolated image"},
				{line: 13, name: "nginx", source: "docker.io/library/nginx", depT: TypeCompose, skip: "no tag"},
				{line: 14, name: "caddy", source: "docker.io/library/caddy", version: "2.8", depT: TypeCompose, insert: "}"},
				{line: 16, name: "alpine:3.19", depT: TypeCompose, skip: "cannot locate value"},
				{line: 18, name: "alpine:3.19\n", depT: TypeCompose, skip: "cannot locate value"},
				{line: 21, name: "busybox", source: "docker.io/library/busybox", version: "1.36", depT: TypeCompose},
			})
		})
	}
}

const composeAnchors = `x-runner: &runner
  image: gitea/runner:5
  restart: unless-stopped
x-tools:
  nested:
    - image: ghcr.io/o/tool:1.0
      environment:
        image: not:1.0
x-img: &img busybox:1.36
services:
  r1:
    <<: *runner
  r2:
    <<: [*runner]
    container_name: r2
  own:
    <<: *runner
    image: gitea/runner:6
  aliased:
    image: *img
  again:
    image: *img
`

func TestExtractComposeAnchors(t *testing.T) {
	for name, src := range variants(composeAnchors) {
		t.Run(name, func(t *testing.T) {
			check(t, []byte(src), extract(t, "ci/compose.yml", src), []want{
				{line: 2, name: "gitea/runner", source: "docker.io/gitea/runner", version: "5", depT: TypeCompose},
				{line: 6, name: "ghcr.io/o/tool", source: "ghcr.io/o/tool", version: "1.0", depT: TypeCompose},
				{line: 18, name: "gitea/runner", source: "docker.io/gitea/runner", version: "6", depT: TypeCompose},
				{line: 9, name: "busybox", source: "docker.io/library/busybox", version: "1.36", depT: TypeCompose},
			})
		})
	}
}

const composeInterpolation = `services:
  a:
    image: ${REGISTRY:-docker.io}/nginx:${NGINX_TAG:-1.27}
  b:
    image: "ghcr.io/o/app:${TAG-2.0}@` + sha1 + `"
  c:
    image: ${REGISTRY}/x:1.0
  d:
    image: nginx:$TAG
  e:
    image: nginx:${TAG:?required}
  f:
    image: nginx:1.${MINOR:-27}
  g:
    image: $${X}:1
  h:
    image: ${R:-${S}}/x:1
  i:
    image: redis:${T:-7.2}-alpine
  j:
    image: ${R:-ghcr.io}/o/lit:3.1$$
  k:
    image: nginx:${TAG:+x}
  l:
    image: nginx:${TAG:-1.27
  m:
    image: nginx:${1X:-1}
`

func TestExtractComposeInterpolation(t *testing.T) {
	src := composeInterpolation
	deps := extract(t, "compose.yaml", src)
	check(t, []byte(src), deps, []want{
		{line: 3, name: "nginx", source: "docker.io/library/nginx", version: "1.27", depT: TypeCompose, insert: "}"},
		{line: 5, name: "ghcr.io/o/app", source: "ghcr.io/o/app", version: "2.0", digest: sha1, depT: TypeCompose, insert: "}@" + sha1},
		{line: 7, name: "${REGISTRY}/x:1.0", depT: TypeCompose, skip: "interpolated image"},
		{line: 9, name: "nginx:$TAG", depT: TypeCompose, skip: "interpolated image"},
		{line: 11, name: "nginx:${TAG:?required}", depT: TypeCompose, skip: "interpolated image"},
		{line: 13, name: "nginx:1.${MINOR:-27}", depT: TypeCompose, skip: "interpolated image"},
		{line: 15, name: "$${X}:1", depT: TypeCompose, skip: "invalid reference"},
		{line: 17, name: "${R:-${S}}/x:1", depT: TypeCompose, skip: "interpolated image"},
		{line: 19, name: "redis:${T:-7.2}-alpine", depT: TypeCompose, skip: "interpolated image"},
		{line: 21, name: "${R:-ghcr.io}/o/lit:3.1$$", depT: TypeCompose, skip: "invalid reference"},
		{line: 23, name: "nginx:${TAG:+x}", depT: TypeCompose, skip: "interpolated image"},
		{line: 25, name: "nginx:${TAG:-1.27", depT: TypeCompose, skip: "interpolated image"},
		{line: 27, name: "nginx:${1X:-1}", depT: TypeCompose, skip: "interpolated image"},
	})
	for _, c := range []struct {
		u    dep.Update
		want string
	}{
		{dep.Update{Dep: deps[0], Type: dep.Minor, NewVersion: "1.28"}, "image: ${REGISTRY:-docker.io}/nginx:${NGINX_TAG:-1.28}\n"},
		{dep.Update{Dep: deps[0], Type: dep.Pin, NewVersion: "1.27", NewDigest: sha2}, "image: ${REGISTRY:-docker.io}/nginx:${NGINX_TAG:-1.27@" + sha2 + "}\n"},
		{dep.Update{Dep: deps[1], Type: dep.Minor, NewVersion: "2.1", NewDigest: sha2}, `image: "ghcr.io/o/app:${TAG-2.1}@` + sha2 + `"` + "\n"},
	} {
		edits, err := New().Edits(c.u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil || !strings.Contains(string(out), c.want) {
			t.Errorf("got %s, %v; want line %q", out, err, c.want)
		}
	}
}

func TestExtractComposeAdditionalContexts(t *testing.T) {
	src := `services:
  app:
    build:
      context: .
      additional_contexts:
        base: docker-image://alpine:3.20
        src: ../src
  app2:
    build:
      additional_contexts:
        - base=docker-image://golang:1.23@` + sha1 + `
        - other=../x
        - docker-image://ignored:1.0
        - [1]
  app3:
    build:
      additional_contexts: [1]
  app4:
    build: .
`
	check(t, []byte(src), extract(t, "docker-compose.yml", src), []want{
		{line: 6, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeCompose},
		{line: 11, name: "golang", source: "docker.io/library/golang", version: "1.23", digest: sha1, depT: TypeCompose, insert: "@"},
	})
}

func TestExtractComposeFileNames(t *testing.T) {
	// "other" is no Compose top-level key, so only the file name selects compose mode.
	const src = "services:\n  a:\n    image: redis:7.2\nother: 1\n"
	for file, ok := range map[string]bool{
		"compose.yaml":                  true,
		"compose.yml":                   true,
		"docker-compose.yaml":           true,
		"docker-compose.yml":            true,
		"compose.override.yml":          true,
		"docker-compose.override.yaml":  true,
		"deploy/compose.prod.yaml":      true,
		"docker-compose.dev.yml":        true,
		"app.compose.yml":               true,
		"deploy/x.compose.yaml":         true,
		"my-compose.yml":                false,
		"composer.yaml":                 false,
		"decompose.yml":                 false,
		"compose/values.yaml":           false,
		"docker-compose.yml.d/foo.yaml": false,
	} {
		if deps := extract(t, file, src); (len(deps) == 1) != ok {
			t.Errorf("%s: got %+v, want compose %v", file, deps, ok)
		}
	}
}

func TestExtractComposeByContent(t *testing.T) {
	src := "name: stack\nx-common: &c\n  restart: always\nservices:\n  a:\n    <<: *c\n    image: redis:7.2\n  b:\n    build: .\nvolumes:\n  data:\n"
	check(t, []byte(src), extract(t, "deploy/stack.yml", src), []want{
		{line: 7, name: "redis", source: "docker.io/library/redis", version: "7.2", depT: TypeCompose},
	})
	for name, src := range map[string]string{
		"kubernetes":        "apiVersion: v1\nkind: Config\nservices:\n  a:\n    image: redis:7.2\n",
		"helm values":       "replicaCount: 1\nservices:\n  a:\n    image: redis:7.2\n",
		"scalar services":   "services:\n  a: x\n  image: redis:7.2\n",
		"no image or build": "services:\n  a:\n    command: x\nx-y:\n  image: redis:7.2\n",
		"services list":     "services:\n  - image: redis:7.2\n",
	} {
		if deps := extract(t, "deploy/"+strings.ReplaceAll(name, " ", "-")+".yaml", src); len(deps) != 0 {
			t.Errorf("%s: got %+v", name, deps)
		}
	}
}

const workflow = `on: push
jobs:
  a:
    runs-on: x
    container: node:20.11
    services:
      redis:
        image: redis:7.2
      pg:
        image: "postgres:16"
      bad: [1]
    steps:
      - uses: actions/checkout@v4
      - uses: docker://alpine:3.19
      - uses: 'docker://ghcr.io/o/i:1.0'
      - uses: docker://
      - run: echo
  b:
    container:
      image: golang:1.22@` + sha1 + `
      options: --cpus 1
  c:
    container: ${{ matrix.img }}
  d:
    uses: org/wf/.github/workflows/x.yml@v1
  e: [1]
`

func TestExtractWorkflow(t *testing.T) {
	for name, src := range variants(workflow) {
		t.Run(name, func(t *testing.T) {
			deps := extract(t, ".gitea/workflows/ci.yml", src)
			check(t, []byte(src), deps, []want{
				{line: 5, name: "node", source: "docker.io/library/node", version: "20.11", depT: TypeWorkflow},
				{line: 8, name: "redis", source: "docker.io/library/redis", version: "7.2", depT: TypeWorkflow},
				{line: 10, name: "postgres", source: "docker.io/library/postgres", version: "16", depT: TypeWorkflow, insert: `"`},
				{line: 14, name: "alpine", source: "docker.io/library/alpine", version: "3.19", depT: TypeWorkflow},
				{line: 15, name: "ghcr.io/o/i", source: "ghcr.io/o/i", version: "1.0", depT: TypeWorkflow, insert: "'"},
				{line: 20, name: "golang", source: "docker.io/library/golang", version: "1.22", digest: sha1, depT: TypeWorkflow, insert: "@"},
				{line: 23, name: "${{ matrix.img }}", depT: TypeWorkflow, skip: "variable reference"},
			})
		})
	}
}

func TestExtractAction(t *testing.T) {
	src := "name: x\nruns:\n  using: docker\n  image: docker://alpine:3.20\n"
	check(t, []byte(src), extract(t, "action.yml", src), []want{
		{line: 4, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeAction},
	})
	for _, src := range []string{
		"runs:\n  using: docker\n  image: Dockerfile\n",
		"runs:\n  using: node20\n  image: docker://alpine:3.20\n",
		"[1, 2]\n",
		"",
	} {
		if deps := extract(t, "sub/action.yaml", src); len(deps) != 0 {
			t.Errorf("%q: got %+v", src, deps)
		}
	}
	src = "runs:\n  using: composite\n  steps:\n    - uses: docker://ghcr.io/o/tool:2.1.0\n    - uses: ./x\n"
	check(t, []byte(src), extract(t, ".github/actions/a/action.yml", src), []want{
		{line: 4, name: "ghcr.io/o/tool", source: "ghcr.io/o/tool", version: "2.1.0", depT: TypeAction},
	})
}

func TestExtractParseError(t *testing.T) {
	for _, file := range []string{"compose.yaml", ".github/workflows/x.yml"} {
		if _, err := New().Extract(file, []byte("a: [\n"), manager.Env{}); err == nil {
			t.Errorf("%s: want parse error", file)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if m.Name() != "docker" {
		t.Errorf("Name = %q", m.Name())
	}
	for path, ok := range map[string]bool{
		"Dockerfile":                   true,
		"a/b/Dockerfile":               true,
		"Dockerfile.dev":               true,
		"Containerfile":                true,
		"build/app.Dockerfile":         true,
		"x.Containerfile":              true,
		"Dockerfile.dockerignore":      false,
		"dockerfile":                   false,
		"Dockerfile/x":                 false,
		"compose.yaml":                 true,
		"compose.override.yml":         true,
		"deploy/docker-compose.yml":    true,
		"docker-compose.prod.yaml":     true,
		"my-compose.yml":               true,
		".github/workflows/ci.yml":     true,
		".gitea/workflows/ci.yaml":     true,
		".forgejo/actions/a/x.yml":     true,
		"workflow-templates/t.yml":     true,
		"action.yml":                   true,
		"sub/action.yaml":              true,
		"README.md":                    false,
		"config/app.yml":               true,
		"charts/c/values.yaml":         true,
		"x.yaml.bak":                   false,
		".github/workflows/readme.txt": false,
	} {
		if got := m.Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestEdits(t *testing.T) {
	const src = "FROM golang:1.22-alpine AS build\nFROM alpine:3.19@" + sha1 + "\n"
	deps := extract(t, "Dockerfile", src)
	tag, pinned := deps[0], deps[1]
	for _, c := range []struct {
		name string
		u    dep.Update
		want string
	}{
		{"tag", dep.Update{Dep: tag, Type: dep.Minor, NewVersion: "1.23-alpine"},
			"FROM golang:1.23-alpine AS build\nFROM alpine:3.19@" + sha1 + "\n"},
		{"pin", dep.Update{Dep: tag, Type: dep.Pin, NewVersion: "1.22-alpine", NewDigest: sha2},
			"FROM golang:1.22-alpine@" + sha2 + " AS build\nFROM alpine:3.19@" + sha1 + "\n"},
		{"tag to digest", dep.Update{Dep: tag, Type: dep.Minor, NewVersion: "1.23-alpine", NewDigest: sha2},
			"FROM golang:1.23-alpine@" + sha2 + " AS build\nFROM alpine:3.19@" + sha1 + "\n"},
		{"digest refresh", dep.Update{Dep: pinned, Type: dep.Digest, NewVersion: "3.19", NewDigest: sha2},
			"FROM golang:1.22-alpine AS build\nFROM alpine:3.19@" + sha2 + "\n"},
		{"pinned update", dep.Update{Dep: pinned, Type: dep.Minor, NewVersion: "3.20", NewDigest: sha2},
			"FROM golang:1.22-alpine AS build\nFROM alpine:3.20@" + sha2 + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			edits, err := New().Edits(c.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply([]byte(src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Errorf("got\n%s\nwant\n%s", out, c.want)
			}
		})
	}
	if _, err := New().Edits(dep.Update{Dep: pinned, Type: dep.Minor, NewVersion: "3.20"}); err == nil || err.Error() != "cannot unpin a digest-pinned image" {
		t.Errorf("unpin err = %v", err)
	}
}

func TestEditsYAML(t *testing.T) {
	src := "services:\n  a:\n    image: 'redis:7.2'\n"
	d := extract(t, "compose.yml", src)[0]
	edits, err := New().Edits(dep.Update{Dep: d, Type: dep.Minor, NewVersion: "7.4", NewDigest: sha2})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if want := "services:\n  a:\n    image: 'redis:7.4@" + sha2 + "'\n"; err != nil || string(out) != want {
		t.Errorf("got %q, %v; want %q", out, err, want)
	}
}

const dockerfileArgs = `ARG BASE=golang:1.22
ARG Q="alpine:3.20"
ARG V=${BASE}
ARG E="a\"b"
FROM ${BASE} AS build
FROM $Q
FROM ${V}
FROM ${E}
FROM $BASE
FROM $UNKNOWN
ARG TOOL=ghcr.io/o/tool:1.0
COPY --from=build /a /b
COPY --from=0 /a /b
COPY --chown=1 --from=busybox:1.36 /bin/sh /sh
COPY --from=$TOOL /t /t
COPY --from=${MISSING} /x /x
RUN --mount=type=cache,target=/c --mount=type=bind,from=ghcr.io/o/src:2.0,source=/,target=/s make
RUN --mount=type=bind,from=build,target=/b true
COPY --from=nginx /x /y
COPY a b
ARG BASE
COPY --from=$BASE x y
ARG S=build
COPY --from=${S} x y
COPY --from=scratch x y
`

func TestExtractDockerfileArgsAndFrom(t *testing.T) {
	for name, src := range variants(dockerfileArgs) {
		t.Run(name, func(t *testing.T) {
			check(t, []byte(src), extract(t, "Dockerfile", src), []want{
				{line: 1, name: "golang", source: "docker.io/library/golang", version: "1.22", depT: TypeDockerfile},
				{line: 2, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeDockerfile, insert: `"`},
				{line: 3, name: "${V}", depT: TypeDockerfile, skip: "variable reference"},
				{line: 4, name: "${E}", depT: TypeDockerfile, skip: "variable reference"},
				{line: 10, name: "$UNKNOWN", depT: TypeDockerfile, skip: "variable reference"},
				{line: 14, name: "busybox", source: "docker.io/library/busybox", version: "1.36", depT: TypeDockerfile, insert: " /bin/sh"},
				{line: 11, name: "ghcr.io/o/tool", source: "ghcr.io/o/tool", version: "1.0", depT: TypeDockerfile},
				{line: 17, name: "ghcr.io/o/src", source: "ghcr.io/o/src", version: "2.0", depT: TypeDockerfile, insert: ",source"},
				{line: 19, name: "nginx", source: "docker.io/library/nginx", depT: TypeDockerfile, skip: "no tag"},
			})
		})
	}
}

func TestVarName(t *testing.T) {
	for in, want := range map[string]string{"$X": "X", "${a_1}": "a_1", "$": "", "${}": "", "$1a": "", "${X}y": "", "x": "", "$X-y": ""} {
		if got := varName(in); got != want {
			t.Errorf("varName(%q) = %q, want %q", in, got, want)
		}
	}
}

const kube = `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: busybox:1.36
      containers:
        - name: app
          image: "ghcr.io/stefanprodan/podinfo:6.5.0"
        - name: tpl
          image: "{{ .Values.image }}"
        - name: side
          image: nginx@` + sha1 + `
---
apiVersion: v1
kind: Pod
spec:
  ephemeralContainers:
    - image: alpine:3.20
  containers: notalist
  other:
    image: not:1.0
`

func TestExtractKubernetes(t *testing.T) {
	for name, src := range variants(kube) {
		t.Run(name, func(t *testing.T) {
			check(t, []byte(src), extract(t, "deploy/app.yaml", src), []want{
				{line: 8, name: "busybox", source: "docker.io/library/busybox", version: "1.36", depT: TypeKubernetes},
				{line: 11, name: "ghcr.io/stefanprodan/podinfo", source: "ghcr.io/stefanprodan/podinfo", version: "6.5.0", depT: TypeKubernetes, insert: `"`},
				{line: 13, name: "{{ .Values.image }}", depT: TypeKubernetes, skip: "template value"},
				{line: 15, name: "nginx", source: "docker.io/library/nginx", digest: sha1, depT: TypeKubernetes, skip: "digest without tag"},
				{line: 21, name: "alpine", source: "docker.io/library/alpine", version: "3.20", depT: TypeKubernetes},
			})
		})
	}
}

const helmValues = `image:
  registry: ghcr.io
  repository: org/controller
  tag: v1.2.3
sidecarImage:
  repository: bitnami/kubectl
  tag: "1.30.1@` + sha1 + `"
proxyImage:
  repository: envoyproxy/envoy
  tag: v1.31.0
  digest: ` + sha2 + `
tplImage:
  repository: x/y
  tag: "{{ .Chart.AppVersion }}"
emptyImage:
  repository: x/z
  tag: ""
notimg:
  repository: a/b
  tag: "1.0"
nested:
  initImage: {repository: busybox, tag: '1.36'}
varImage: {repository: $R, tag: "1"}
escImage: {repository: a, tag: "\x31.0"}
badImage: {repository: "UP", tag: "1"}
dgImage: {repository: a, tag: "1.0", digest: "sha256:\x31"}
noTag: {repository: a}
`

func TestExtractHelm(t *testing.T) {
	for name, src := range variants(helmValues) {
		t.Run(name, func(t *testing.T) {
			check(t, []byte(src), extract(t, "charts/c/values.yaml", src), []want{
				{line: 4, name: "ghcr.io/org/controller", source: "ghcr.io/org/controller", version: "v1.2.3", depT: TypeHelm},
				{line: 7, name: "bitnami/kubectl", source: "docker.io/bitnami/kubectl", version: "1.30.1", digest: sha1, depT: TypeHelm, insert: "@"},
				{line: 10, name: "envoyproxy/envoy", source: "docker.io/envoyproxy/envoy", version: "v1.31.0", digest: sha2, depT: TypeHelm},
				{line: 14, name: "x/y", depT: TypeHelm, skip: "template value"},
				{line: 17, name: "x/z", source: "docker.io/x/z", depT: TypeHelm, skip: "no tag"},
				{line: 23, name: "$R", depT: TypeHelm, skip: "variable reference"},
				{line: 24, name: "a", depT: TypeHelm, skip: "cannot locate value"},
				{line: 25, name: "UP", depT: TypeHelm, skip: "invalid reference"},
				{line: 26, name: "a", source: "docker.io/library/a", version: "1.0", depT: TypeHelm, skip: "cannot locate value"},
				{line: 22, name: "busybox", source: "docker.io/library/busybox", version: "1.36", depT: TypeHelm, insert: "'"},
			})
		})
	}
}

func TestExtractManifestsPrefilterAndErrors(t *testing.T) {
	big := strings.Repeat("key: [unterminated\n", 10000)
	before := manifestParses.Load()
	if deps, err := New().Extract("big.yaml", []byte(big), manager.Env{}); err != nil || deps != nil {
		t.Errorf("big: %v %v", deps, err)
	}
	if manifestParses.Load() != before {
		t.Error("file without image:/repository: was parsed")
	}
	// helm templates: parse errors are not errors, and drop deps of earlier documents
	for _, src := range []string{"containers:\n  - image: a:1\n---\n{{ bad\n", "image: {{ .Values.image }}\n"} {
		if deps, err := New().Extract("templates/d.yaml", []byte(src), manager.Env{}); err != nil || len(deps) != 0 {
			t.Errorf("%q: %v %v", src, deps, err)
		}
	}
	if manifestParses.Load() != before+2 {
		t.Errorf("parses = %d, want 2", manifestParses.Load()-before)
	}
}

func TestEditsManifests(t *testing.T) {
	for _, c := range []struct {
		file, src string
		u         func(d dep.Dep) dep.Update
		want      string
	}{
		{"values.yaml", "image:\n  repository: a/b\n  tag: \"1.0\"\n  digest: " + sha1 + "\n",
			func(d dep.Dep) dep.Update {
				return dep.Update{Dep: d, Type: dep.Minor, NewVersion: "1.1", NewDigest: sha2}
			},
			"image:\n  repository: a/b\n  tag: \"1.1\"\n  digest: " + sha2 + "\n"},
		{"values.yaml", "image:\n  repository: a/b\n  tag: 1.0\n",
			func(d dep.Dep) dep.Update {
				return dep.Update{Dep: d, Type: dep.Pin, NewVersion: "1.0", NewDigest: sha2}
			},
			"image:\n  repository: a/b\n  tag: 1.0@" + sha2 + "\n"},
		{"k8s.yml", "containers:\n- image: a/b:1.0\n",
			func(d dep.Dep) dep.Update { return dep.Update{Dep: d, Type: dep.Major, NewVersion: "2.0"} },
			"containers:\n- image: a/b:2.0\n"},
		{"Dockerfile", "ARG B=\"a/b:1.0\"\nFROM $B\nCOPY --from=c/d:2 x y\n",
			func(d dep.Dep) dep.Update { return dep.Update{Dep: d, Type: dep.Minor, NewVersion: "1.1"} },
			"ARG B=\"a/b:1.1\"\nFROM $B\nCOPY --from=c/d:2 x y\n"},
	} {
		d := extract(t, c.file, c.src)[0]
		edits, err := New().Edits(c.u(d))
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(c.src), edits)
		if err != nil || string(out) != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.file, out, err, c.want)
		}
	}
}
