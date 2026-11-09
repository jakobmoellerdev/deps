package quadlet

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	line                int
	name, version, skip string
}

func extract(t *testing.T, src []byte, env manager.Env) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("app.container", src, env)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.Datasource != "docker" || d.Versioning != "docker" || d.DepType != TypeImage {
			t.Errorf("%d: %s/%s/%s/%s", i, d.Manager, d.Datasource, d.Versioning, d.DepType)
		}
		if d.Skip == "" && !docker.Untagged(d) && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
			t.Errorf("%d: src[RefSpan] = %q, want %q", i, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Line != w.line || d.Name != w.name || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got line=%d name=%q version=%q skip=%q\nwant %+v", i, d.Line, d.Name, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		"app.container":                            true,
		".config/containers/systemd/db.volume":     true,
		"quadlets/alpine.image":                    true,
		".container":                               false,
		"app.container.d/override.conf":            false,
		"app.network":                              false,
		"app.pod":                                  false,
		"Containerfile":                            false,
		"docs/app.containers":                      false,
		"etc/containers/systemd/app.kube":          false,
		"etc/containers/systemd/app.container.bak": false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`# comment Image=ignored:1
[Unit]
Description=Image=not-a-key:1

[Container]
Image=docker.io/library/nginx:1.27
Exec=sh -c "echo \
Image=continued:1"
Image = "ghcr.io/acme/app:v1.2.3@sha256:1111111111111111111111111111111111111111111111111111111111111111"

[Image]
Image=docker://quay.io/prometheus/node-exporter:v1.8.0

[Volume]
Driver=image
Image=docker-daemon:alpine

[Service]
Image=not-quadlet:1
`)
	check(t, extract(t, src, manager.Env{}), []want{
		{line: 9, name: "ghcr.io/acme/app", version: "v1.2.3"},
		{line: 12, name: "quay.io/prometheus/node-exporter", version: "v1.8.0"},
		{line: 16, name: "alpine", version: "latest"},
	})
	if d := extract(t, src, manager.Env{})[0]; d.Digest == "" || string(src[d.DigestSpan.Start:d.DigestSpan.End]) != d.Digest {
		t.Errorf("digest = %q, span %+v", d.Digest, d.DigestSpan)
	}
}

func TestExtractSkips(t *testing.T) {
	src := []byte("[Container]\nImage=app.build\n\n[Volume]\nImage=data.image\n\n[Image]\nImage=oci-archive:/tmp/app.tar\n")
	check(t, extract(t, src, manager.Env{}), []want{
		{line: 2, name: "app.build", skip: skipUnit},
		{line: 5, name: "data.image", skip: skipUnit},
		{line: 8, name: "oci-archive:/tmp/app.tar", skip: skipTransport},
	})
	src = []byte("[Container]\nImage=localhost/app\n[Image]\nImage=${REGISTRY}/app:1\n[Volume]\nImage=quay.io/a/b:1\\\n  :2\n")
	check(t, extract(t, src, manager.Env{}), []want{
		{line: 2, name: "localhost/app", skip: skipLocal},
		{line: 4, name: "${REGISTRY}/app:1", skip: "variable reference"},
		{line: 6, name: "quay.io/a/b:1", skip: "cannot locate value"},
	})
	deps := extract(t, src, manager.Env{RegistryAliases: map[string]string{"${REGISTRY}": "ghcr.io/acme"}})
	if d := deps[1]; d.Skip != "" || d.Source != "ghcr.io/acme/app" || d.Version != "1" {
		t.Errorf("aliased = %+v", d)
	}
	// An empty Image= resets the key.
	src = []byte("[Container]\nImage=alpine:3\nImage=\n[Image]\nImage=quay.io/a/b:1\n")
	check(t, extract(t, src, manager.Env{}), []want{{line: 5, name: "quay.io/a/b", version: "1"}})
	check(t, extract(t, nil, manager.Env{}), nil)
}

// The fixtures are rgolangh/podman-quadlets units and Podman's own quadlet test units (`##`
// lines are test assertions, comments to systemd).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		wants []want
	}{
		{"testdata/redpanda-0.container", []want{{line: 9, name: "docker.redpanda.com/redpandadata/redpanda", version: "v23.3.4"}}},
		{"testdata/maybe-db.container", []want{{line: 15, name: "postgres", version: "16"}}},
		{"testdata/custom-local.container", []want{{line: 12, name: "custom.build", skip: skipUnit}}},
		{"testdata/image.quadlet.volume", []want{{line: 8, name: "basic.image", skip: skipUnit}}},
		{"testdata/all-tags.image", []want{{line: 5, name: "localhost/imagename", skip: skipLocal}}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			check(t, extract(t, src, manager.Env{}), tt.wants)
		})
	}
}

func TestEditsAndRename(t *testing.T) {
	src := []byte("[Container]\nImage=docker://docker.io/library/postgres:16\n")
	deps := extract(t, src, manager.Env{})
	sha := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "17", NewDigest: sha})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "[Container]\nImage=docker://docker.io/library/postgres:17@" + sha + "\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	d, err := New().(manager.Renamer).Rename(deps[0], "ghcr.io/acme/postgres")
	if err != nil || d.Source != "ghcr.io/acme/postgres" {
		t.Errorf("Rename = %+v, %v", d, err)
	}
}
