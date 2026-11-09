package devcontainer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		".devcontainer.json":                          true,
		".devcontainer/devcontainer.json":             true,
		".devcontainer/python/devcontainer.json":      true,
		"src/node/.devcontainer/devcontainer.json":    true,
		"src/node/.devcontainer.json":                 true,
		"devcontainer.json":                           false,
		".devcontainer/a/b/devcontainer.json":         false,
		"x/devcontainer.json":                         false,
		".devcontainer/devcontainer.json.bak":         false,
		".devcontainer/devcontainer-feature.json":     false,
		"devcontainer/devcontainer.json":              false,
		".devcontainer/python/my.devcontainer.json":   false,
		"a.devcontainer.json":                         false,
		".devcontainer/devcontainer.jsonc":            false,
		".github/.devcontainer/x/y/devcontainer.json": false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestBuildFilesByDockerManager checks that the files `build.dockerfile` and
// `dockerComposeFile` name in the templates are the docker manager's, not this manager's.
func TestBuildFilesByDockerManager(t *testing.T) {
	for _, p := range []string{".devcontainer/Dockerfile", ".devcontainer/base.Dockerfile", ".devcontainer/Dockerfile.dev", ".devcontainer/docker-compose.yml", ".devcontainer/compose.yaml"} {
		if !docker.New().Match(p) || New().Match(p) {
			t.Errorf("%s: docker manager match %v, devcontainer %v", p, docker.New().Match(p), New().Match(p))
		}
	}
}

// show renders a dep as "line type name version [ref|skip]", ref being the bytes at RefSpan
// and, for pinned refs, "@" and the bytes at DigestSpan.
func show(src []byte, d dep.Dep) string {
	if d.Skip != "" {
		return fmt.Sprintf("%d %s %s skip=%s", d.Line, d.DepType, d.Name, d.Skip)
	}
	s := fmt.Sprintf("%d %s %s %s %s %s ref=%s", d.Line, d.DepType, d.Name, d.Datasource, d.Versioning, d.Version, src[d.RefSpan.Start:d.RefSpan.End])
	if d.Digest != "" {
		s += "@" + string(src[d.DigestSpan.Start:d.DigestSpan.End])
	}
	return s
}

const fixture = "\ufeff" + `// comment with "image": "ignored:1"
{
	/* "image": "ignored:2" */
	"name": "demo", // "features": {}
	"image": "mcr.microsoft.com/devcontainers/base:1.2.3-bookworm@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	"build": {"dockerfile": "Dockerfile", "args": {"image": "not:me"}},
	"features": {
		"ghcr.io/devcontainers/features/node:1": {"version": "20.11.1", "nodeGypDependencies": false},
		"ghcr.io/devcontainers/features/go:1.3": "1.22",
		"ghcr.io/devcontainers/features/ruby:1": {},
		"ghcr.io/devcontainers/features/python:1": {"version": "3.12"},
		"ghcr.io/devcontainers/features/node:2": {"version": "lts"},
		"ghcr.io/devcontainers/features/go": {"version": "1"},
		"ghcr.io/devcontainers/features/docker-in-docker:2.12.0": {
			"version": "latest", // the feature's own option, not a tool
		},
		"ghcr.io/devcontainers/features/sshd:1@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd": {},
		"ghcr.io/devcontainers/features/rust:": {},
		"ghcr.io/x/f@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd": {},
		"./local-feature": {},
		"../shared/feature": {},
		"https://example.com/feature.tgz": {},
		"docker-in-docker": "latest",
		"owner/repo/feature@v1": {},
		"localhost:5000/features/x:1.0.0": true,
		"ghcr.io/${localEnv:ORG}/f:1": {},
		"ghcr.io/devcontainers/features/ruby:1.1": {"version": "\u0033.3"},
		"ghcr.io/devcontainers/features/node:1.1": {"version": 20},
		"ghcr.io/devcontainers/features/java:1": {"version": "21", "jdkDistro": "tem"},
		"ghcr.io/devcontainers/features/java:1.1": {"jdkDistro": "ms", "version": "17.0.9"},
		"ghcr.io/devcontainers/features/java:1.2": "21.0.5-tem",
	},
}
`

func TestExtract(t *testing.T) {
	for name, src := range map[string]string{"lf": fixture, "crlf": strings.ReplaceAll(fixture, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			deps, err := New().Extract(".devcontainer/devcontainer.json", []byte(src), manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{
				"5 image mcr.microsoft.com/devcontainers/base docker docker 1.2.3-bookworm ref=1.2.3-bookworm@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				"8 feature ghcr.io/devcontainers/features/node docker docker 1 ref=1",
				"8 feature-version node node-version semver 20.11.1 ref=20.11.1",
				"9 feature ghcr.io/devcontainers/features/go docker docker 1.3 ref=1.3",
				"9 feature-version go go-toolchain tag 1.22 ref=1.22",
				"10 feature ghcr.io/devcontainers/features/ruby docker docker 1 ref=1",
				"10 feature-version ruby skip=no version",
				"11 feature ghcr.io/devcontainers/features/python docker docker 1 ref=1",
				"11 feature-version python python-version tag 3.12 ref=3.12",
				"12 feature ghcr.io/devcontainers/features/node docker docker 2 ref=2",
				"12 feature-version node skip=non-version value",
				"13 feature ghcr.io/devcontainers/features/go skip=no tag",
				"13 feature-version go skip=partial version",
				"14 feature ghcr.io/devcontainers/features/docker-in-docker docker docker 2.12.0 ref=2.12.0",
				"17 feature ghcr.io/devcontainers/features/sshd docker docker 1 ref=1@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
				"18 feature ghcr.io/devcontainers/features/rust: skip=invalid reference",
				"19 feature ghcr.io/x/f docker docker latest ref=@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
				"20 feature ./local-feature skip=local feature",
				"21 feature ../shared/feature skip=local feature",
				"22 feature https://example.com/feature.tgz skip=tarball feature",
				"23 feature docker-in-docker skip=not an OCI reference",
				"24 feature owner/repo/feature@v1 skip=not an OCI reference",
				"25 feature localhost:5000/features/x docker docker 1.0.0 ref=1.0.0",
				"26 feature ghcr.io/${localEnv:ORG}/f:1 skip=variable reference",
				"27 feature ghcr.io/devcontainers/features/ruby docker docker 1.1 ref=1.1",
				"27 feature-version ruby skip=cannot locate value",
				"28 feature ghcr.io/devcontainers/features/node docker docker 1.1 ref=1.1",
				"28 feature-version node skip=no version",
				"29 feature ghcr.io/devcontainers/features/java docker docker 1 ref=1",
				"29 feature-version java-jdk java-version tag 21 ref=21",
				"30 feature ghcr.io/devcontainers/features/java docker docker 1.1 ref=1.1",
				"30 feature-version java-jdk skip=unsupported distribution",
				"31 feature ghcr.io/devcontainers/features/java docker docker 1.2 ref=1.2",
				"31 feature-version java-jdk skip=unsupported range",
			}
			if len(deps) != len(want) {
				for _, d := range deps {
					t.Log(show([]byte(src), d))
				}
				t.Fatalf("got %d deps, want %d", len(deps), len(want))
			}
			for i, d := range deps {
				if got := show([]byte(src), d); got != want[i] {
					t.Errorf("dep %d = %q, want %q", i, got, want[i])
				}
				if d.Manager != Name || d.File != ".devcontainer/devcontainer.json" {
					t.Errorf("dep %d: manager %q file %q", i, d.Manager, d.File)
				}
			}
		})
	}
}

func TestExtractNoDeps(t *testing.T) {
	for _, src := range []string{`{}`, `{"image": 1, "features": []}`, `{"name": "x", "image": null, "features": null}`} {
		deps, err := New().Extract(".devcontainer.json", []byte(src), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("Extract(%s) = %v, %v", src, deps, err)
		}
	}
}

func TestExtractErrors(t *testing.T) {
	for _, src := range []string{``, `[]`, `{"image": "a:1"`, `{"features": {"ghcr.io/devcontainers/features/node:1": {"version": "1"`, `{"features": [`, `{"features": {"a/b:1": {`, `{"features": {"ghcr.io/devcontainers/features/node:1": [}}`, `{"image": "a:1"} x`} {
		if _, err := New().Extract(".devcontainer.json", []byte(src), manager.Env{}); err == nil {
			t.Errorf("Extract(%q): no error", src)
		}
	}
}

// TestRealWorld extracts devcontainer.json files of public repositories: devcontainers/cli's
// example (go and node version options), microsoft/vscode-remote-try-node (comments, image
// only), a devcontainers/templates template (${templateOption:…} variables) and
// microsoft/vscode (features with an empty tag).
func TestRealWorld(t *testing.T) {
	for file, want := range map[string][]string{
		"devcontainers-cli-example.devcontainer.json": {
			"30 feature ghcr.io/devcontainers/features/go docker docker 1 ref=1",
			"31 feature-version go go-toolchain tag 1.18.4 ref=1.18.4",
			"33 feature ghcr.io/devcontainers/features/node docker docker 1 ref=1",
			"34 feature-version node node-version semver 16.15.1 ref=16.15.1",
			"37 feature ghcr.io/devcontainers/features/desktop-lite docker docker 1 ref=1",
			"38 feature ghcr.io/devcontainers/features/docker-in-docker docker docker 2 ref=2",
			"40 feature ghcr.io/devcontainers/features/sshd docker docker 1 ref=1",
		},
		"vscode-remote-try-node.devcontainer.json": {
			"7 image mcr.microsoft.com/devcontainers/javascript-node docker docker 5-26-trixie ref=5-26-trixie",
		},
		"templates-java.devcontainer.json": {
			"6 image mcr.microsoft.com/devcontainers/java:3-${templateOption:imageVariant} skip=variable reference",
			"9 feature ghcr.io/devcontainers/features/java docker docker 1 ref=1",
			"10 feature-version java-jdk skip=non-version value",
		},
		"vscode.devcontainer.json": {
			"7 feature ghcr.io/devcontainers/features/desktop-lite: skip=invalid reference",
			"8 feature ghcr.io/devcontainers/features/rust: skip=invalid reference",
			"9 feature ghcr.io/devcontainers/features/sshd docker docker 1 ref=1",
		},
	} {
		src, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		deps, err := New().Extract(".devcontainer/devcontainer.json", src, manager.Env{})
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var got []string
		for _, d := range deps {
			got = append(got, show(src, d))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s:\n got %s\nwant %s", file, strings.Join(got, "\n     "), strings.Join(want, "\n     "))
		}
	}
}

// update returns src with the edits of u applied.
func update(t *testing.T, src []byte, u dep.Update) string {
	t.Helper()
	edits, err := New().Edits(u)
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestEdits(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "devcontainers-cli-example.devcontainer.json"))
	if err != nil {
		t.Fatal(err)
	}
	deps, err := New().Extract(".devcontainer/devcontainer.json", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	// Feature tag, version option and digest pin of a feature with a written digest keep the
	// comments and formatting.
	got := update(t, src, dep.Update{Dep: deps[0], NewVersion: "2"})
	if want := strings.Replace(string(src), `"ghcr.io/devcontainers/features/go:1"`, `"ghcr.io/devcontainers/features/go:2"`, 1); got != want {
		t.Errorf("feature update:\n%s", got)
	}
	got = update(t, src, dep.Update{Dep: deps[3], NewVersion: "24.11.1"})
	if want := strings.Replace(string(src), `"version": "16.15.1"`, `"version": "24.11.1"`, 1); got != want {
		t.Errorf("version update:\n%s", got)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[3], NewVersion: "24.11.1", NewDigest: "sha256:x"}); err == nil {
		t.Error("digest for a feature version: no error")
	}

	img := []byte(`{
	// base
	"image": "mcr.microsoft.com/devcontainers/go:1-1.22-bookworm", // trailing,
}`)
	deps, err = New().Extract(".devcontainer.json", img, manager.Env{})
	if err != nil || len(deps) != 1 {
		t.Fatalf("Extract = %v, %v", deps, err)
	}
	got = update(t, img, dep.Update{Dep: deps[0], NewVersion: "1-1.23-bookworm", NewDigest: "sha256:abc"})
	if want := strings.Replace(string(img), "go:1-1.22-bookworm", "go:1-1.23-bookworm@sha256:abc", 1); got != want {
		t.Errorf("image pin:\n%s", got)
	}
	got = update(t, img, dep.Update{Dep: deps[0], NewName: "ghcr.io/org/go", NewVersion: "1-1.22-bookworm"})
	if want := strings.Replace(string(img), "mcr.microsoft.com/devcontainers/go:", "ghcr.io/org/go:", 1); got != want {
		t.Errorf("image replacement:\n%s", got)
	}
}

func TestRename(t *testing.T) {
	m := New().(manager.Renamer)
	d, err := m.Rename(dep.Dep{DepType: DepTypeImage}, "ghcr.io/org/img")
	if err != nil || d.Name != "ghcr.io/org/img" || d.Source != "ghcr.io/org/img" {
		t.Errorf("Rename image = %+v, %v", d, err)
	}
	if _, err := m.Rename(dep.Dep{DepType: DepTypeFeature}, "ghcr.io/org/f:1"); err == nil {
		t.Error("Rename with tag: no error")
	}
	if _, err := m.Rename(dep.Dep{DepType: DepTypeFeatureVersion}, "node"); err == nil {
		t.Error("Rename feature version: no error")
	}
}
