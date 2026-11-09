package docker

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const (
	alpine321Arm = "https://dl-cdn.alpinelinux.org/alpine?branch=v3.21&components=main,community&arch=aarch64"
	alpine320    = "https://dl-cdn.alpinelinux.org/alpine?branch=v3.20&components=main,community&arch=x86_64"
	wolfi        = "https://packages.wolfi.dev/os?arch=x86_64"
)

// installWant is one extracted install dep.
type installWant struct {
	line                      int
	datasource, name, version string
	source, skip              string
}

func installDeps(deps []dep.Dep) []dep.Dep {
	var out []dep.Dep
	for _, d := range deps {
		if d.DepType == TypeInstall {
			out = append(out, d)
		}
	}
	return out
}

func checkInstalls(t *testing.T, src []byte, deps []dep.Dep, wants []installWant) {
	t.Helper()
	deps = installDeps(deps)
	if len(deps) != len(wants) {
		t.Fatalf("got %d install deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Manager != Name || d.Datasource != w.datasource || d.Versioning != w.datasource || d.Line != w.line || d.Name != w.name ||
			d.Version != w.version || d.Source != w.source || d.Skip != w.skip {
			t.Errorf("%d: got %s/%s line=%d name=%q version=%q source=%q skip=%q\nwant %+v", i, d.Datasource, d.Versioning, d.Line, d.Name, d.Version, d.Source, d.Skip, w)
			continue
		}
		if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != w.version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, got, w.version)
		}
	}
}

func TestExtractInstalls(t *testing.T) {
	raw, err := os.ReadFile("testdata/Dockerfile.install")
	if err != nil {
		t.Fatal(err)
	}
	wants := []installWant{
		{5, "apk", "bash", "5.2.37-r0", alpine321Arm, ""},
		{6, "apk", "git", "2.47.2-r0", alpine321Arm, ""},
		{11, "apk", "jq", "1.7.1-r0", alpine321Arm, ""},
		{14, "apk", "tzdata", "2024b-r0", alpine320, ""},
		{14, "apk", "curl", "8.12", "", skipConstraint},
		{14, "apk", "openssl", "=3", "", skipConstraint},
		{14, "apk", "bash", "${BASH_VERSION}", "", skipVariable},
		{15, "apk", "rclone", "1.68.2-r0", "", dep.SkipUnknownRegistry},
		{16, "apk", "nginx", "1.27.3-r0", "", dep.SkipUnknownRegistry},
		{19, "apk", "ca-certificates", "20241121-r1", "", dep.SkipUnknownRegistry},
		{22, "apk", "curl", "8.11.1-r0", wolfi, ""},
		{27, "deb", "curl", "8.14.1-2", "", dep.SkipUnknownRegistry},
		{28, "deb", "git", "1:2.47.3-0+deb13u1", "", dep.SkipUnknownRegistry},
		{29, "deb", "jq", "1.7*", "", skipConstraint},
	}
	for name, src := range variants(string(raw)) {
		t.Run(name, func(t *testing.T) {
			deps := extract(t, "Dockerfile", src)
			checkInstalls(t, []byte(src), deps, wants)
			// The images are extracted as before.
			var images []string
			for _, d := range deps {
				if d.DepType == TypeDockerfile {
					images = append(images, d.Name+":"+d.Version)
				}
			}
			if got := strings.Join(images, " "); got != "node:22-alpine3.21 alpine:3.20 alpine:latest cgr.dev/chainguard/wolfi-base:latest debian:trixie-slim" {
				t.Errorf("images = %s", got)
			}
		})
	}
}

func TestApkRegistry(t *testing.T) {
	for _, c := range []struct{ ref, platform, want string }{
		{"alpine:3.21", "", "https://dl-cdn.alpinelinux.org/alpine?branch=v3.21&components=main,community&arch=x86_64"},
		{"alpine:3.21.3", "linux/arm/v7", "https://dl-cdn.alpinelinux.org/alpine?branch=v3.21&components=main,community&arch=armv7"},
		{"docker.io/library/alpine:3.21.3@" + sha1, "linux/amd64", "https://dl-cdn.alpinelinux.org/alpine?branch=v3.21&components=main,community&arch=x86_64"},
		{"alpine", "", ""},
		{"alpine:3", "", ""},
		{"alpine:3.21-foo", "", ""},
		{"python:3.13-alpine3.21", "$BUILDPLATFORM", "https://dl-cdn.alpinelinux.org/alpine?branch=v3.21&components=main,community&arch=x86_64"},
		{"golang:1.23.4-alpine3.20", "linux/arm64/v8", "https://dl-cdn.alpinelinux.org/alpine?branch=v3.20&components=main,community&arch=aarch64"},
		{"node:22-alpine", "", ""},
		{"chainguard/wolfi-base:latest", "linux/arm64", "https://packages.wolfi.dev/os?arch=aarch64"},
		{"cgr.dev/chainguard/python:latest-dev", "", wolfi},
		{"debian:bookworm", "", ""},
		{"Invalid Ref", "", ""},
	} {
		if got := apkRegistry(c.ref, c.platform); got != c.want {
			t.Errorf("apkRegistry(%q, %q) = %q, want %q", c.ref, c.platform, got, c.want)
		}
	}
}

func TestExtractInstallsScopes(t *testing.T) {
	cases := []struct {
		name, src string
		aliases   map[string]string
		wants     []installWant
	}{
		{
			name:    "registry alias and digest",
			src:     "FROM ${REGISTRY}/library/alpine:3.20@" + sha1 + "\nRUN apk add bash=5.2.26-r0\n",
			aliases: map[string]string{"${REGISTRY}": "docker.io"},
			wants:   []installWant{{2, "apk", "bash", "5.2.26-r0", alpine320, ""}},
		},
		{
			name:  "unresolved variable base",
			src:   "ARG BASE\nFROM ${BASE}\nRUN apk add bash=5.2.26-r0\n",
			wants: []installWant{{3, "apk", "bash", "5.2.26-r0", "", dep.SkipUnknownRegistry}},
		},
		{
			name:  "unnamed stage, no subcommand, unbalanced quote, empty version",
			src:   "FROM alpine:3.20\nRUN apk --no-cache add 'bash=1 \"curl=8.1-r0\" zlib= ./local.apk\nRUN apk info -v && apt-get -y install\nRUN apk update\n",
			wants: []installWant{{2, "apk", "curl", "8.1-r0", alpine320, ""}},
		},
		{
			name:  "apt release option and pipes",
			src:   "FROM ubuntu:24.04\nRUN apt install -t noble-updates curl=8.5.0-2ubuntu10.6 libc6:i386=2.39 vim- '^gnome' x=$V | tee log || true\n",
			wants: []installWant{{2, "deb", "curl", "8.5.0-2ubuntu10.6", "", dep.SkipUnknownRegistry}, {2, "deb", "x", "$V", "", skipVariable}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps, err := New().Extract("Dockerfile", []byte(c.src), manager.Env{RegistryAliases: c.aliases})
			if err != nil {
				t.Fatal(err)
			}
			checkInstalls(t, []byte(c.src), deps, c.wants)
		})
	}
}

func TestEditsInstall(t *testing.T) {
	src := "FROM alpine:3.21\nRUN apk add --no-cache bash=5.2.37-r0 \\\n  curl=8.12.1-r0\n"
	deps := installDeps(extract(t, "Dockerfile", src))
	if len(deps) != 2 {
		t.Fatalf("deps = %+v", deps)
	}
	edits, err := New().Edits(dep.Update{Dep: deps[1], NewVersion: "8.12.1-r1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(src, "curl=8.12.1-r0", "curl=8.12.1-r1", 1); string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
