package engine

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bazel"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
)

// pkgUpdate writes a package.json depending on a@1.0.0 at file under root and returns the
// update of a to 1.1.0.
func pkgUpdate(t *testing.T, root, file string) dep.Update {
	t.Helper()
	const pkg = `{"dependencies": {"a": "^1.0.0"}}` + "\n"
	writeFile(t, root, file, pkg)
	i := strings.Index(pkg, "1.0.0")
	return dep.Update{
		Dep:  dep.Dep{Manager: npm.Name, File: file, Line: 1, Name: "a", Version: "1.0.0", RefSpan: dep.Span{Start: i, End: i + 5}},
		Type: dep.Minor, NewVersion: "1.1.0",
	}
}

func writeFile(t *testing.T, root, file, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lockRoots scopes package.json files below ws/ to ws, others to their directory, and
// nothing under nolock/.
func lockRoots(_, manifest string) (string, bool) {
	switch {
	case strings.HasPrefix(manifest, "ws/"):
		return "ws", true
	case strings.HasPrefix(manifest, "nolock/"):
		return "", false
	}
	return filepath.ToSlash(filepath.Dir(manifest)), true
}

type processCall struct {
	dir       string
	manifests []string
}

// recorder is an npm post-processor stand-in that records its calls and writes a lock listing
// the manifests it saw; it fails for scopes named fail.
type recorder struct {
	mu    sync.Mutex
	calls []processCall
}

func (r *recorder) post() map[string]PostProcessor {
	return map[string]PostProcessor{npm.Name: postFunc{siblings: []string{"lock"}, scope: lockRoots, f: func(_ context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
		names := make([]string, 0, len(manifests))
		for n := range manifests {
			names = append(names, n)
		}
		slices.Sort(names)
		r.mu.Lock()
		r.calls = append(r.calls, processCall{dir, names})
		r.mu.Unlock()
		if filepath.Base(dir) == "fail" {
			return nil, errors.New("install failed")
		}
		out := map[string][]byte{"lock": append(append([]byte(nil), files["lock"]...), strings.Join(names, ",")+"\n"...)}
		if b, ok := manifests["package.json"]; ok {
			out["package.json"] = append(append([]byte(nil), b...), "// normalized\n"...)
		}
		return out, nil
	}}}
}

func paths(changes []FileChange) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Path
	}
	return out
}

func TestBuildGroupsManifestsByScope(t *testing.T) {
	root := t.TempDir()
	ups := []dep.Update{
		pkgUpdate(t, root, "ws/packages/b/package.json"),
		pkgUpdate(t, root, "ws/package.json"),
		pkgUpdate(t, root, "ws/packages/a/package.json"),
		pkgUpdate(t, root, "solo/package.json"),
		pkgUpdate(t, root, "nolock/package.json"),
	}
	writeFile(t, root, "ws/lock", "old\n")
	r := &recorder{}
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New()}, ups, r.post())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	slices.SortFunc(r.calls, func(a, b processCall) int { return strings.Compare(a.dir, b.dir) })
	want := []processCall{
		{filepath.Join(root, "solo"), []string{"package.json"}},
		{filepath.Join(root, "ws"), []string{"package.json", "packages/a/package.json", "packages/b/package.json"}},
	}
	if len(r.calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", r.calls, want)
	}
	for i, c := range r.calls {
		if c.dir != want[i].dir || !slices.Equal(c.manifests, want[i].manifests) {
			t.Errorf("call %d = %+v, want %+v", i, c, want[i])
		}
	}
	wantPaths := []string{"nolock/package.json", "solo/lock", "solo/package.json", "ws/lock", "ws/package.json", "ws/packages/a/package.json", "ws/packages/b/package.json"}
	if got := paths(changes); !slices.Equal(got, wantPaths) {
		t.Fatalf("changes = %v, want %v", got, wantPaths)
	}
	byPath := map[string]FileChange{}
	for _, c := range changes {
		byPath[c.Path] = c
	}
	if c := byPath["ws/lock"]; string(c.Old) != "old\n" || string(c.New) != "old\npackage.json,packages/a/package.json,packages/b/package.json\n" || c.Mode != 0o644 {
		t.Errorf("ws/lock = %q -> %q (%v)", c.Old, c.New, c.Mode)
	}
	if c := byPath["solo/lock"]; c.Old != nil || string(c.New) != "package.json\n" {
		t.Errorf("solo/lock = %q -> %q, want a new lock", c.Old, c.New)
	}
	const patched = `{"dependencies": {"a": "^1.1.0"}}` + "\n"
	if got := string(byPath["ws/package.json"].New); got != patched+"// normalized\n" {
		t.Errorf("root manifest = %q, want the post-processor's result", got)
	}
	for _, p := range []string{"ws/packages/a/package.json", "nolock/package.json"} {
		if got := string(byPath[p].New); got != patched {
			t.Errorf("%s = %q, want the patch only", p, got)
		}
	}
}

func TestBuildScopeFailureDropsAllManifests(t *testing.T) {
	root := t.TempDir()
	scope := func(_, manifest string) (string, bool) {
		if strings.HasPrefix(manifest, "fail/") {
			return "fail", true
		}
		return "ok", true
	}
	ups := []dep.Update{
		pkgUpdate(t, root, "fail/a/package.json"),
		pkgUpdate(t, root, "fail/b/package.json"),
		pkgUpdate(t, root, "ok/package.json"),
	}
	writeFile(t, root, "fail/lock", "old\n")
	r := &recorder{}
	post := r.post()
	p := post[npm.Name].(postFunc)
	p.scope = scope
	post[npm.Name] = p
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New()}, ups, post)
	if len(errs) != 1 || errs[0].Error() != "fail/a/package.json, fail/b/package.json: install failed" {
		t.Fatalf("errs = %v", errs)
	}
	if got := paths(changes); !slices.Equal(got, []string{"ok/lock", "ok/package.json"}) {
		t.Errorf("changes = %v, want only the ok scope", got)
	}
	if len(r.calls) != 2 {
		t.Errorf("calls = %+v, want one per scope", r.calls)
	}
}

func TestBuildScopeErrors(t *testing.T) {
	root := t.TempDir()
	outside := pkgUpdate(t, root, "x/package.json")
	badPatch := pkgUpdate(t, root, "y/package.json")
	badPatch.Dep.Version = "0.9.0"
	post := map[string]PostProcessor{npm.Name: postFunc{
		scope: func(_, manifest string) (string, bool) {
			if strings.HasPrefix(manifest, "x/") {
				return "elsewhere", true
			}
			return "y", true
		},
		f: func(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
			t.Error("Process called")
			return nil, nil
		},
	}}
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New()}, []dep.Update{outside, badPatch}, post)
	if len(errs) != 2 || errs[0].Error() != "x/package.json: post-processing scope elsewhere does not contain x/package.json" ||
		!strings.HasPrefix(errs[1].Error(), "y/package.json: edit at ") {
		t.Fatalf("errs = %v", errs)
	}
	if changes != nil {
		t.Errorf("changes = %v", paths(changes))
	}
}

func TestBuildRejectsTwoPostProcessorsPerFile(t *testing.T) {
	root := t.TempDir()
	up := pkgUpdate(t, root, "package.json")
	other := up
	other.Dep.Manager = gomod.Name
	post := map[string]PostProcessor{npm.Name: postFunc{}, gomod.Name: postFunc{}}
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New(), gomod.New()}, []dep.Update{up, other}, post)
	if len(errs) != 1 || errs[0].Error() != "package.json: updates of post-processed managers npm and gomod" || changes != nil {
		t.Fatalf("changes = %v, errs = %v", paths(changes), errs)
	}
}

// sharedPost is a comparable post-processor that counts its runs; managers registered with the
// same value share it.
type sharedPost struct{ runs *atomic.Int32 }

func (sharedPost) Scope(_ context.Context, _, _ string) (string, bool) { return ".", true }
func (sharedPost) Siblings() []string                                  { return []string{"uv.lock"} }
func (p sharedPost) Process(_ context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
	p.runs.Add(1)
	return map[string][]byte{"uv.lock": manifests["pyproject.toml"]}, nil
}

// A pyproject.toml updated by pep621 and poetry is post-processed once by their shared locker.
func TestBuildSharedPostProcessor(t *testing.T) {
	root := t.TempDir()
	const src = "[project]\ndependencies = [\"a>=1.0\"]\n[tool.poetry.dependencies]\nb = \"^2.0\"\n"
	writeFile(t, root, "pyproject.toml", src)
	writeFile(t, root, "uv.lock", "")
	at := func(s string) dep.Span { i := strings.Index(src, s); return dep.Span{Start: i, End: i + len(s)} }
	ups := []dep.Update{
		{Dep: dep.Dep{Manager: pep621.Name, File: "pyproject.toml", Name: "a", Version: "1.0", Range: ">=1.0", RefSpan: at(">=1.0")}, NewVersion: "1.1"},
		{Dep: dep.Dep{Manager: poetry.Name, File: "pyproject.toml", Name: "b", Version: "2.0", Range: "^2.0", RefSpan: at("^2.0")}, NewVersion: "2.1"},
	}
	p := sharedPost{runs: new(atomic.Int32)}
	changes, errs := Build(t.Context(), root, []manager.Manager{pep621.New(), poetry.New()}, ups, map[string]PostProcessor{pep621.Name: p, poetry.Name: p})
	want := "[project]\ndependencies = [\"a>=1.1\"]\n[tool.poetry.dependencies]\nb = \"^2.1\"\n"
	if errs != nil || len(changes) != 2 || string(changes[0].New) != want || string(changes[1].New) != want || p.runs.Load() != 1 {
		t.Fatalf("changes = %v, errs = %v, runs = %d", paths(changes), errs, p.runs.Load())
	}
	other := sharedPost{runs: new(atomic.Int32)}
	_, errs = Build(t.Context(), root, []manager.Manager{pep621.New(), poetry.New()}, ups, map[string]PostProcessor{pep621.Name: p, poetry.Name: other})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "updates of post-processed managers pep621 and poetry") {
		t.Errorf("distinct post-processors: errs = %v", errs)
	}
}

// sharedScopePost is a SharedScope post-processor that writes its manifest names to lock.
type sharedScopePost struct{ runs *atomic.Int32 }

func (sharedScopePost) Scope(_ context.Context, _, _ string) (string, bool) { return ".", true }
func (sharedScopePost) Siblings() []string                                  { return []string{"lock"} }
func (sharedScopePost) SharedScope()                                        {}
func (p sharedScopePost) Process(_ context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
	p.runs.Add(1)
	return map[string][]byte{"lock": []byte(strings.Join(slices.Sorted(maps.Keys(manifests)), ","))}, nil
}

// Files of two managers whose post-processor is a SharedScope (bazel-module's MODULE.bazel and
// bazelisk's .bazelversion) are processed together, once, in one scope.
func TestBuildSharedScope(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "MODULE.bazel", "bazel_dep(name = \"a\", version = \"1.0\")\n")
	writeFile(t, root, ".bazelversion", "9.3.1\n")
	writeFile(t, root, "lock", "")
	ups := []dep.Update{
		{Dep: dep.Dep{Manager: bazel.Name, File: "MODULE.bazel", Name: "a", Version: "1.0", RefSpan: dep.Span{Start: 33, End: 36}}, NewVersion: "1.1"},
		{Dep: dep.Dep{Manager: bazel.BazeliskName, File: ".bazelversion", Name: "bazelbuild/bazel", Version: "9.3.1", RefSpan: dep.Span{Start: 0, End: 5}}, NewVersion: "9.4.0"},
	}
	p := sharedScopePost{runs: new(atomic.Int32)}
	changes, errs := Build(t.Context(), root, []manager.Manager{bazel.New(), bazel.NewBazelisk()}, ups, map[string]PostProcessor{bazel.Name: p, bazel.BazeliskName: p})
	if errs != nil || len(changes) != 3 || p.runs.Load() != 1 {
		t.Fatalf("changes = %v, errs = %v, runs = %d", paths(changes), errs, p.runs.Load())
	}
	if changes[2].Path != "lock" || string(changes[2].New) != ".bazelversion,MODULE.bazel" {
		t.Errorf("lock = %s: %q", changes[2].Path, changes[2].New)
	}
}

// scopeSiblings is a postFunc whose siblings are listed per scope directory.
type scopeSiblings struct {
	postFunc
	list func(dir string) []string
}

func (p scopeSiblings) ScopeSiblings(dir string) []string { return p.list(dir) }

func TestBuildScopeSiblings(t *testing.T) {
	root := t.TempDir()
	up := pkgUpdate(t, root, "p/package.json")
	writeFile(t, root, "p/sub/lock", "old\n")
	var listed string
	post := map[string]PostProcessor{npm.Name: scopeSiblings{
		postFunc: postFunc{siblings: []string{"static"}, f: func(_ context.Context, _ string, _, files map[string][]byte) (map[string][]byte, error) {
			if _, ok := files["static"]; ok {
				t.Error("static siblings read")
			}
			return map[string][]byte{"sub/lock": append(files["sub/lock"], "new\n"...), "static": []byte("x"), "sub/new": []byte("y")}, nil
		}},
		list: func(dir string) []string { listed = dir; return []string{"sub/lock", "sub/new"} },
	}}
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New()}, []dep.Update{up}, post)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if listed != filepath.Join(root, "p") {
		t.Errorf("ScopeSiblings(%q)", listed)
	}
	if got := paths(changes); !slices.Equal(got, []string{"p/package.json", "p/sub/lock", "p/sub/new"}) {
		t.Fatalf("changes = %v", got)
	}
	if c := changes[1]; string(c.Old) != "old\n" || string(c.New) != "old\nnew\n" {
		t.Errorf("p/sub/lock = %q -> %q", c.Old, c.New)
	}
}

// treePost is a postFunc that regenerates the directory trees listed by trees.
type treePost struct {
	postFunc
	trees []string
}

func (p treePost) Trees(string) []string { return p.trees }

// TestBuildTrees regenerates part of a vendor tree: changed, new and deleted files become
// changes with the old content read from root; unchanged ones and results outside the
// trees do not.
func TestBuildTrees(t *testing.T) {
	root := t.TempDir()
	up := pkgUpdate(t, root, "p/package.json")
	writeFile(t, root, "p/vendor/same", "same\n")
	writeFile(t, root, "p/vendor/changed", "old\n")
	writeFile(t, root, "p/vendor/gone", "gone\n")
	if err := os.Chmod(filepath.Join(root, "p", "vendor", "changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	post := map[string]PostProcessor{npm.Name: treePost{
		postFunc: postFunc{f: func(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
			return map[string][]byte{
				"vendor/same": []byte("same\n"), "vendor/changed": []byte("new\n"), "vendor/added": []byte("added\n"),
				"vendor/gone": nil, "vendor/never": nil, "outside": []byte("x"),
			}, nil
		}},
		trees: []string{"vendor"},
	}}
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New()}, []dep.Update{up}, post)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := paths(changes); !slices.Equal(got, []string{"p/package.json", "p/vendor/added", "p/vendor/changed", "p/vendor/gone"}) {
		t.Fatalf("changes = %v", got)
	}
	if c := changes[1]; c.Old != nil || string(c.New) != "added\n" || c.Mode != 0o644 {
		t.Errorf("added: %+v", c)
	}
	if c := changes[2]; string(c.Old) != "old\n" || string(c.New) != "new\n" || c.Mode != 0o600 {
		t.Errorf("changed: %+v", c)
	}
	if c := changes[3]; string(c.Old) != "gone\n" || c.New != nil {
		t.Errorf("gone: %+v", c)
	}
}

// TestBuildTreesWholeScope regenerates the tree "." (copier): any file of the scope may
// change, the manifest's result replaces the patched manifest.
func TestBuildTreesWholeScope(t *testing.T) {
	root := t.TempDir()
	up := pkgUpdate(t, root, "p/package.json")
	writeFile(t, root, "p/README.md", "old\n")
	writeFile(t, root, "other/x", "x\n")
	post := map[string]PostProcessor{npm.Name: treePost{
		postFunc: postFunc{f: func(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
			return map[string][]byte{"package.json": []byte("{}\n"), "README.md": []byte("new\n"), "src/a.go": []byte("package a\n")}, nil
		}},
		trees: []string{"."},
	}}
	changes, errs := Build(t.Context(), root, []manager.Manager{npm.New()}, []dep.Update{up}, post)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := paths(changes); !slices.Equal(got, []string{"p/README.md", "p/package.json", "p/src/a.go"}) {
		t.Fatalf("changes = %v", got)
	}
	if c := changes[1]; string(c.New) != "{}\n" {
		t.Errorf("package.json: %q", c.New)
	}
}
