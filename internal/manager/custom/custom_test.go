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
		"# deps: cpan Foo",                              // 14
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
				{15, "cpan", "Foo", "", "semver", "", "", `unknown datasource "cpan"`},
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

func TestMatchRenovatePatterns(t *testing.T) {
	m := one(t, config.Custom{Files: []string{`/(^|/)\.env$/`, "!vendor/**"}})
	for path, ok := range map[string]bool{
		".env": true, "a/.env": true, "a/x.env": false, "vendor/.env": false,
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
		{config.Custom{Files: []string{"x"}, Versioning: "nope"}, `versioning: must be one of tag, go, docker, semver, loose, cargo, pep440, hashicorp, maven, ruby, conda, composer, julia, bazel-module, nuget, rpm, apk, deb, pub or regex:<RE2>, got "nope"`},
		{config.Custom{Files: []string{"x"}, Versioning: "regex:(?<minor>1)"}, `versioning: regex: must have a (?<major>...) group`},
		{config.Custom{Files: []string{"x"}, Datasource: "cpan"}, `datasource: unknown "cpan"`},
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

// rpm deps take their repository from registryUrl (marker field, regex group, else the
// entry's) and default to rpm versioning unless the marker, group or entry sets one.
func TestRPM(t *testing.T) {
	const repo = "https://packages.microsoft.com/azurelinux/3.0/prod/base/x86_64/repodata/"
	m := one(t, config.Custom{Files: []string{"f"}, RegistryURL: repo})
	src := strings.Join([]string{
		"# renovate: datasource=rpm depName=golang", // 1
		"GO=1.25.3-1.azl3",                          // 2
		"# renovate: datasource=rpm depName=kernel registryUrl=https://x/repodata versioning=loose", // 3
		"KERNEL=6.6.104.2-4.azl3",                              // 4
		"# deps: rpm moby-engine",                              // 5
		"MOBY=25.0.3-15.azl3",                                  // 6
		"# renovate: datasource=go depName=golang.org/x/tools", // 7
		"v0.38.0", // 8
	}, "\n")
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "rpm", "golang", repo, "rpm", "1.25.3-1.azl3", "", ""},
		{4, "rpm", "kernel", "https://x/repodata", "loose", "6.6.104.2-4.azl3", "", ""},
		{6, "rpm", "moby-engine", repo, "rpm", "25.0.3-15.azl3", "", ""},
		{8, "go", "golang.org/x/tools", "", "semver", "v0.38.0", "", ""},
	})

	// Regex mode: datasource rpm makes rpm the default versioning; the entry's versioning wins.
	m = one(t, config.Custom{Files: []string{"f"}, Datasource: "rpm", RegistryURL: repo,
		Match: `(?m)^(?P<name>[a-z-]+)(?: (?P<registryUrl>\S+))?=(?P<version>\S+)$`})
	src = "golang=1.25.3-1.azl3\nkernel https://y/repodata/=6.6.104.2-4.azl3\n"
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{1, "rpm", "golang", repo, "rpm", "1.25.3-1.azl3", "", ""},
		{2, "rpm", "kernel", "https://y/repodata/", "rpm", "6.6.104.2-4.azl3", "", ""},
	})
	m = one(t, config.Custom{Files: []string{"f"}, Datasource: "rpm", RegistryURL: repo, Versioning: "loose",
		Match: `(?m)^(?P<name>[a-z-]+)=(?P<version>\S+)$`})
	src = "golang=1.25.3-1.azl3\n"
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{1, "rpm", "golang", repo, "loose", "1.25.3-1.azl3", "", ""},
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
		"# renovate: datasource=pypi depName=Django",                            // 40
		"DJANGO_VERSION=4.2.11",                                                 // 41
		"# renovate: datasource=endoflife-date depName=ubuntu versioning=loose", // 42
		"UBUNTU=24.04", // 43
		"# renovate: datasource=endoflife-date depName=nodejs registryUrl=https://eol.example.com/api", // 44
		"NODE=24.11.0", // 45
		"# renovate: datasource=python-version depName=python", // 46
		"ARG PYTHON_VERSION=3.13.1",                            // 47
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
		{43, "endoflife-date", "ubuntu", "", "loose", "24.04", "", ""},
		{45, "endoflife-date", "nodejs", "https://eol.example.com/api", "semver", "24.11.0", "", ""},
		{47, "python-version", "python", "", "pep440", "3.13.1", "", ""},
	})
}

func TestExtractVersion(t *testing.T) {
	src := strings.Join([]string{
		"# renovate: datasource=github-releases depName=jqlang/jq extractVersion=^jq-(?<version>.+)$",
		"JQ=1.7.1",
		"# renovate: datasource=github-releases depName=o/bad extractVersion=^x-(.+)$",
		"B=1.0.0",
	}, "\n")
	m := oneWith(t, &config.Config{Custom: []config.Custom{{Files: []string{"**/.env"}}}})
	got := extract(t, m, src)
	want := []struct{ extract, skip string }{
		{"^jq-(?<version>.+)$", ""},
		{"^x-(.+)$", "invalid extractVersion: needs a (?<version>…) group"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].ExtractVersion != w.extract || got[i].Skip != w.skip {
			t.Errorf("dep %d: extractVersion %q, skip %q; want %q, %q", i, got[i].ExtractVersion, got[i].Skip, w.extract, w.skip)
		}
	}
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

func BenchmarkAnnotations(b *testing.B) {
	var sb strings.Builder
	for i := range 5000 {
		if i%500 == 0 {
			sb.WriteString("# deps: docker nginx\nimage: nginx:1.27.3\n")
			continue
		}
		sb.WriteString("    some_key: some fairly ordinary value # with a comment\n")
	}
	src := []byte(sb.String())
	ms, err := New(&config.Config{Custom: []config.Custom{{Files: []string{"**/x.yaml"}}}})
	if err != nil {
		b.Fatal(err)
	}
	m := ms[0].(*customManager)
	b.ReportAllocs()
	for b.Loop() {
		m.annotations("x.yaml", src)
	}
}

// Renovate's currentDigest of a github-release-attachments dep (OCM's cosign .env pin) is the
// sha256 of a release asset: it becomes the dep's attachment checksum, not its digest.
func TestRegexAttachmentDigest(t *testing.T) {
	sum := strings.Repeat("3a", 32)
	m := one(t, config.Custom{Files: []string{"x"}, Match: `datasource=(?<datasource>\S+) depName=(?<depName>\S+)\s+V=(?<currentValue>\S+)\s+SUM=(?<currentDigest>[0-9a-f]{64})`})
	src := "# renovate: datasource=github-release-attachments depName=o/cosign\nV=v3.1.4+ocm.6\nSUM=" + sum + "\n"
	ds := extract(t, m, src)
	if len(ds) != 1 {
		t.Fatalf("deps = %+v", ds)
	}
	d := ds[0]
	if d.Skip != "" || d.Digest != "" || d.Checksum != sum || d.ChecksumSource == nil || !d.ChecksumSource.Attachment || src[d.ChecksumSpan.Start:d.ChecksumSpan.End] != sum {
		t.Errorf("dep = %+v", d)
	}
	edits, err := m.Edits(dep.Update{Dep: d, NewVersion: "v3.2.0+ocm.1", NewChecksum: strings.Repeat("b", 64)})
	if err != nil || len(edits) != 2 {
		t.Errorf("edits = %+v, %v", edits, err)
	}
}

// registryUrl (entry field, regex group, Renovate marker) is the Source of helm and
// gitlab-packages deps and ignored for other datasources.
func TestRegistryURL(t *testing.T) {
	src := strings.Join([]string{
		"# renovate: datasource=gitlab-packages packageName=gitlab-org/cli:glab registryUrl=https://gitlab.example.com", // 1
		"GLAB=1.44.1", // 2
		"# renovate: datasource=gitlab-packages packageName=g/p:x", // 3
		"X=1.0.0", // 4
		"# renovate: datasource=npm depName=left-pad registryUrl=https://npm.example.com", // 5
		"PAD=1.3.0",                     // 6
		"# deps: gitlab-packages g/p:y", // 7
		"Y=2.0.0",                       // 8
	}, "\n")
	m := one(t, config.Custom{Files: []string{"f"}, RegistryURL: "https://entry.example.com"})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "gitlab-packages", "gitlab-org/cli:glab", "https://gitlab.example.com", "semver", "1.44.1", "", ""},
		{4, "gitlab-packages", "g/p:x", "https://entry.example.com", "semver", "1.0.0", "", ""},
		{6, "npm", "left-pad", "", "semver", "1.3.0", "", ""},
		{8, "gitlab-packages", "g/p:y", "https://entry.example.com", "semver", "2.0.0", "", ""},
	})

	re := one(t, config.Custom{Files: []string{"f"}, Match: `(?m)^(?P<datasource>[a-z-]+) (?P<name>\S+) (?P<version>\S+)(?: (?P<registryUrl>\S+))?$`, RegistryURL: "https://entry.example.com"})
	src = "gitlab-packages g/p:x 1.0.0 https://group.example.com\ngitlab-packages g/p:y 2.0.0\nnpm left-pad 1.3.0 https://npm.example.com"
	check(t, src, extract(t, re, src), DepTypeRegex, []want{
		{1, "gitlab-packages", "g/p:x", "https://group.example.com", "semver", "1.0.0", "", ""},
		{2, "gitlab-packages", "g/p:y", "https://entry.example.com", "semver", "2.0.0", "", ""},
		{3, "npm", "left-pad", "", "semver", "1.3.0", "", ""},
	})
}

// registryUrl (entry field, regex group, Renovate marker) is the Source of apk deps, which
// default to apk versioning in markers; other datasources ignore it.
func TestApkRegistryURL(t *testing.T) {
	const alpine = "https://dl-cdn.alpinelinux.org/alpine?branch=v3.22&components=main,community&arch=x86_64"
	src := strings.Join([]string{
		"# renovate: datasource=apk depName=bash registryUrl=" + alpine,                   // 1
		"RUN apk add --no-cache bash=5.2.37-r0",                                           // 2
		"# renovate: datasource=apk depName=curl",                                         // 3
		"RUN apk add curl=8.14.1-r4",                                                      // 4
		"# renovate: datasource=npm depName=left-pad registryUrl=https://npm.example.com", // 5
		"PAD=1.3.0",           // 6
		"# deps: apk openssl", // 7
		"OPENSSL=3.5.9-r0",    // 8
	}, "\n")
	m := one(t, config.Custom{Files: []string{"f"}, RegistryURL: "https://packages.wolfi.dev/os?arch=x86_64"})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "apk", "bash", alpine, "apk", "5.2.37-r0", "", ""},
		{4, "apk", "curl", "https://packages.wolfi.dev/os?arch=x86_64", "apk", "8.14.1-r4", "", ""},
		{6, "npm", "left-pad", "", "semver", "1.3.0", "", ""},
		{8, "apk", "openssl", "https://packages.wolfi.dev/os?arch=x86_64", "apk", "3.5.9-r0", "", ""},
	})

	re := one(t, config.Custom{Files: []string{"f"}, Match: `(?m)^(?P<datasource>[a-z-]+) (?P<name>\S+) (?P<version>\S+)(?: (?P<registryUrl>\S+))?$`, Versioning: "apk", RegistryURL: alpine})
	src = "apk jq 1.8.0-r0 https://packages.wolfi.dev/os?arch=aarch64\napk bash 5.2.37-r0\nnpm left-pad 1.3.0 https://npm.example.com"
	check(t, src, extract(t, re, src), DepTypeRegex, []want{
		{1, "apk", "jq", "https://packages.wolfi.dev/os?arch=aarch64", "apk", "1.8.0-r0", "", ""},
		{2, "apk", "bash", alpine, "apk", "5.2.37-r0", "", ""},
		{3, "npm", "left-pad", "", "apk", "1.3.0", "", ""},
	})
}

// An entry's versioning wins over the apk default; without one apk regex deps use apk.
func TestApkDefaultVersioning(t *testing.T) {
	src := "apk bash 5.2.37-r0\nnpm left-pad 1.3.0"
	match := `(?m)^(?P<datasource>[a-z-]+) (?P<name>\S+) (?P<version>\S+)$`
	check(t, src, extract(t, one(t, config.Custom{Files: []string{"f"}, Match: match}), src), DepTypeRegex, []want{
		{1, "apk", "bash", "", "apk", "5.2.37-r0", "", ""},
		{2, "npm", "left-pad", "", "semver", "1.3.0", "", ""},
	})
	check(t, src, extract(t, one(t, config.Custom{Files: []string{"f"}, Match: match, Versioning: "loose"}), src), DepTypeRegex, []want{
		{1, "apk", "bash", "", "loose", "5.2.37-r0", "", ""},
		{2, "npm", "left-pad", "", "loose", "1.3.0", "", ""},
	})
}

// TestDeb covers the deb datasource in every mode: Renovate's Dockerfile marker example, a
// `deps:` annotation with a registry, the entry's registryUrl and a regex registryUrl group.
func TestDeb(t *testing.T) {
	const trixie = "https://deb.debian.org/debian?suite=trixie&components=main&binaryArch=amd64"
	const sec = "https://security.debian.org/debian-security?suite=bookworm-security&components=main&binaryArch=amd64"
	src := strings.Join([]string{
		"# renovate: suite=bullseye depName=gcc-11 datasource=deb",          // 1 no registryUrl: the default
		`ENV GCC_VERSION="11.2.0-19"`,                                       // 2
		"# renovate: datasource=deb depName=curl registryUrl=" + trixie,     // 3
		"RUN apt-get install -y curl=8.14.1-2 && rm -rf /var/lib/apt/lists", // 4
		"# deps: deb " + sec + " openssl",                                   // 5
		"ARG OPENSSL=3.0.15-1~deb12u1",                                      // 6
		"# deps: deb git",                                                   // 7
		"ARG GIT_1=1:2.39.5-0+deb12u2.",                                     // 8 epoch, trailing dot
		"# deps: deb tzdata versioning=loose",                               // 9
		"TZ 2024a-0+deb12u1",                                                // 10
		"# renovate: datasource=deb depName=x registryUrl=https://deb.debian.org/debian?suite=stable", // 11
		"X=1.0-1",                                // 12
		"# renovate: datasource=deb depName=a/b", // 13
		"X=1.0-1",                                // 14
		"# deps: deb nover",                      // 15
		"just text and v1",                       // 16
		"# deps: deb dockerce",                   // 17
		"pkg=5:23.0.0-1~debian.12~bookworm",      // 18
	}, "\n")
	m := one(t, config.Custom{Files: []string{"x"}})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "deb", "gcc-11", "", "deb", "11.2.0-19", "", ""},
		{4, "deb", "curl", trixie, "deb", "8.14.1-2", "", ""},
		{6, "deb", "openssl", sec, "deb", "3.0.15-1~deb12u1", "", ""},
		{8, "deb", "git", "", "deb", "1:2.39.5-0+deb12u2", "", ""},
		{10, "deb", "tzdata", "", "loose", "2024a-0+deb12u1", "", ""},
		{12, "deb", "x", "https://deb.debian.org/debian?suite=stable", "deb", "", "", `invalid deb registry URL "https://deb.debian.org/debian?suite=stable": missing query parameter components`},
		{14, "deb", "a/b", "", "deb", "", "", `invalid deb name "a/b"`},
		{16, "deb", "nover", "", "deb", "", "", "no version on next line"},
		{18, "deb", "dockerce", "", "deb", "5:23.0.0-1~debian.12~bookworm", "", ""},
	})

	// The entry's registryUrl and versioning apply unless the marker sets them.
	m = one(t, config.Custom{Files: []string{"x"}, RegistryURL: trixie, Versioning: "loose"})
	src = "# deps: deb curl\nX=8.14.1-2\n# renovate: datasource=deb depName=git registryUrl=" + sec + "\nX=1.0-1\n"
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "deb", "curl", trixie, "loose", "8.14.1-2", "", ""},
		{4, "deb", "git", sec, "loose", "1.0-1", "", ""},
	})

	// Regex mode: a registryUrl group wins over the entry's registryUrl.
	m = one(t, config.Custom{Files: []string{"x"}, Datasource: "deb", RegistryURL: trixie,
		Match: `# renovate:(?: suite=(?P<suite>\S+))?(?: registryUrl=(?P<registryUrl>\S+))? depName=(?P<depName>\S+)\nENV \w+="(?P<currentValue>[^"]+)"`})
	src = "# renovate: suite=bullseye depName=gcc-11\nENV GCC_VERSION=\"11.2.0-19\"\n" +
		"# renovate: registryUrl=" + sec + " depName=openssl\nENV OPENSSL_VERSION=\"3.0.15-1~deb12u1\"\n"
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{2, "deb", "gcc-11", trixie, "deb", "11.2.0-19", "", ""},
		{4, "deb", "openssl", sec, "deb", "3.0.15-1~deb12u1", "", ""},
	})
	// An entry's own versioning wins over deb's default; a regex datasource group gets it.
	m = one(t, config.Custom{Files: []string{"x"}, Versioning: "semver"})
	src = "# renovate: datasource=deb depName=git\nARG GIT=1:2.47.3-0+deb13u1\n"
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{{2, "deb", "git", "", "semver", "1:2.47.3-0+deb13u1", "", ""}})
	m = one(t, config.Custom{Files: []string{"x"}, Name: "git", Match: `(?P<datasource>deb) (?P<version>\S+)`})
	check(t, "deb 1:2.47.3-0+deb13u1", extract(t, m, "deb 1:2.47.3-0+deb13u1"), DepTypeRegex, []want{{1, "deb", "git", "", "deb", "1:2.47.3-0+deb13u1", "", ""}})
	// Datasources without a registry keep an empty source.
	m = one(t, config.Custom{Files: []string{"x"}, Datasource: "npm", Name: "n", RegistryURL: trixie, Match: `(?P<version>\d+\.\d+)`})
	check(t, "1.2", extract(t, m, "1.2"), DepTypeRegex, []want{{1, "npm", "n", "", "semver", "1.2", "", ""}})
}

func TestFirstDebVersion(t *testing.T) {
	for line, want := range map[string]string{
		"curl=8.14.1-2 git=1:2.39.5":    "8.14.1-2",
		"X_1 Y=1:2.39.5-0+deb12u2:":     "1:2.39.5-0+deb12u2",
		"python3.11 v=3.11.2-6+deb12u4": "3.11.2-6+deb12u4",
		"ca-certificates=20250419 \\":   "20250419",
		"abc1.0 1.0.":                   "1.0",
		"x=1.0~rc1 ":                    "1.0~rc1",
		"bad 1.0-a_b":                   "",
	} {
		s, e, ok := firstDebVersion([]byte(line))
		if got := line[s:e]; ok != (want != "") || got != want {
			t.Errorf("firstDebVersion(%q) = %q, %v, want %q", line, got, ok, want)
		}
	}
}

func TestArtifactoryAnnotations(t *testing.T) {
	src := strings.Join([]string{
		"# renovate: datasource=artifactory depName=tools/cli/ registryUrl=https://example.jfrog.io/artifactory/generic-local/", // 1
		"CLI_VERSION=1.4.2", // 2
		"# deps: artifactory https://example.jfrog.io/artifactory/generic-local/tools/cli/", // 3
		"cli 1.4.2",                     // 4
		"# deps: artifactory tools/cli", // 5
		"1.0.0",                         // 6
		"# renovate: datasource=artifactory depName=/ registryUrl=https://x", // 7
		"1.0.0", // 8
		"# renovate: datasource=go depName=example.com/m registryUrl=https://x", // 9
		"1.0.0", // 10
	}, "\n")
	m := one(t, config.Custom{Files: []string{"f"}})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "artifactory", "tools/cli", "https://example.jfrog.io/artifactory/generic-local", "semver", "1.4.2", "", ""},
		{4, "artifactory", "https://example.jfrog.io/artifactory/generic-local/tools/cli", "", "semver", "1.4.2", "", ""},
		{6, "artifactory", "tools/cli", "", "semver", "", "", `invalid artifactory name "tools/cli"`},
		{8, "artifactory", "", "https://x", "semver", "", "", `invalid artifactory name "https://x "`},
		{10, "go", "example.com/m", "", "semver", "1.0.0", "", ""},
	})
}

// The artifactory registry URL comes from a regex registryUrl group, else the entry's
// registryUrl (several URLs, trailing slashes trimmed).
func TestArtifactoryRegexRegistryURL(t *testing.T) {
	// Renovate's artifactory readme example: an OpenAPI definition fetched from a generic repository.
	src := "const a = 'https://example.jfrog.io/artifactory/api-local/path/to/api/api-definition_v1.2.3.yaml';\n" +
		"const b = 'api/other_v2.0.0.yaml';\n"
	m := one(t, config.Custom{
		Files:       []string{"f"},
		Match:       `'(?P<registryUrl>https://[^'/]+/artifactory/[^'/]+/)?(?P<packageName>[^']+/)[a-z-]+_(?P<currentValue>v?[0-9.]+)\.yaml'`,
		Datasource:  "artifactory",
		RegistryURL: "https://a.example/artifactory/r1 https://b.example/artifactory/r2/",
	})
	check(t, src, extract(t, m, src), DepTypeRegex, []want{
		{1, "artifactory", "path/to/api", "https://example.jfrog.io/artifactory/api-local", "semver", "v1.2.3", "", ""},
		{2, "artifactory", "api", "https://a.example/artifactory/r1 https://b.example/artifactory/r2", "semver", "v2.0.0", "", ""},
	})
}

// registryUrl (entry field, regex group, Renovate marker) is the Source of the forge
// datasources and ignored for other datasources; invalid project paths are skipped.
func TestRegistryURLForge(t *testing.T) {
	src := strings.Join([]string{
		"# renovate: datasource=gitlab-releases depName=gitlab-org/cli registryUrl=https://gitlab.example.com", // 1
		"GLAB=v1.44.1", // 2
		"# renovate: datasource=forgejo-tags packageName=forgejo/runner", // 3
		"RUNNER=v13.2.0", // 4
		"# renovate: datasource=npm depName=left-pad registryUrl=https://npm.example.com", // 5
		"PAD=1.3.0",                  // 6
		"# deps: gitea-releases o/r", // 7
		"R=2.0.0",                    // 8
		"# deps: gitea-tags a/b/c",   // 9
		"C=1.0.0",                    // 10
		"# renovate: datasource=gitlab-tags depName=g/p registryUrl=oci://x", // 11
		"P=1.0.0", // 12
	}, "\n")
	m := one(t, config.Custom{Files: []string{"f"}, RegistryURL: "https://entry.example.com"})
	check(t, src, extract(t, m, src), DepTypeAnnotation, []want{
		{2, "gitlab-releases", "gitlab-org/cli", "https://gitlab.example.com", "semver", "v1.44.1", "", ""},
		{4, "forgejo-tags", "forgejo/runner", "https://entry.example.com", "semver", "v13.2.0", "", ""},
		{6, "npm", "left-pad", "", "semver", "1.3.0", "", ""},
		{8, "gitea-releases", "o/r", "https://entry.example.com", "semver", "2.0.0", "", ""},
		{10, "gitea-tags", "a/b/c", "https://entry.example.com", "semver", "", "", `invalid gitea-tags name "a/b/c"`},
		{12, "gitlab-tags", "g/p", "oci://x", "semver", "", "", `invalid gitlab-tags registry URL "oci://x"`},
	})

	re := one(t, config.Custom{Files: []string{"f"}, Match: `(?m)^(?P<datasource>[a-z-]+) (?P<name>\S+) (?P<version>\S+)(?: (?P<registryUrl>\S+))?$`})
	src = "gitlab-tags g/s/p 1.0.0 https://gitlab.example.com\nforgejo-releases https://codeberg.org/o/r 2.0.0\nnpm left-pad 1.3.0 https://npm.example.com"
	check(t, src, extract(t, re, src), DepTypeRegex, []want{
		{1, "gitlab-tags", "g/s/p", "https://gitlab.example.com", "semver", "1.0.0", "", ""},
		{2, "forgejo-releases", "https://codeberg.org/o/r", "", "semver", "2.0.0", "", ""},
		{3, "npm", "left-pad", "", "semver", "1.3.0", "", ""},
	})
}
