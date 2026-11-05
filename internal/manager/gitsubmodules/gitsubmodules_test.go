package gitsubmodules

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const (
	c1 = "1111111111111111111111111111111111111111"
	c2 = "2222222222222222222222222222222222222222"
	c3 = "3333333333333333333333333333333333333333"
	c4 = "4444444444444444444444444444444444444444"
)

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{".gitmodules": true, "third_party/.gitmodules": true, "gitmodules": false, ".gitmodules.bak": false} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

const gitmodules = `# vendored libraries
[submodule "libfoo"]
	path = lib/foo
	url = https://github.com/acme/libfoo.git
[submodule "bar"]
	path = lib/bar
	url = git@gitlab.com:group/bar.git
	branch = release/1.x ; tracked by CI
[Submodule "proto"]
	PATH = "lib/proto"
	url = ssh://git@git.example.com:2222/team/proto
	branch = "v1.2.0"
[submodule.legacy]
	path = lib/legacy
	url = https://github.com/acme/legacy
	branch = v2.0.0 # pinned release
[core]
	path = not/a/submodule
[submodule "rel"]
	path = lib/rel
	url = ../rel.git
[submodule "nopath"]
	url = https://github.com/acme/nopath
[submodule "missing"]
	path = lib/missing
	url = https://github.com/acme/missing
[submodule "dot"]
	path = lib/dot
	url = https://github.com/acme/dot
	branch = .
[submodule "escaped"]
	path = lib/escaped
	url = https://github.com/acme/escaped
	branch = "v1\.0.0"
[submodule "outside"]
	path = ../elsewhere
	url = https://github.com/acme/outside
`

func TestExtract(t *testing.T) {
	var asked []string
	env := manager.Env{
		Gitlinks: func(paths []string) (map[string]string, error) {
			asked = paths
			return map[string]string{"lib/foo": c1, "lib/bar": c2, "lib/proto": c3, "lib/legacy": c4, "lib/rel": c2, "lib/dot": c1, "lib/escaped": c1}, nil
		},
		Superproject: func() (string, string, error) { return "https://github.com/acme/app.git", "main", nil },
	}
	deps, err := New().Extract(".gitmodules", []byte(gitmodules), env)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib/foo", "lib/bar", "lib/proto", "lib/legacy", "lib/rel", "lib/missing", "lib/dot", "lib/escaped"}; !slices.Equal(asked, want) {
		t.Errorf("Gitlinks asked for %v, want %v", asked, want)
	}
	span := func(s string) dep.Span {
		i := strings.Index(gitmodules, s)
		return dep.Span{Start: i, End: i + len(s)}
	}
	base := dep.Dep{Manager: Name, Datasource: "git-refs", Versioning: "tag", DepType: DepTypeSubmodule, File: ".gitmodules"}
	with := func(f func(d *dep.Dep)) dep.Dep {
		d := base
		f(&d)
		return d
	}
	want := []dep.Dep{
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 2, "acme/libfoo", "https://github.com/acme/libfoo", "lib/foo", "HEAD", c1
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 8, "group/bar", "https://gitlab.com/group/bar", "lib/bar", "release/1.x", c2
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 12, "team/proto", "https://git.example.com/team/proto", "lib/proto", "v1.2.0", c3
			d.Datasource, d.RefSpan = "git-tags", span("v1.2.0")
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 16, "acme/legacy", "https://github.com/acme/legacy", "lib/legacy", "v2.0.0", c4
			d.Datasource, d.RefSpan = "git-tags", span("v2.0.0")
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 19, "acme/rel", "https://github.com/acme/rel", "lib/rel", "HEAD", c2
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Skip = 22, "acme/nopath", "https://github.com/acme/nopath", "no path"
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Skip = 24, "acme/missing", "https://github.com/acme/missing", "lib/missing", "not a gitlink in the git index"
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 30, "acme/dot", "https://github.com/acme/dot", "lib/dot", "main", c1
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Gitlink, d.Version, d.Digest = 34, "acme/escaped", "https://github.com/acme/escaped", "lib/escaped", "v1.0.0", c1
			d.Datasource, d.Skip = "git-tags", "cannot locate branch value"
		}),
		with(func(d *dep.Dep) {
			d.Line, d.Name, d.Source, d.Skip = 35, "acme/outside", "https://github.com/acme/outside", "invalid path"
		}),
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(want), deps)
	}
	for i := range want {
		if !reflect.DeepEqual(deps[i], want[i]) {
			t.Errorf("dep %d:\n got %+v\nwant %+v", i, deps[i], want[i])
		}
	}
}

func TestExtractSuperproject(t *testing.T) {
	src := "[submodule \"rel\"]\n\tpath = rel\n\turl = ../rel.git\n[submodule \"dot\"]\n\tpath = dot\n\turl = https://github.com/acme/dot\n\tbranch = .\n"
	links := func([]string) (map[string]string, error) { return map[string]string{"rel": c1, "dot": c1}, nil }
	tests := []struct {
		name, origin, branch string
		relName, relSkip     string
		dotVersion, dotSkip  string
	}{
		{"https origin", "https://gitea.example.com/team/app", "dev", "team/rel", "", "dev", ""},
		{"ssh origin", "ssh://git@git.example.com:2222/team/app.git", "dev", "team/rel", "", "dev", ""},
		// Renovate's new URL() cannot take these as a base.
		{"scp-style origin", "git@github.com:team/app.git", "dev", "../rel.git", "relative url without a URL origin remote", "dev", ""},
		{"no origin, detached", "", "", "../rel.git", "relative url without a URL origin remote", ".", "branch = . with a detached HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, err := New().Extract(".gitmodules", []byte(src), manager.Env{Gitlinks: links, Superproject: func() (string, string, error) {
				return tt.origin, tt.branch, nil
			}})
			if err != nil || len(deps) != 2 {
				t.Fatalf("Extract = %+v, %v", deps, err)
			}
			if d := deps[0]; d.Name != tt.relName || d.Skip != tt.relSkip {
				t.Errorf("relative url dep = %q skip %q, want %q skip %q", d.Name, d.Skip, tt.relName, tt.relSkip)
			}
			if d := deps[1]; d.Version != tt.dotVersion || d.Skip != tt.dotSkip || d.Datasource != "git-refs" {
				t.Errorf("branch = . dep = %q skip %q (%s), want %q skip %q", d.Version, d.Skip, d.Datasource, tt.dotVersion, tt.dotSkip)
			}
		})
	}
	_, err := New().Extract(".gitmodules", []byte(src), manager.Env{Gitlinks: links, Superproject: func() (string, string, error) {
		return "", "", errors.New("git config: exit status 128")
	}})
	if err == nil {
		t.Error("superproject lookup failure was not reported")
	}
}

func TestGitSuperproject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "trunk")
	if origin, branch, err := GitSuperproject(repo)(); err != nil || origin != "" || branch != "trunk" {
		t.Errorf("without origin = %q, %q, %v", origin, branch, err)
	}
	run("remote", "add", "origin", "https://github.com/acme/app")
	run("-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "x")
	run("checkout", "-q", "--detach")
	if origin, branch, err := GitSuperproject(repo)(); err != nil || origin != "https://github.com/acme/app" || branch != "" {
		t.Errorf("detached = %q, %q, %v", origin, branch, err)
	}
	if _, _, err := GitSuperproject(t.TempDir())(); err == nil {
		t.Error("outside a work tree: no error")
	}
}

func TestExtractNestedFile(t *testing.T) {
	src := "[submodule \"x\"]\n\tpath = x\n\turl = https://github.com/acme/x\n"
	deps, err := New().Extract("third_party/.gitmodules", []byte(src), manager.Env{Gitlinks: func(paths []string) (map[string]string, error) {
		return map[string]string{"third_party/x": c1}, nil
	}})
	if err != nil || len(deps) != 1 || deps[0].Gitlink != "third_party/x" || deps[0].Digest != c1 || deps[0].Skip != "" {
		t.Fatalf("Extract = %+v, %v", deps, err)
	}
}

func TestEdits(t *testing.T) {
	src := "[submodule \"a\"]\n\tpath = a\n\turl = https://github.com/acme/a\n\tbranch = \"v1.2.0\" # release\n"
	deps, err := New().Extract(".gitmodules", []byte(src), manager.Env{Gitlinks: func([]string) (map[string]string, error) {
		return map[string]string{"a": c1}, nil
	}})
	if err != nil || len(deps) != 1 {
		t.Fatalf("Extract = %+v, %v", deps, err)
	}
	m := New()
	edits, err := m.Edits(dep.Update{Dep: deps[0], NewVersion: "v1.3.0", NewDigest: c2})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(src, "v1.2.0", "v1.3.0", 1); string(out) != want {
		t.Errorf("patched:\n%s\nwant:\n%s", out, want)
	}
	if edits, err := m.Edits(dep.Update{Dep: deps[0], NewVersion: "v1.2.0", NewDigest: c2}); err != nil || edits != nil {
		t.Errorf("digest update edits = %+v, %v; want none (only the gitlink moves)", edits, err)
	}
	branch := dep.Dep{Manager: Name, Datasource: "git-refs", Version: "main", Digest: c1}
	if _, err := m.Edits(dep.Update{Dep: branch, NewVersion: "dev", NewDigest: c2}); err == nil {
		t.Error("changing a tracked branch succeeded")
	}
}

func TestIndexGitlinks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, "sub", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "lib", "file"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "sub/lib/file")
	run("update-index", "--add", "--cacheinfo", "160000,"+c1+",sub/lib/foo")
	run("update-index", "--add", "--cacheinfo", "160000,"+c2+",sub/lib/a*b")

	// Paths are relative to a root below the work tree's top and taken literally.
	links, err := IndexGitlinks(filepath.Join(repo, "sub"))([]string{"lib/foo", "lib/file", "lib/a*b", "lib/none"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"lib/foo": c1, "lib/a*b": c2}; !reflect.DeepEqual(links, want) {
		t.Errorf("gitlinks = %v, want %v", links, want)
	}
	if _, err := IndexGitlinks(t.TempDir())([]string{"x"}); err == nil || !strings.HasPrefix(err.Error(), "git ls-files: ") {
		t.Errorf("outside a work tree: err = %v", err)
	}
}
