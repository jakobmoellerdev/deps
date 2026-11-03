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
				{line: 15, name: "ubuntu", source: "docker.io/library/ubuntu", version: "latest", depT: TypeDockerfile, skip: "non-version tag"},
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
				{line: 11, name: "${REGISTRY}/x:1.0", depT: TypeCompose, skip: "variable reference"},
				{line: 13, name: "nginx", source: "docker.io/library/nginx", depT: TypeCompose, skip: "no tag"},
				{line: 14, name: "caddy", source: "docker.io/library/caddy", version: "2.8", depT: TypeCompose, insert: "}"},
				{line: 16, name: "alpine:3.19", depT: TypeCompose, skip: "cannot locate value"},
				{line: 18, name: "alpine:3.19\n", depT: TypeCompose, skip: "cannot locate value"},
				{line: 21, name: "busybox", source: "docker.io/library/busybox", version: "1.36", depT: TypeCompose},
			})
		})
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
		"my-compose.yml":               false,
		".github/workflows/ci.yml":     true,
		".gitea/workflows/ci.yaml":     true,
		".forgejo/actions/a/x.yml":     true,
		"workflow-templates/t.yml":     true,
		"action.yml":                   true,
		"sub/action.yaml":              true,
		"README.md":                    false,
		"config/app.yml":               false,
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
