package engine

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

func sha(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

var (
	managers = []manager.Manager{actions.New(), gomod.New()}
	env      = manager.Env{DefaultHost: "github.com"}
)

type fakeTags map[string][]string // repo URL -> tag names; commit = sha(url+"@"+name)

func (f fakeTags) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	names, ok := f[d.Source]
	if !ok {
		return nil, errors.New("upstream unavailable")
	}
	out := make([]Release, len(names))
	for i, n := range names {
		out[i] = Release{Version: n, Digest: sha(d.Source + "@" + n)}
	}
	return out, nil
}

func (fakeTags) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	return "", fmt.Errorf("tag %s not found upstream", v)
}

type fakeGo map[string][]string

func (f fakeGo) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	vs, ok := f[d.Name]
	if !ok {
		return nil, errors.New("no proxy has " + d.Name)
	}
	out := make([]Release, len(vs))
	for i, v := range vs {
		out[i] = Release{Version: v}
	}
	return out, nil
}

func (fakeGo) Digest(context.Context, dep.Dep, string) (string, error) {
	return "", errors.New("no digests")
}

// sources builds Options with the given datasources (nil ones are left out).
func sources(tags fakeTags, gos fakeGo) Options {
	o := Options{Managers: managers, Datasources: map[string]Datasource{}}
	if tags != nil {
		o.Datasources["git-tags"] = tags
	}
	if gos != nil {
		o.Datasources["go"] = gos
	}
	return o
}

func fakeTidy(_ context.Context, _ string, mod, _ []byte) ([]byte, []byte, error) {
	return append(append([]byte(nil), mod...), "// tidied\n"...), []byte("sum\n"), nil
}

// postFunc adapts a function to a PostProcessor reading siblings; scope nil scopes every
// manifest to its own directory.
type postFunc struct {
	siblings []string
	f        func(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error)
	scope    func(root, manifest string) (string, bool)
}

func (p postFunc) Scope(root, manifest string) (string, bool) {
	if p.scope == nil {
		return path.Dir(manifest), true
	}
	return p.scope(root, manifest)
}

func (p postFunc) Siblings() []string { return p.siblings }

func (p postFunc) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return p.f(ctx, dir, manifests, files)
}

// tidyFunc is a go mod tidy stand-in; post registers it as the gomod post-processor.
type tidyFunc func(ctx context.Context, dir string, mod, sum []byte) ([]byte, []byte, error)

func (f tidyFunc) post() map[string]PostProcessor {
	return map[string]PostProcessor{gomod.Name: postFunc{siblings: []string{"go.sum"}, f: func(ctx context.Context, dir string, mods, files map[string][]byte) (map[string][]byte, error) {
		newMod, newSum, err := f(ctx, dir, mods["go.mod"], files["go.sum"])
		if err != nil {
			return nil, err
		}
		return map[string][]byte{"go.mod": newMod, "go.sum": newSum}, nil
	}}}
}

const (
	checkout = "https://github.com/actions/checkout"
	setupGo  = "https://github.com/actions/setup-go"
	cache    = "https://github.com/actions/cache"
)

var tags = fakeTags{
	checkout: {"v4", "v4.1.0", "v4.1.1", "v4.2.0", "v5", "v5.0.0", "v5.0.1", "v6.0.0-beta.1"},
	setupGo:  {"v5.0.0", "v5.1.0", "v6.0.0"},
	cache:    {"v4", "v3"},
}

var workflow = `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: actions/checkout@v4.1.0
      - uses: "actions/setup-go@` + sha(setupGo+"@v5.0.0") + `" # v5.0.0
      - uses: actions/cache@` + sha("stale") + ` # v4
      - uses: owner/broken@v1
`

func line(n int, s string) string { return strings.Split(s, "\n")[n-1] }

func setup(t testing.TB, cfgYAML string, files map[string]string) (string, *config.Config) {
	t.Helper()
	root := t.TempDir()
	if cfgYAML != "" {
		files[".deps.yaml"] = cfgYAML
	}
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

func TestResolveAndBuildActions(t *testing.T) {
	const wf = ".github/workflows/ci.yml"
	tests := []struct {
		name      string
		cfg       string
		wantLines map[int]string // line number in the workflow -> expected content
		wantSkip  string
	}{
		{
			name: "defaults keep style, refresh digests",
			wantLines: map[int]string{
				6: "      - uses: actions/checkout@v4.2.0", // v5.0.1 is a separate major update
				7: `      - uses: "actions/setup-go@` + sha(setupGo+"@v5.1.0") + `" # v5.1.0`,
				8: "      - uses: actions/cache@" + sha(cache+"@v4") + " # v4",
			},
		},
		{
			name: "allow constraint",
			cfg:  "rules:\n  - names: [\"actions/*\"]\n    allow: \"< 5\"\n",
			wantLines: map[int]string{
				6: "      - uses: actions/checkout@v4.2.0",
				7: `      - uses: "actions/setup-go@` + sha(setupGo+"@v5.0.0") + `" # v5.0.0`,
			},
		},
		{
			name: "updates replaced by later rule",
			cfg:  "rules:\n  - updates: [major]\n  - names: [actions/checkout]\n    updates: [patch]\n",
			wantLines: map[int]string{
				6: "      - uses: actions/checkout@v4.1.1",
				8: "      - uses: actions/cache@" + sha("stale") + " # v4",
			},
		},
		{
			name:      "ignore",
			cfg:       "rules:\n  - names: [actions/checkout]\n    ignore: true\n",
			wantLines: map[int]string{6: "      - uses: actions/checkout@v4.1.0"},
			wantSkip:  "ignored by rule #0",
		},
		{
			name:      "pin digest",
			cfg:       "pin: digest\n",
			wantLines: map[int]string{6: "      - uses: actions/checkout@" + sha(checkout+"@v4.2.0") + " # v4.2.0"},
		},
		{
			name:      "pin digest without newer version pins current",
			cfg:       "pin: digest\nrules:\n  - names: [actions/checkout]\n    allow: \"< 4.1.1\"\n",
			wantLines: map[int]string{6: "      - uses: actions/checkout@" + sha(checkout+"@v4.1.0") + " # v4.1.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{
				wf:                                     workflow,
				"testdata/.github/workflows/x.yml":     workflow,
				"ignored/.github/workflows/y.yml":      workflow,
				"node_modules/.github/workflows/z.yml": workflow,
			})
			cfg.IgnorePaths = []string{"ignored/**"}
			res, err := Resolve(t.Context(), root, cfg, env, sources(tags, nil))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), wf+":9: owner/broken: upstream unavailable") {
				t.Errorf("errors = %v, want only the broken dep", res.Errors)
			}
			for _, u := range res.Updates {
				if u.Dep.File != wf {
					t.Errorf("update from pruned/ignored file %s", u.Dep.File)
				}
			}
			if tt.wantSkip != "" && (len(res.Skipped) != 1 || res.Skipped[0].Skip != tt.wantSkip) {
				t.Errorf("skipped = %+v, want %q", res.Skipped, tt.wantSkip)
			}
			updates, _ := Local(res.Updates) // as plan and apply
			changes, errs := Build(t.Context(), root, managers, updates, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			got := workflow
			if len(changes) == 1 {
				got = string(changes[0].New)
			} else if len(changes) > 1 {
				t.Fatalf("got %d changes", len(changes))
			}
			for n, want := range tt.wantLines {
				if l := line(n, got); l != want {
					t.Errorf("line %d:\n got %s\nwant %s", n, l, want)
				}
			}
		})
	}
}

func TestResolveAndBuildGomod(t *testing.T) {
	mod := "module example.com/x\n\ngo 1.24\n\nrequire example.com/a v1.0.0\n"
	root, cfg := setup(t, "", map[string]string{"go.mod": mod})
	res, err := Resolve(t.Context(), root, cfg, env, sources(nil, fakeGo{"example.com/a": {"v1.0.0", "v1.1.0", "v1.2.0-rc.1", "v2.0.0"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.1.0" || res.Updates[0].Type != dep.Minor {
		t.Fatalf("updates = %+v", res.Updates)
	}
	changes, errs := Build(t.Context(), root, managers, res.Updates, tidyFunc(fakeTidy).post())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(changes) != 2 || changes[0].Path != "go.mod" || changes[1].Path != "go.sum" {
		t.Fatalf("changes = %+v", changes)
	}
	wantMod := strings.Replace(mod, "v1.0.0", "v1.1.0", 1) + "// tidied\n"
	if string(changes[0].New) != wantMod {
		t.Errorf("go.mod = %q, want %q", changes[0].New, wantMod)
	}
	if string(changes[1].New) != "sum\n" || changes[1].Old != nil || changes[1].Mode != 0o644 {
		t.Errorf("go.sum change = %+v", changes[1])
	}
}

func TestGroupUpdates(t *testing.T) {
	d := func(name string) dep.Dep { return dep.Dep{Manager: "github-actions", Name: name} }
	groups := GroupUpdates([]dep.Update{
		{Dep: d("actions/checkout"), Type: dep.Major, NewVersion: "v5"},
		{Dep: d("Github/CodeQL-Action/init"), Type: dep.Pin, NewVersion: "v3", NewDigest: "abcdef0123"},
		{Dep: d("actions/checkout"), Type: dep.Minor, NewVersion: "v5.1.0"},
		{Dep: d("a/b"), Type: dep.Digest, NewVersion: "v1", NewDigest: "1234567890"},
		{Dep: d("a/c"), Type: dep.Major, NewVersion: "v2"},
		{Dep: d("a/c"), Type: dep.Major, NewVersion: "v2"},
	}, "")
	want := []struct{ branch, title string }{
		{"deps/github-actions/github-codeql-action-init", "chore(deps): pin Github/CodeQL-Action/init to abcdef0"},
		{"deps/github-actions/a-b", "chore(deps): update a/b digest to 1234567"},
		{"deps/github-actions/a-c", "chore(deps): update a/c to v2"},
		{"deps/github-actions/actions-checkout", "chore(deps): update actions/checkout"},
	}
	if len(groups) != len(want) {
		t.Fatalf("got %d groups", len(groups))
	}
	for i, w := range want {
		if groups[i].Branch != w.branch || groups[i].Title != w.title {
			t.Errorf("group %d = %s %q, want %s %q", i, groups[i].Branch, groups[i].Title, w.branch, w.title)
		}
	}
}

func TestGroupBranchSlug(t *testing.T) {
	groups := GroupUpdates([]dep.Update{{Dep: dep.Dep{Manager: "gomod", Name: "--Example.com//Foo__Bar--"}, Type: dep.Minor, NewVersion: "v1.1.0"}}, "")
	if len(groups) != 1 || groups[0].Branch != "deps/gomod/example.com-foo__bar" {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestResolveErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 files")
	}
	// The pinned dep needs a digest refresh and the floating one a pin; neither current tag
	// exists upstream.
	root, cfg := setup(t, "pin: digest\n", map[string]string{
		".github/workflows/bad.yml": "jobs: [\n",
		".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n" +
			"      - uses: ./local\n" +
			"      - uses: actions/cache@" + sha(cache+"@v9") + " # v9\n",
		".github/workflows/locked.yml": "jobs: {}\n",
		".github/workflows/pin.yml":    "jobs:\n  a:\n    steps:\n      - uses: actions/cache@v9\n",
		".github/workflows/z.yml":      "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4.1.0\n",
		"go.mod":                       "module example.com/x\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/b v1.0.0\n\texample.com/c v1.0.0\n)\n",
	})
	if err := os.Chmod(filepath.Join(root, ".github", "workflows", "locked.yml"), 0); err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(t.Context(), root, cfg, env, sources(tags, fakeGo{"example.com/a": {"v1.0.0", "v1.0.1"}, "example.com/c": {"v1.0.0"}}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".github/workflows/bad.yml: ",
		".github/workflows/locked.yml: open ",
		".github/workflows/ci.yml:5: actions/cache: tag v9 not found upstream",
		".github/workflows/pin.yml:4: actions/cache: tag v9 not found upstream",
		"go.mod:5: example.com/b: no proxy has example.com/b",
	}
	if len(res.Errors) != len(want) {
		t.Fatalf("errors = %v, want %q", res.Errors, want)
	}
	for i, w := range want {
		if !strings.HasPrefix(res.Errors[i].Error(), w) {
			t.Errorf("error %d = %q, want prefix %q", i, res.Errors[i], w)
		}
	}
	if fe, ok := errors.AsType[*FileError](res.Errors[0]); !ok || fe.File != ".github/workflows/bad.yml" {
		t.Errorf("error 0 = %#v, want FileError for bad.yml", res.Errors[0])
	}
	if de, ok := errors.AsType[*DepError](res.Errors[4]); !ok || de.File != "go.mod" || de.Line != 5 || de.Name != "example.com/b" {
		t.Errorf("error 4 = %#v, want DepError for go.mod:5 example.com/b", res.Errors[4])
	}
	// Updates are sorted by file, a dep's buckets by version; example.com/c is current and
	// yields none.
	if len(res.Updates) != 3 ||
		res.Updates[0].Dep.File != ".github/workflows/z.yml" || res.Updates[0].NewDigest != sha(checkout+"@v4.2.0") || res.Updates[0].Bucket != "" ||
		res.Updates[1].Dep.File != ".github/workflows/z.yml" || res.Updates[1].NewDigest != sha(checkout+"@v5.0.1") || res.Updates[1].Bucket != "major" ||
		res.Updates[2].Dep.Name != "example.com/a" || res.Updates[2].NewVersion != "v1.0.1" || res.Updates[2].Type != dep.Patch {
		t.Errorf("updates = %+v", res.Updates)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Skip != "local action" || res.Skipped[0].Line != 4 {
		t.Errorf("skipped = %+v, want the local action", res.Skipped)
	}
}

func TestResolveFailures(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "nope")
		_, err := Resolve(t.Context(), root, config.Default(), env, sources(nil, nil))
		if !errors.Is(err, os.ErrNotExist) || !strings.HasPrefix(err.Error(), "walk "+root+": ") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		root, cfg := setup(t, "", map[string]string{".github/workflows/ci.yml": workflow})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		res, err := Resolve(ctx, root, cfg, env, sources(tags, nil))
		if !errors.Is(err, context.Canceled) || res != nil {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
}

func TestDiscoverSkipsSymlinks(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{"real.yml": workflow})
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.yml"), filepath.Join(root, ".github", "workflows", "ci.yml")); err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(t.Context(), root, cfg, env, sources(tags, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updates)+len(res.Skipped)+len(res.Errors) != 0 {
		t.Errorf("symlinked workflow was processed: %+v", res)
	}
}

// modUpdate returns a minor update of example.com/a in a go.mod at file (written under root).
func modUpdate(t *testing.T, root, file string) dep.Update {
	t.Helper()
	const mod = "module example.com/x\n\nrequire example.com/a v1.0.0\n"
	abs := filepath.Join(root, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	i := strings.Index(mod, "v1.0.0")
	return dep.Update{
		Dep:  dep.Dep{Manager: "gomod", File: file, Line: 3, Name: "example.com/a", Version: "v1.0.0", RefSpan: dep.Span{Start: i, End: i + 6}},
		Type: dep.Minor, NewVersion: "v1.1.0",
	}
}

func TestBuildErrorsDropFile(t *testing.T) {
	root := t.TempDir()
	good := modUpdate(t, root, "good/go.mod")
	if err := os.WriteFile(filepath.Join(root, "good", "go.sum"), []byte("old sum\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failing := modUpdate(t, root, "fail/go.mod")
	sumDir := modUpdate(t, root, "sumdir/go.mod")
	if err := os.Mkdir(filepath.Join(root, "sumdir", "go.sum"), 0o755); err != nil {
		t.Fatal(err)
	}
	mismatch := modUpdate(t, root, "mismatch/go.mod")
	mismatch.Dep.Version = "v0.9.0"
	missing := good
	missing.Dep.File = "missing/go.mod"

	tidyErr := errors.New("tidy failed")
	tidy := tidyFunc(func(_ context.Context, dir string, mod, sum []byte) ([]byte, []byte, error) {
		if filepath.Base(dir) == "fail" {
			return nil, nil, tidyErr
		}
		return mod, append(append([]byte(nil), sum...), "new\n"...), nil
	})
	changes, errs := Build(t.Context(), root, managers, []dep.Update{good, failing, sumDir, mismatch, missing}, tidy.post())

	if len(errs) != 4 {
		t.Fatalf("errs = %v, want 4", errs)
	}
	if !strings.HasPrefix(errs[0].Error(), "fail/go.mod: ") || !errors.Is(errs[0], tidyErr) {
		t.Errorf("errs[0] = %v, want tidy failure", errs[0])
	}
	if want := `mismatch/go.mod: edit at 44: expected "v0.9.0", found "v1.0.0"`; errs[1].Error() != want {
		t.Errorf("errs[1] = %q, want %q", errs[1], want)
	}
	if !strings.HasPrefix(errs[2].Error(), "missing/go.mod: ") || !errors.Is(errs[2], os.ErrNotExist) {
		t.Errorf("errs[2] = %v, want missing file", errs[2])
	}
	if !strings.HasPrefix(errs[3].Error(), "sumdir/go.mod: ") {
		t.Errorf("errs[3] = %v, want go.sum read failure", errs[3])
	}
	if len(changes) != 2 || changes[0].Path != "good/go.mod" || changes[1].Path != "good/go.sum" {
		t.Fatalf("changes = %+v, want only good/go.mod and good/go.sum", changes)
	}
	sum := changes[1]
	if string(sum.Old) != "old sum\n" || string(sum.New) != "old sum\nnew\n" || sum.Mode != 0o600 {
		t.Errorf("go.sum change = %q -> %q mode %v, want existing content and mode kept", sum.Old, sum.New, sum.Mode)
	}
	if !strings.Contains(string(changes[0].New), "example.com/a v1.1.0") {
		t.Errorf("go.mod = %q", changes[0].New)
	}
}

func TestBuildCanceledWhileWaitingForPostProcessor(t *testing.T) {
	// Occupy every post-processor slot so the only way out is cancellation.
	for range cap(postSem) {
		postSem <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(postSem) {
			<-postSem
		}
	})
	root := t.TempDir()
	up := modUpdate(t, root, "go.mod")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	changes, errs := Build(ctx, root, managers, []dep.Update{up}, tidyFunc(func(context.Context, string, []byte, []byte) ([]byte, []byte, error) {
		called = true
		return nil, nil, nil
	}).post())
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) || !strings.HasPrefix(errs[0].Error(), "go.mod: ") {
		t.Fatalf("errs = %v, want go.mod: context canceled", errs)
	}
	if changes != nil || called {
		t.Errorf("changes = %+v, tidy called = %v", changes, called)
	}
}

func TestBuildUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 files")
	}
	root := t.TempDir()
	up := modUpdate(t, root, "go.mod")
	if err := os.Chmod(filepath.Join(root, "go.mod"), 0); err != nil {
		t.Fatal(err)
	}
	changes, errs := Build(t.Context(), root, managers, []dep.Update{up}, nil)
	if len(errs) != 1 || !errors.Is(errs[0], os.ErrPermission) || !strings.HasPrefix(errs[0].Error(), "go.mod: ") {
		t.Fatalf("errs = %v, want go.mod permission error", errs)
	}
	if changes != nil {
		t.Errorf("changes = %+v", changes)
	}
}

func TestResolveAndBuildNpm(t *testing.T) {
	pkg := `{
  "dependencies": {"a": "^1.0.0", "local": "file:../x"},
  "devDependencies": {"@s/b": "~2.0.0"},
  "peerDependencies": {"a": ">=1.0.0"},
  "packageManager": "npm@12.1.0"
}
`
	const oldLock = `{"lockfileVersion": 3, "packages": {"node_modules/a": {"version": "1.0.0"}}}` + "\n"
	root, cfg := setup(t, "rangeStrategy: bump\n", map[string]string{
		"web/package.json":                pkg,
		"web/package-lock.json":           oldLock,
		"web/.npmrc":                      "registry=x\n",
		"web/node_modules/z/package.json": `{"dependencies": {"a": "1.0.0"}}`,
	})
	o := Options{Managers: []manager.Manager{npm.New()}, Datasources: map[string]Datasource{"npm": fakeGo{
		"a":    {"1.0.0", "1.1.0", "2.0.0-rc.1"},
		"@s/b": {"2.0.0", "2.0.1", "3.0.0"},
		"npm":  {"12.1.0", "12.2.0"},
	}}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, fmt.Sprintf("%s:%d %s %s->%s %s", u.Dep.File, u.Dep.Line, u.Dep.Name, u.Dep.Version, u.NewVersion, u.Type))
	}
	want := []string{
		"web/package.json:2 a 1.0.0->1.1.0 minor",
		"web/package.json:3 @s/b 2.0.0->2.0.1 patch",
		"web/package.json:3 @s/b 2.0.0->3.0.0 major",
		"web/package.json:5 npm 12.1.0->12.2.0 minor",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var skips []string
	for _, d := range res.Skipped {
		skips = append(skips, d.Name+": "+d.Skip)
	}
	if len(skips) != 2 || skips[0] != "local: local dependency" || !strings.HasPrefix(skips[1], "a: ignored by default (peerDependencies;") {
		t.Errorf("skipped = %q", skips)
	}

	var seen map[string][]byte
	post := map[string]PostProcessor{"npm": postFunc{siblings: []string{"package-lock.json", ".npmrc"}, f: func(_ context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
		if dir != filepath.Join(root, "web") {
			t.Errorf("dir = %s", dir)
		}
		seen = files
		return map[string][]byte{"package-lock.json": append([]byte("lock for "), manifests["package.json"][:20]...), ".npmrc": files[".npmrc"]}, nil
	}}}
	updates, _ := Local(res.Updates) // @s/b's major is a separate PR
	changes, errs := Build(t.Context(), root, o.Managers, updates, post)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if string(seen["package-lock.json"]) != oldLock || string(seen[".npmrc"]) != "registry=x\n" {
		t.Errorf("post-processor files = %q", seen)
	}
	wantPkg := strings.NewReplacer(`"^1.0.0"`, `"^1.1.0"`, `"~2.0.0"`, `"~2.0.1"`, "npm@12.1.0", "npm@12.2.0").Replace(pkg)
	if len(changes) != 2 || changes[0].Path != "web/package-lock.json" || changes[1].Path != "web/package.json" ||
		string(changes[1].New) != wantPkg || string(changes[0].Old) != oldLock || !strings.HasPrefix(string(changes[0].New), "lock for {") {
		t.Fatalf("changes = %+v", changes)
	}

	// plan: no post-processing, only package.json.
	changes, errs = Build(t.Context(), root, o.Managers, updates, nil)
	if len(errs) > 0 || len(changes) != 1 || string(changes[0].New) != wantPkg {
		t.Errorf("plan changes = %+v, %v", changes, errs)
	}
}

// skipping skips "b" with a *dep.SkipError and lists v1.1.0 for any other dep.
type skipping struct{}

func (skipping) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	if d.Name == "b" {
		return nil, fmt.Errorf("wrapped: %w", &dep.SkipError{Reason: "unknown registry corp"})
	}
	return []Release{{Version: "v1.1.0"}}, nil
}

func (skipping) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

// A datasource's SkipError skips the dep instead of failing it.
func TestDatasourceSkip(t *testing.T) {
	deps := []dep.Dep{
		{Datasource: "s", Versioning: "semver", Name: "a", Version: "v1.0.0", Line: 1},
		{Datasource: "s", Versioning: "semver", Name: "b", Version: "v1.0.0", Line: 1},
	}
	var traces []Trace
	root, cfg := setup(t, "", map[string]string{"stub.txt": "x\n"})
	res, err := Resolve(t.Context(), root, cfg, env, Options{
		Managers:    []manager.Manager{stubManager{name: "stub", deps: deps}},
		Datasources: map[string]Datasource{"s": skipping{}},
		Trace:       func(x Trace) { traces = append(traces, x) },
	})
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	if len(res.Updates) != 1 || res.Updates[0].Dep.Name != "a" {
		t.Errorf("updates = %+v", res.Updates)
	}
	const reason = "unknown registry corp"
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "b" || res.Skipped[0].Skip != reason {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if len(res.Deps) != 2 || res.Deps[0].Skip != "" || res.Deps[1].Skip != reason {
		t.Errorf("deps = %+v", res.Deps)
	}
	if len(traces) != 2 || traces[1].Err != nil || traces[1].Dep.Skip != reason {
		t.Errorf("traces = %+v", traces)
	}
}
