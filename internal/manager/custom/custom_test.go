package custom

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func one(t *testing.T, c config.Custom) manager.Manager {
	t.Helper()
	return oneWith(t, &config.Config{Custom: []config.Custom{c}})
}

// oneWith returns the manager of the single cfg.Custom entry.
func oneWith(t *testing.T, cfg *config.Config) manager.Manager {
	t.Helper()
	ms, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Name() != Name {
		t.Fatalf("got %d managers", len(ms))
	}
	return ms[0]
}

func extract(t *testing.T, m manager.Manager, src string) []dep.Dep {
	t.Helper()
	deps, err := m.Extract("f", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

// want describes an extracted dep; ref is the text at RefSpan, digest the text at DigestSpan.
type want struct {
	line                             int
	datasource, name, source, versio string
	version, digest, skip            string
}

func check(t *testing.T, src string, deps []dep.Dep, depType string, wants []want) {
	t.Helper()
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Manager != Name || d.DepType != depType || d.File != "f" || d.Line != w.line || d.Datasource != w.datasource ||
			d.Name != w.name || d.Source != w.source || d.Versioning != w.versio || d.Version != w.version ||
			d.Digest != w.digest || d.Skip != w.skip {
			t.Errorf("dep %d = %+v, want %+v", i, d, w)
			continue
		}
		if got := src[d.RefSpan.Start:d.RefSpan.End]; got != w.version {
			t.Errorf("dep %d RefSpan text %q, want %q", i, got, w.version)
		}
		if got := src[d.DigestSpan.Start:d.DigestSpan.End]; got != w.digest {
			t.Errorf("dep %d DigestSpan text %q, want %q", i, got, w.digest)
		}
	}
}

func TestAnnotations(t *testing.T) {
	src := strings.Join([]string{
		"# deps: go golang.org/x/tools",                                     // 1
		"TOOLS_VERSION ?= v0.30.0 # next: 1.2.3",                            // 2
		"// deps: github-releases golangci/golangci-lint versioning=semver", // 3
		`const lint = "2.5.0"`,                                              // 4
		"  -- deps: docker nginx versioning=docker",                         // 5
		"image: nginx:1.27.3-alpine",                                        // 6
		"; deps: git-tags github.com/actions/checkout",                      // 7
		"ref=@v6.0.1.", // 8
		"<!-- deps: git-tags https://gitea.com/o/r -->", // 9
		"go1.2.3 x1.2 1.2.3abc 1.2.3-rc.1+b5, more",     // 10
		"# deps: docker alpine",                         // 11
		"# deps: go example.com/m",                      // 12 annotation on annotation's next line
		"v1.0.0",                                        // 13
		"# deps: nuget Foo",                             // 14
		"1.0.0",                                         // 15
		"# deps: go example.com/v versioning=bogus",     // 16
		"1.0.0",                         // 17
		"# deps: git-tags ftp://x/y",    // 18
		"1.0.0",                         // 19
		"# deps: github-releases a/b/c", // 20
		"1.0.0",                         // 21
		"# deps: go example.com/none",   // 22
		"version v1 only",               // 23
		"deps: go not.annotated",        // 24 no comment marker
		"1.0.0",                         // 25
		"# deps: go example.com/last",   // 26 last line
	}, "\n")
	for name, s := range map[string]string{"lf": src, "crlf": strings.ReplaceAll(src, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			m := one(t, config.Custom{Files: []string{"**/Makefile"}})
			check(t, s, extract(t, m, s), DepTypeAnnotation, []want{
				{2, "go", "golang.org/x/tools", "", "semver", "v0.30.0", "", ""},
				{4, "github-releases", "golangci/golangci-lint", "https://github.com/golangci/golangci-lint", "semver", "2.5.0", "", ""},
				{6, "docker", "nginx", "", "docker", "1.27.3-alpine", "", ""},
				{8, "git-tags", "github.com/actions/checkout", "https://github.com/actions/checkout", "semver", "v6.0.1", "", ""},
				{10, "git-tags", "https://gitea.com/o/r", "https://gitea.com/o/r", "semver", "1.2.3-rc.1+b5", "", ""},
				{12, "docker", "alpine", "", "semver", "", "", "no version on next line"},
				{13, "go", "example.com/m", "", "semver", "v1.0.0", "", ""},
				{15, "nuget", "Foo", "", "semver", "", "", `unknown datasource "nuget"`},
				{17, "go", "example.com/v", "", "bogus", "", "", `unknown versioning "bogus"`},
				{19, "git-tags", "ftp://x/y", "", "semver", "", "", `invalid git-tags name "ftp://x/y"`},
				{21, "github-releases", "a/b/c", "", "semver", "", "", `invalid github-releases name "a/b/c"`},
				{23, "go", "example.com/none", "", "semver", "", "", "no version on next line"},
				{26, "go", "example.com/last", "", "semver", "", "", "no version on next line"},
			})
		})
	}
}

func TestHelmAnnotations(t *testing.T) {
	src := strings.Join([]string{
		"# deps: helm https://charts.jetstack.io/cert-manager",                         // 1
		"CERT_MANAGER=v1.14.0",                                                         // 2
		"# deps: helm oci://registry-1.docker.io/bitnamicharts redis versioning=loose", // 3
		"redis: 18.0.0", // 4
		"<!-- deps: helm https://example.com/charts/ app -->", // 5
		"app 1.2.3", // 6
		"# renovate: datasource=helm depName=nginx registryUrl=https://charts.bitnami.com/bitnami", // 7
		"nginx 15.0.0",              // 8
		"# deps: helm cert-manager", // 9
		"1.0.0",                     // 10
		"# deps: helm ftp://x/y",    // 11
		"1.0.0",                     // 12
		"# deps: helm oci:///x",     // 13
		"1.0.0",                     // 14
		"# renovate: datasource=helm depName=a/b registryUrl=https://x", // 15
		"1.0.0", // 16
	}, "\n")
	m := one(t, config.Custom{Files: []string{"f"}})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "helm", "cert-manager", "https://charts.jetstack.io", "semver", "v1.14.0", "", ""},
		{4, "helm", "redis", "oci://registry-1.docker.io/bitnamicharts", "loose", "18.0.0", "", ""},
		{6, "helm", "app", "https://example.com/charts", "semver", "1.2.3", "", ""},
		{8, "helm", "nginx", "https://charts.bitnami.com/bitnami", "semver", "15.0.0", "", ""},
		{10, "helm", "cert-manager", "", "semver", "", "", `invalid helm name "cert-manager"`},
		{12, "helm", "ftp://x/y", "", "semver", "", "", `invalid helm name "ftp://x/y"`},
		{14, "helm", "oci:///x", "", "semver", "", "", `invalid helm name "oci:///x"`},
		{16, "helm", "a/b", "https://x", "semver", "", "", `invalid helm name "https://x a/b"`},
	})
}

func TestAnnotationEntryVersioning(t *testing.T) {
	m := one(t, config.Custom{Files: []string{"x"}, Versioning: "tag"})
	src := "# deps: git-tags https://github.com/o/r/\nv1.2\n"
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "git-tags", "https://github.com/o/r/", "https://github.com/o/r", "tag", "v1.2", "", ""},
	})
	// Entries accept regex schemes; marker fields accept scheme names only.
	const re = `regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)\+ocm\.(?<build>\d+)$`
	m = one(t, config.Custom{Files: []string{"x"}, Versioning: re})
	src = "# renovate: datasource=github-releases depName=o/cosign\nV=v3.1.4+ocm.6\n" +
		"# deps: github-releases o/x versioning=loose\nX=1.2.3.4\n" +
		"# renovate: datasource=github-releases depName=o/y versioning=regex:^(?<major>[0-9]+)$\n7\n"
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "github-releases", "o/cosign", "https://github.com/o/cosign", re, "v3.1.4+ocm.6", "", ""},
		{4, "github-releases", "o/x", "https://github.com/o/x", "loose", "1.2.3.4", "", ""},
		{6, "github-releases", "o/y", "https://github.com/o/y", `regex:^(?<major>[0-9]+)$`, "", "", `unknown versioning "regex:^(?<major>[0-9]+)$"`},
	})
}

func TestRegex(t *testing.T) {
	src := "FROM a\n" +
		"ARG LINT=v2.5.0 # golangci/golangci-lint\n" +
		"ARG GH=2.80.0 # cli/cli\n" +
		"ARG BAD= # x/y\n" +
		"ARG IMG=nginx:1.27.3@sha256:abc123 # img\n"
	cases := []struct {
		name  string
		cfg   config.Custom
		wants []want
	}{
		{
			name: "name group, fixed datasource",
			cfg:  config.Custom{Files: []string{"Dockerfile"}, Match: `ARG \w+=(?P<version>v?[\d.]*) # (?P<name>\S+)`, Datasource: "github-releases"},
			wants: []want{
				{2, "github-releases", "golangci/golangci-lint", "https://github.com/golangci/golangci-lint", "semver", "v2.5.0", "", ""},
				{3, "github-releases", "cli/cli", "https://github.com/cli/cli", "semver", "2.80.0", "", ""},
				{4, "github-releases", "x/y", "", "semver", "", "", "empty version"},
			},
		},
		{
			name: "digest and datasource groups, fixed name",
			cfg: config.Custom{Files: []string{"Dockerfile"}, Name: "nginx", Versioning: "docker",
				Match: `=(?P<image>\w+):(?P<version>[\w.-]+)@(?P<digest>sha256:[0-9a-f]+) # (?P<datasource>img)`},
			wants: []want{
				{5, "img", "nginx", "", "docker", "1.27.3", "sha256:abc123", `unknown datasource "img"`},
			},
		},
		{
			name: "datasource group",
			cfg:  config.Custom{Files: []string{"Dockerfile"}, Name: "golang.org/x/tools", Match: `(?P<datasource>go)?=(?P<version>v2\S+)`},
			wants: []want{
				{2, "", "golang.org/x/tools", "", "semver", "", "", "empty datasource"},
			},
		},
		{
			name: "empty name group",
			cfg:  config.Custom{Files: []string{"Dockerfile"}, Datasource: "go", Match: `(?P<name>z*)GH=(?P<version>\S+)`},
			wants: []want{
				{3, "go", "", "", "semver", "", "", "empty name"},
			},
		},
		{
			name: "optional version group absent",
			cfg:  config.Custom{Files: []string{"Dockerfile"}, Datasource: "go", Name: "m", Match: `FROM (?P<version>\d+)?`},
			wants: []want{
				{1, "go", "m", "", "semver", "", "", "empty version"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			check(t, src, extract(t, one(t, c.cfg), src), DepTypeRegex, c.wants)
		})
	}
}

func TestMatch(t *testing.T) {
	m := one(t, config.Custom{Files: []string{"**/Makefile", "tools/*.mk"}})
	for path, ok := range map[string]bool{
		"Makefile": true, "a/b/Makefile": true, "tools/x.mk": true, "tools/a/x.mk": false, "Makefile.bak": false,
	} {
		if got := m.Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestNewErrors(t *testing.T) {
	for _, c := range []struct {
		cfg  config.Custom
		want string
	}{
		{config.Custom{}, "custom[1]: files: must not be empty"},
		{config.Custom{Files: []string{"a/[b"}}, `files: invalid glob "a/[b"`},
		{config.Custom{Files: []string{"x"}, Versioning: "nope"}, `versioning: must be one of tag, go, docker, semver, loose, cargo, pep440 or regex:<RE2>, got "nope"`},
		{config.Custom{Files: []string{"x"}, Versioning: "regex:(?<minor>1)"}, `versioning: regex: must have a (?<major>...) group`},
		{config.Custom{Files: []string{"x"}, Datasource: "nuget"}, `datasource: unknown "nuget"`},
		{config.Custom{Files: []string{"x"}, Datasource: "custom.envtest"}, `datasource: unknown "custom.envtest"`},
		{config.Custom{Files: []string{"x"}, Match: "("}, "match: error parsing regexp"},
		{config.Custom{Files: []string{"x"}, Match: "a", Datasource: "go", Name: "n"}, "match: must have a (?P<version>...) group"},
		{config.Custom{Files: []string{"x"}, Match: "(?P<version>a)", Name: "n"}, "datasource: required unless"},
		{config.Custom{Files: []string{"x"}, Match: "(?P<version>a)", Datasource: "go"}, "name: required unless"},
		{config.Custom{Files: []string{"x"}, Match: "(?P<version>a)", Datasource: "go", Name: "n", Checksum: &config.Checksum{URL: "u"}}, "checksum: requires a (?P<checksum>...) group"},
		{config.Custom{Files: []string{"x"}, Match: "(?P<version>a)(?P<checksum>b)", Datasource: "go", Name: "n"}, "checksum: requires a (?P<checksum>...) group"},
	} {
		ms, err := New(&config.Config{Custom: []config.Custom{{Files: []string{"ok"}}, c.cfg}})
		if err == nil || ms != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%+v) = %v, %v; want error containing %q", c.cfg, ms, err, c.want)
		}
	}
}

func TestNewMany(t *testing.T) {
	ms, err := New(&config.Config{Custom: []config.Custom{{Files: []string{"a"}}, {Files: []string{"b"}, Match: "(?P<version>x)(?P<name>y)(?P<datasource>z)"}}})
	if err != nil || len(ms) != 2 || !ms[0].Match("a") || !ms[1].Match("b") || ms[0].Match("b") {
		t.Fatalf("New = %v, %v", ms, err)
	}
}

// A regex entry may use a defined custom datasource (OCM's controller .env envtest entry).
func TestRegexCustomDatasource(t *testing.T) {
	m := oneWith(t, &config.Config{
		Custom: []config.Custom{{Files: []string{".env"}, Datasource: "custom.envtest", Name: "envtest",
			Match: `ENVTEST_K8S_VERSION=v?(?P<version>\d+\.\d+\.\d+)`}},
		CustomDatasources: map[string]config.CustomDatasource{"envtest": {URL: "https://x/envtest.yaml", Format: "yaml"}},
	})
	src := "# renovate: datasource=custom.envtest depName=envtest\nENVTEST_K8S_VERSION=1.36.0\n"
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{2, "custom.envtest", "envtest", "", "semver", "1.36.0", "", ""},
	})
}

func TestEdits(t *testing.T) {
	src := "# deps: go golang.org/x/tools\nTOOLS ?= v0.30.0\n" +
		"IMG=nginx:1.27.3@sha256:aaa\n"
	ann := one(t, config.Custom{Files: []string{"x"}})
	re := one(t, config.Custom{Files: []string{"x"}, Datasource: "docker", Name: "nginx", Versioning: "docker",
		Match: `nginx:(?P<version>[\w.-]+)@(?P<digest>sha256:\w+)`})
	a := extract(t, ann, src)[0]
	r := extract(t, re, src)[0]

	cases := []struct {
		name    string
		updates []dep.Update
		want    string
		err     string
	}{
		{
			name: "version and digest",
			updates: []dep.Update{
				{Dep: a, NewVersion: "v0.31.0"},
				{Dep: r, NewVersion: "1.27.4", NewDigest: "sha256:bbbb"},
			},
			want: "# deps: go golang.org/x/tools\nTOOLS ?= v0.31.0\nIMG=nginx:1.27.4@sha256:bbbb\n",
		},
		{
			name:    "digest only",
			updates: []dep.Update{{Dep: r, NewVersion: "1.27.3", NewDigest: "sha256:cc"}},
			want:    "# deps: go golang.org/x/tools\nTOOLS ?= v0.30.0\nIMG=nginx:1.27.3@sha256:cc\n",
		},
		{name: "digest without span", updates: []dep.Update{{Dep: a, NewVersion: "v0.31.0", NewDigest: "sha256:x"}}, err: "no digest to update"},
		{name: "stale digest", updates: []dep.Update{{Dep: r, NewVersion: "1.27.4"}}, err: "digest must be updated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var edits []patch.Edit
			for _, u := range c.updates {
				e, err := ann.Edits(u) // any custom manager edits any custom dep
				if err != nil {
					if c.err == "" || !strings.Contains(err.Error(), c.err) {
						t.Fatalf("Edits: %v, want %q", err, c.err)
					}
					return
				}
				edits = append(edits, e...)
			}
			if c.err != "" {
				t.Fatalf("Edits succeeded, want %q", c.err)
			}
			got, err := patch.Apply([]byte(src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestChecksum(t *testing.T) {
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	src := "FROM x\nARG GH_VERSION=2.60.0\nARG GH_SHA256=" + h1 + "\n" +
		"ARG GH_VERSION=2.61.0\nARG GH_SHA256=" + h1[:10] + "\n"
	cs := &config.Checksum{Asset: "gh_{{version}}_linux_amd64.tar.gz"}
	m := one(t, config.Custom{Files: []string{"x"}, Datasource: "github-releases", Name: "cli/cli", Checksum: cs,
		Match: `ARG GH_VERSION=(?P<version>\S+)\nARG GH_SHA256=(?P<checksum>[0-9a-f]+)`})
	deps := extract(t, m, src)
	if len(deps) != 2 {
		t.Fatalf("deps = %+v", deps)
	}
	d := deps[0]
	if d.Skip != "" || d.Line != 2 || d.Checksum != h1 || src[d.ChecksumSpan.Start:d.ChecksumSpan.End] != h1 ||
		src[d.RefSpan.Start:d.RefSpan.End] != "2.60.0" || *d.ChecksumSource != (dep.ChecksumSource{Asset: cs.Asset}) {
		t.Errorf("dep = %+v", d)
	}
	if deps[1].Skip != "checksum is not 64 or 128 hex digits" {
		t.Errorf("short checksum: skip = %q", deps[1].Skip)
	}

	for _, c := range []struct {
		name string
		u    dep.Update
		want string
		err  string
	}{
		{"version and checksum", dep.Update{Dep: d, NewVersion: "2.61.0", NewChecksum: h2},
			"FROM x\nARG GH_VERSION=2.61.0\nARG GH_SHA256=" + h2 + "\n", ""},
		{"missing checksum", dep.Update{Dep: d, NewVersion: "2.61.0"}, "", "checksum must be updated together with the version"},
		{"length mismatch", dep.Update{Dep: d, NewVersion: "2.61.0", NewChecksum: h2 + h2}, "", "checksum length mismatch"},
		{"no checksum span", dep.Update{Dep: dep.Dep{Version: "1", RefSpan: d.RefSpan}, NewVersion: "2", NewChecksum: h2}, "", "no checksum to update"},
	} {
		t.Run(c.name, func(t *testing.T) {
			edits, err := m.Edits(c.u)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("Edits: %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := patch.Apply([]byte(src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(got), c.want) {
				t.Errorf("got %q, want prefix %q", got, c.want)
			}
		})
	}
}

// TestRenovateMarkers uses the marker shapes of open-component-model's .env, workflows,
// envtest.go, netlify.toml and yaml fixtures.
func TestRenovateMarkers(t *testing.T) {
	const dockleDigest = "sha256:eade932f793742de0aa8755406c7677cd7696f8675b6180926f7eeffa7abe6b9"
	const scapDigest = "sha256:065ffd57a1b45e9249fa52f514da4baa0b62d35ac27fd1cc56f4d195c2a9f43e"
	src := strings.Join([]string{
		"# renovate: datasource=github-releases depName=golangci/golangci-lint",                      // 1
		"GOLANGCI_LINT_VERSION=v2.15.0",                                                              // 2
		"# renovate: datasource=docker depName=goodwithtech/dockle",                                  // 3
		"DOCKLE_IMAGE=goodwithtech/dockle:v0.4.15@" + dockleDigest,                                   // 4
		"# renovate: datasource=docker depName=cgr.dev/chainguard/openscap",                          // 5
		"OPENSCAP_IMAGE=cgr.dev/chainguard/openscap:latest@" + scapDigest,                            // 6
		"          # renovate: datasource=github-releases depName=helm/helm",                         // 7
		"          version: v4.3.0",                                                                  // 8
		"  # renovate: datasource=github-releases depName=open-component-model/open-component-model", // 9
		"  semver: 0.16.0", // 10
		"// renovate: datasource=custom.envtest depName=envtest",  // 11
		`const DefaultEnvTestVersion = "1.36.0"`,                  // 12
		"  # renovate: datasource=golang-version depName=go",      // 13
		`  GO_VERSION = "1.27.1"`,                                 // 14
		"  # renovate: datasource=node-version depName=node",      // 15
		`  NODE_VERSION = "26.5.0"`,                               // 16
		"  # renovate: datasource=npm depName=npm",                // 17
		`  NPM_VERSION = "12.2.0"`,                                // 18
		"# renovate: datasource=go depName=k8s.io/code-generator", // 19
		"DEEPCOPY_GEN_VERSION=v0.37.1",                            // 20
		"# renovate: depName=x/y datasource=github-tags packageName=o/r versioning=tag registryUrl=https://x", // 21
		"REF=v1.2", // 22
		"<!-- renovate: datasource=docker depName=nginx -->", // 23
		"image: nginx:1.27.3-alpine",                         // 24
		"# renovate: datasource=docker depName=alpine",       // 25
		"ALPINE=3.20",               // 26 no image ref: version token
		"# renovate: depName=x",     // 27
		"1.0.0",                     // 28
		"# renovate: datasource=go", // 29
		"1.0.0",                     // 30
		"# renovate: datasource=go depName=a versioning=loose", // 31
		"1.0.0", // 32
		"# renovate: datasource=github-release-attachments depName=o/r", // 33
		"# renovate: datasource=docker depName=busybox",                 // 34 marker on marker's next line
		"busybox:1.36", // 35
		"# renovate: datasource=custom.missing depName=x", // 36 undefined custom datasource
		"1.0.0", // 37
		"# renovate: datasource=node depName=node", // 38 Renovate's old name
		"NODE=v26.5.0", // 39
		"# renovate: datasource=pypi depName=Django", // 40
		"DJANGO_VERSION=4.2.11",                      // 41
	}, "\n")
	m := oneWith(t, &config.Config{Custom: []config.Custom{{Files: []string{"**/.env"}}},
		CustomDatasources: map[string]config.CustomDatasource{"envtest": {URL: "https://x/envtest.yaml"}}})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "github-releases", "golangci/golangci-lint", "https://github.com/golangci/golangci-lint", "semver", "v2.15.0", "", ""},
		{4, "docker", "goodwithtech/dockle", "", "docker", "v0.4.15", dockleDigest, ""},
		{6, "docker", "cgr.dev/chainguard/openscap", "", "docker", "latest", scapDigest, ""},
		{8, "github-releases", "helm/helm", "https://github.com/helm/helm", "semver", "v4.3.0", "", ""},
		{10, "github-releases", "open-component-model/open-component-model", "https://github.com/open-component-model/open-component-model", "semver", "0.16.0", "", ""},
		{12, "custom.envtest", "envtest", "", "semver", "1.36.0", "", ""},
		{14, "go-toolchain", "go", "", "semver", "1.27.1", "", ""},
		{16, "node-version", "node", "", "semver", "26.5.0", "", ""},
		{18, "npm", "npm", "", "semver", "12.2.0", "", ""},
		{20, "go", "k8s.io/code-generator", "", "semver", "v0.37.1", "", ""},
		{22, "git-tags", "o/r", "https://github.com/o/r", "tag", "v1.2", "", ""},
		{24, "docker", "nginx", "", "docker", "1.27.3-alpine", "", ""},
		{26, "docker", "alpine", "", "docker", "3.20", "", ""},
		{28, "", "x", "", "", "", "", "renovate marker without datasource"},
		{30, "go", "", "", "", "", "", "renovate marker without depName"},
		{32, "go", "a", "", "loose", "1.0.0", "", ""},
		{34, "github-releases", "o/r", "https://github.com/o/r", "semver", "", "", "no version on next line"},
		{35, "docker", "busybox", "", "docker", "1.36", "", ""},
		{37, "custom.missing", "x", "", "semver", "", "", `unknown datasource "custom.missing"`},
		{39, "node-version", "node", "", "semver", "v26.5.0", "", ""},
		{41, "pypi", "Django", "", "semver", "4.2.11", "", ""},
	})
}

func TestDockerRefBoundaries(t *testing.T) {
	for _, c := range []struct{ line, tag string }{
		{"X=a/b:1.2 y", "1.2"},
		{"X=a/b:1.2@sha256:short c:3", "3"},
		{"key: value", ""},
		{"localhost:5000/img:v1", "v1"},
	} {
		loc := firstDockerRef([]byte(c.line))
		got := ""
		if loc != nil {
			got = c.line[loc[2]:loc[3]]
		}
		if got != c.tag {
			t.Errorf("firstDockerRef(%q) tag = %q, want %q", c.line, got, c.tag)
		}
	}
}

// TestRegexRenovateGroups covers Renovate's named groups (OCM renovate.json5 shapes).
func TestRegexRenovateGroups(t *testing.T) {
	const digest = "sha256:eade932f793742de0aa8755406c7677cd7696f8675b6180926f7eeffa7abe6b9"
	src := "# renovate: datasource=docker depName=goodwithtech/dockle\nDOCKLE_IMAGE=goodwithtech/dockle:v0.4.15@" + digest + "\n" +
		"# renovate: datasource=golang-version depName=go\nGO_VERSION = \"1.27.1\"\n" +
		"# renovate: datasource=github-tags depName=x packageName=o/r versioning=tag\nREF = \"1.2.3\"\n"
	m := one(t, config.Custom{Files: []string{"x"}, Match: `# renovate: datasource=docker depName=(?P<depName>\S+)\s+\w+_IMAGE=\S+?:(?P<currentValue>[^@\s]+)@(?P<currentDigest>sha256:[a-f0-9]{64})`, Datasource: "docker", Versioning: "docker"})
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{2, "docker", "goodwithtech/dockle", "", "docker", "v0.4.15", digest, ""},
	})
	m = one(t, config.Custom{Files: []string{"x"}, Match: `# renovate: datasource=(?P<datasource>\S+) depName=(?P<depName>\S+)(?: packageName=(?P<packageName>\S+))?(?: versioning=(?P<versioning>\S+))?\s+\w+ = "(?P<currentValue>[^"]+)"`})
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{4, "go-toolchain", "go", "", "semver", "1.27.1", "", ""},
		{6, "git-tags", "o/r", "https://github.com/o/r", "tag", "1.2.3", "", ""},
	})
	m = one(t, config.Custom{Files: []string{"x"}, Match: `(?P<versioning>bogus) (?P<currentValue>1\.0)`, Datasource: "go", Name: "n"})
	check(t, "bogus 1.0", extract(t, m, "bogus 1.0"), DepTypeRegex, []want{
		{1, "go", "n", "", "bogus", "1.0", "", `unknown versioning "bogus"`},
	})
}
