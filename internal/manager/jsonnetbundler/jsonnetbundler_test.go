package jsonnetbundler

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// render prints a dep with the text at its spans.
func render(d dep.Dep, src []byte) string {
	at := func(s dep.Span) string {
		if s == (dep.Span{}) {
			return "-"
		}
		return string(src[s.Start:s.End])
	}
	return fmt.Sprintf("%s %s %s@%s digest=%s ref=%s pin=%s skip=%q", d.Name, d.Datasource, d.Source, d.Version, d.Digest, at(d.RefSpan), at(d.DigestSpan), d.Skip)
}

// extract extracts the testdata files named (slash paths below root) and reads their locks,
// as the engine does.
func extract(t *testing.T, root string, names ...string) ([][]dep.Dep, map[string][]byte) {
	t.Helper()
	var files [][]dep.Dep
	srcs := map[string][]byte{}
	for _, file := range names {
		src := read(t, filepath.Join(root, filepath.FromSlash(file)))
		deps, err := New().Extract(file, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files, srcs[file] = append(files, deps), src
	}
	if errs := New().(manager.LockReader).ReadLocks(root, files); len(errs) > 0 {
		t.Fatal(errs)
	}
	return files, srcs
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"jsonnetfile.json": true, "jsonnet/kube-prometheus/jsonnetfile.json": true,
		"vendor/github.com/x/jsonnetfile.json": false, "a/vendor/x/jsonnetfile.json": false,
		"jsonnetfile.lock.json": false, "myjsonnetfile.json": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
	if m.Name() != Name {
		t.Error(m.Name())
	}
}

// The kube-prometheus layout: the root jsonnetfile.json includes jsonnet/kube-prometheus as a
// local package and tracks grafana/jsonnet-libs' master in its lock; the package's own
// jsonnetfile.json (no lock next to it) pins tags, branches and a commit.
func TestExtractKubePrometheus(t *testing.T) {
	const root = "testdata/kube-prometheus"
	files, srcs := extract(t, root, "root/jsonnetfile.json", "jsonnetfile.json")
	var got []string
	for i, f := range []string{"root/jsonnetfile.json", "jsonnetfile.json"} {
		for _, d := range files[i] {
			if d.Manager != Name || d.DepType != DepTypeDependency || d.File != f || d.Versioning != "tag" || d.Line == 0 {
				t.Errorf("dep %+v", d)
			}
			got = append(got, render(d, srcs[f]))
		}
	}
	want := []string{
		`github.com/grafana/jsonnet-libs/mixin-utils git-refs https://github.com/grafana/jsonnet-libs@master digest=74c55e5f503a1f84eb939f7c421e366d04f25961 ref=- pin=- skip=""`,
		`jsonnet/kube-prometheus git-tags @ digest= ref=- pin=- skip="local dependency"`,
		`github.com/brancz/kubernetes-grafana/grafana git-refs https://github.com/brancz/kubernetes-grafana@HEAD digest=5698c8940b6dadca3f42107b7839557bc041761f ref=- pin=5698c8940b6dadca3f42107b7839557bc041761f skip=""`,
		`github.com/grafana/grafana/grafana-mixin git-tags https://github.com/grafana/grafana@v13.2.2 digest= ref=v13.2.2 pin=- skip=""`,
		`github.com/etcd-io/etcd/contrib/mixin git-tags https://github.com/etcd-io/etcd@v3.7.2 digest= ref=v3.7.2 pin=- skip=""`,
		`github.com/prometheus-operator/prometheus-operator/jsonnet/prometheus-operator git-tags https://github.com/prometheus-operator/prometheus-operator@v0.94.1 digest= ref=v0.94.1 pin=- skip=""`,
	}
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("deps:\n%s\nwant prefix:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Branches of the package's file have no lock next to it.
	var branches, skipped int
	for _, d := range files[1] {
		if d.Datasource == dsRefs && d.DigestSpan == (dep.Span{}) {
			branches++
			if d.Skip == skipNoLock {
				skipped++
			}
		}
	}
	if branches == 0 || skipped != branches {
		t.Errorf("branches %d, skipped %d", branches, skipped)
	}
}

func TestExtractForms(t *testing.T) {
	src := []byte(`{
  "version": 1,
  "dependencies": [
    {"source": {"git": {"remote": "git@github.com:acme/lib.git", "subdir": "/lib/"}}, "version": "v1.2"},
    {"source": {"git": {"remote": "https://gitlab.com/acme/b.git"}}, "version": "main"},
    {"source": {"git": {"remote": "https://github.com/acme/escaped"}}, "version": "v1\u002e0"},
    {"source": {"git": {"remote": "https://github.com/acme/novers"}}},
    {"source": {"git": {"remote": "not a url"}}, "version": "v1"},
    {"source": {"local": {"directory": "lib"}}, "version": ""},
    {"source": {"other": {}}, "version": "v1"},
    "not an object"
  ]
}`)
	deps, err := New().Extract("jsonnetfile.json", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range deps {
		got = append(got, render(d, src))
	}
	want := []string{
		`github.com/acme/lib/lib git-tags https://github.com/acme/lib@v1.2 digest= ref=v1.2 pin=- skip=""`,
		`gitlab.com/acme/b git-refs https://gitlab.com/acme/b@main digest= ref=- pin=- skip=""`,
		`github.com/acme/escaped git-tags https://github.com/acme/escaped@v1.0 digest= ref=- pin=- skip="cannot locate value"`,
		`github.com/acme/novers git-tags https://github.com/acme/novers@ digest= ref=- pin=- skip="no version"`,
		`not a url git-tags @ digest= ref=- pin=- skip="unrecognized remote"`,
		`lib git-tags @ digest= ref=- pin=- skip="local dependency"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("deps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, src := range []string{`{"version": 1}`, `{"dependencies": []}`} {
		if deps, err := New().Extract("jsonnetfile.json", []byte(src), manager.Env{}); err != nil || deps != nil {
			t.Errorf("%s: %v, %v", src, deps, err)
		}
	}
	if _, err := New().Extract("jsonnetfile.json", []byte("{"), manager.Env{}); err == nil {
		t.Error("invalid JSON")
	}
}

func TestReadLocks(t *testing.T) {
	root := t.TempDir()
	branch := func(remote string) string {
		return `{"version": 1, "dependencies": [{"source": {"git": {"remote": "` + remote + `"}}, "version": "main"}]}`
	}
	commit := strings.Repeat("a", 40)
	write(t, root, map[string]string{
		"ok/jsonnetfile.json":         branch("https://github.com/acme/a"),
		"ok/jsonnetfile.lock.json":    `{"dependencies": [{"source": {"git": {"remote": "https://github.com/acme/a.git", "subdir": ""}}, "version": "` + commit + `"}, {"source": {"local": {"directory": "x"}}, "version": ""}]}`,
		"bad/jsonnetfile.json":        branch("https://github.com/acme/a"),
		"bad/jsonnetfile.lock.json":   "{",
		"none/jsonnetfile.json":       branch("https://github.com/acme/a"),
		"other/jsonnetfile.json":      branch("https://github.com/acme/b"),
		"other/jsonnetfile.lock.json": `{"dependencies": [{"source": {"git": {"remote": "https://github.com/acme/a.git"}}, "version": "` + commit + `"}]}`,
	})
	var files [][]dep.Dep
	names := []string{"ok", "bad", "none", "other"}
	for _, n := range names {
		file := n + "/jsonnetfile.json"
		deps, err := New().Extract(file, read(t, filepath.Join(root, file)), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, deps)
	}
	errs := New().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "bad/jsonnetfile.lock.json: ") {
		t.Errorf("errs = %v", errs)
	}
	want := []string{commit, "", "", ""}
	skips := []string{"", skipUnreadable, skipNoLock, skipNotLocked}
	for i, deps := range files {
		if d := deps[0]; d.Digest != want[i] || d.Skip != skips[i] {
			t.Errorf("%s: digest %q skip %q", names[i], d.Digest, d.Skip)
		}
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`{"dependencies": [
  {"source": {"git": {"remote": "https://github.com/acme/tag"}}, "version": "v1.0.0"},
  {"source": {"git": {"remote": "https://github.com/acme/pin"}}, "version": "` + strings.Repeat("a", 40) + `"},
  {"source": {"git": {"remote": "https://github.com/acme/branch"}}, "version": "main"}
]}`)
	deps, err := New().Extract("jsonnetfile.json", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("b", 40)
	apply := func(u dep.Update) string {
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
	if got := apply(dep.Update{Dep: deps[0], NewVersion: "v1.1.0"}); !strings.Contains(got, `"version": "v1.1.0"`) {
		t.Errorf("tag:\n%s", got)
	}
	if got := apply(dep.Update{Dep: deps[1], NewVersion: "HEAD", NewDigest: head}); !strings.Contains(got, `"version": "`+head+`"`) {
		t.Errorf("commit:\n%s", got)
	}
	if edits, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "main", NewDigest: head}); err != nil || edits != nil {
		t.Errorf("branch: %v, %v", edits, err)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[2], NewVersion: "dev"}); err == nil {
		t.Error("branch change")
	}
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"b/jsonnetfile.json": "{}", "b/jsonnetfile.lock.json": "{}", "a/jsonnetfile.json": "{}", "a/jsonnetfile.lock.json": "{}", "c/jsonnetfile.json": "{}"})
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"b/jsonnetfile.json", "c/jsonnetfile.json", "a/jsonnetfile.json"})
	want := []manager.LockRoot{{Manifest: "a/jsonnetfile.json", Lockfile: "a/jsonnetfile.lock.json"}, {Manifest: "b/jsonnetfile.json", Lockfile: "b/jsonnetfile.lock.json"}}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %v", got)
	}
}
