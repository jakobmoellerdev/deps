package engine

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func sha(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

type fakeTags map[string][]string // repo URL -> tag names; commit = sha(url+"@"+name)

func (f fakeTags) Tags(_ context.Context, url string) ([]gittags.Tag, error) {
	names, ok := f[url]
	if !ok {
		return nil, errors.New("upstream unavailable")
	}
	out := make([]gittags.Tag, len(names))
	for i, n := range names {
		out[i] = gittags.Tag{Name: n, Commit: sha(url + "@" + n)}
	}
	return out, nil
}

type fakeGo map[string][]string

func (f fakeGo) Versions(_ context.Context, mod, _ string) ([]string, error) {
	return f[mod], nil
}

type fakeTidy struct{}

func (fakeTidy) Tidy(_ context.Context, _ string, mod, _ []byte) ([]byte, []byte, error) {
	return append(append([]byte(nil), mod...), "// tidied\n"...), []byte("sum\n"), nil
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

func setup(t *testing.T, cfgYAML string, files map[string]string) (string, *config.Config) {
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
				6: "      - uses: actions/checkout@v5.0.1",
				7: `      - uses: "actions/setup-go@` + sha(setupGo+"@v6.0.0") + `" # v6.0.0`,
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
			wantSkip:  "ignored by rule",
		},
		{
			name:      "pin digest",
			cfg:       "pin: digest\n",
			wantLines: map[int]string{6: "      - uses: actions/checkout@" + sha(checkout+"@v5.0.1") + " # v5.0.1"},
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
			res, err := Resolve(t.Context(), root, cfg, "github.com", Sources{Tags: tags})
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
			changes, errs := Build(t.Context(), root, res.Updates, nil)
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
	res, err := Resolve(t.Context(), root, cfg, "github.com", Sources{Go: fakeGo{"example.com/a": {"v1.0.0", "v1.1.0", "v1.2.0-rc.1", "v2.0.0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.1.0" || res.Updates[0].Type != dep.Minor {
		t.Fatalf("updates = %+v", res.Updates)
	}
	changes, errs := Build(t.Context(), root, res.Updates, fakeTidy{})
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
	})
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
