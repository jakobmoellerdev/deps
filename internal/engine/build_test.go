package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
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
