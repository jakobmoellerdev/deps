package engine

import (
	"context"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
)

// Dockerfile package pins: apk pins are looked up in the base image's Alpine release, apt
// pins only once a rule's registryUrls names their suite, which also replaces a derived apk
// registry; a custom annotation still wins over the native dep it covers.
func TestResolveRegistryURLs(t *testing.T) {
	const (
		alpine = "https://dl-cdn.alpinelinux.org/alpine?branch=v3.21&components=main,community&arch=x86_64"
		mirror = "https://alpine.example.com/alpine?branch=v3.21&components=main&arch=x86_64"
		trixie = "https://deb.debian.org/debian?suite=trixie&components=main&binaryArch=amd64"
	)
	files := map[string]string{
		"Dockerfile": `FROM alpine:3.21
RUN apk add --no-cache bash=5.2.37-r0 curl=8.12.1-r0
FROM debian:trixie
RUN apt-get install -y git=1:2.47.3-0+deb13u1 \
  # renovate: datasource=deb depName=jq registryUrl=https://deb.debian.org/debian?suite=trixie&components=main&binaryArch=arm64
  jq=1.7.1-6 \
  vim=2:9.1.1230-2
`,
		"other/Dockerfile": "FROM debian:trixie\nRUN apt-get install -y curl=8.14.1-2\n",
	}
	cfg := `custom:
  - files: [Dockerfile]
rules:
  - datasources: [docker]
    ignore: true
  - managers: [docker]
    datasources: [deb]
    paths: [Dockerfile]
    registryUrls: ['` + trixie + `']
  - datasources: [apk]
    names: [curl]
    registryUrls: ['` + mirror + `']
`
	root, c := setup(t, cfg, files)
	ms, err := Managers(c)
	if err != nil {
		t.Fatal(err)
	}
	// The fake keys packages by registry and name: a lookup elsewhere fails.
	pkgs := fakeGo{
		alpine + " bash": {"5.2.37-r0", "5.2.37-r1"},
		mirror + " curl": {"8.12.1-r0", "8.12.1-r1"},
		trixie + " git":  {"1:2.47.3-0+deb13u1", "1:2.47.3-0+deb13u2"},
		trixie + " vim":  {"2:9.1.1230-2", "2:9.1.1230-3"},
		"https://deb.debian.org/debian?suite=trixie&components=main&binaryArch=arm64 jq": {"1.7.1-6", "1.7.1-7"},
	}
	ds := registryFake{pkgs}
	res, err := Resolve(t.Context(), root, c, env, Options{Managers: ms, Datasources: map[string]Datasource{"apk": ds, "deb": ds}})
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, u.Dep.Manager+" "+u.Dep.File+" "+u.Dep.Name+" "+u.NewVersion)
	}
	slices.Sort(got)
	want := []string{
		"custom Dockerfile jq 1.7.1-7",
		"docker Dockerfile bash 5.2.37-r1",
		"docker Dockerfile curl 8.12.1-r1",
		"docker Dockerfile git 1:2.47.3-0+deb13u2",
		"docker Dockerfile vim 2:9.1.1230-3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("updates =\n%q\nwant\n%q", got, want)
	}
	skips := map[string]string{}
	for _, d := range res.Deps {
		if d.DepType == docker.TypeInstall {
			skips[d.File+" "+d.Name] = d.Skip
		}
	}
	if s := skips["other/Dockerfile curl"]; s != dep.SkipUnknownRegistry {
		t.Errorf("other/Dockerfile curl skip = %q", s)
	}
	if s := skips["Dockerfile jq"]; s != "covered by annotation on line 5" {
		t.Errorf("Dockerfile jq skip = %q", s)
	}
}

// registryFake looks packages up by "<registry> <name>".
type registryFake struct{ fakeGo }

func (f registryFake) Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) {
	d.Name = d.Source + " " + d.Name
	return f.fakeGo.Releases(ctx, d, dir)
}

// Packages a Dockerfile RUN installs are not images: pin: digest leaves them alone.
func TestDigestCapableInstall(t *testing.T) {
	d := dep.Dep{Manager: docker.Name, Datasource: "apk", DepType: docker.TypeInstall, Version: "1.0-r0"}
	if digestCapable(d) {
		t.Error("apk install dep is digest capable")
	}
	d.Datasource, d.DepType = "docker", docker.TypeDockerfile
	if !digestCapable(d) {
		t.Error("dockerfile image is not digest capable")
	}
}
