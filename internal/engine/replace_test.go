package engine

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

// movedImages lists tags of the images under their new names only (Source, the canonical
// name); the old ones fail like a registry that dropped them. Digests are imageDigest of the
// familiar name.
type movedImages map[string][]string

func (m movedImages) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	tags, ok := m[d.Source]
	if !ok {
		return nil, errors.New("releases listed for " + d.Name)
	}
	out := make([]Release, len(tags))
	for i, v := range tags {
		out[i] = Release{Version: v}
	}
	return out, nil
}

func (movedImages) Digest(_ context.Context, d dep.Dep, v string) (string, error) {
	return imageDigest(d.Name, v), nil
}

// TestReplacements resolves and builds replacement updates: built-in registry moves and npm
// renames, rule replacements with and without a version and by prefix, a digest-pinned image
// re-pinned to the new name's digest, Helm values images with and without a registry key,
// and the replacements that do not apply: a missing version (a rule's skips the dep, a
// built-in one leaves it to its usual updates), replacements: false and an ignore by update
// type. `updates` lists do not stop replacements. A replacement's usual updates are kept as
// Declined.
func TestReplacements(t *testing.T) {
	pinned := "k8s.gcr.io/etcd:3.5.0@" + imageDigest("k8s.gcr.io/etcd", "3.5.0")
	files := map[string]string{
		"Dockerfile":               "FROM k8s.gcr.io/pause:3.9\nFROM docker.io/library/alpine:3.20\nFROM quay.io/old/app:1.0\n",
		"k8s.yaml":                 "spec:\n  containers:\n    - image: " + pinned + "\n",
		"values.yaml":              "image:\n  repository: \"k8s.gcr.io/split\"\n  tag: 1.0.0\nsidecarImage:\n  registry: k8s.gcr.io\n  repository: reg\n  tag: 2.0.0\n",
		"package.json":             `{"dependencies": {"jade": "^1.11.0", "request": "~2.88.0", "left-pad": "1.3.0", "cucumber": "6.0.0"}}` + "\n",
		".github/workflows/ci.yml": "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - uses: google-github-actions/release-please-action@v4\n",
		"go.mod":                   "module example.com/x\n\nrequire example.com/old v1.2.0\n",
		"main.go":                  "package main\n\nimport _ \"example.com/old/pkg\"\n",
	}
	images := movedImages{
		"registry.k8s.io/pause": {"3.9"}, "registry.k8s.io/etcd": {"3.5.0"}, "registry.k8s.io/split": {"1.0.0"},
		"registry.k8s.io/reg": {"2.0.0"}, "ghcr.io/x/alpine": {"3.20"}, "ghcr.io/new/app": {"1.0.0"}, // a release 1.0.0 matches the 1.0 written
	}
	type want struct {
		file, line string // line of file after the build
	}
	tests := []struct {
		name    string
		cfg     string
		updates map[string]string // dep name -> "type to"
		skipped map[string]string // dep name -> skip reason
		looked  []string          // image deps looked up under their old name (failing)
		want    []want
	}{
		{
			name: "built-in and rule replacements",
			cfg: `rangeStrategy: bump
rules:
  - names: [request]
    replacementName: got
    replacementVersion: 12.0.0
    allowedUpdateTypes: [patch]
  - names: [alpine]
    replacementName: ghcr.io/x/alpine
  - names: ['quay.io/old/**']
    replacementPrefix: quay.io/old/
    replacementName: ghcr.io/new/
  - managers: [gomod]
    replacementName: example.com/new
    replacementVersion: v2.0.0
`,
			updates: map[string]string{
				"k8s.gcr.io/pause": "replacement registry.k8s.io/pause@3.9",
				"k8s.gcr.io/etcd":  "replacement registry.k8s.io/etcd@3.5.0@" + dep.ShortDigest(imageDigest("registry.k8s.io/etcd", "3.5.0")),
				"k8s.gcr.io/split": "replacement registry.k8s.io/split@1.0.0",
				"k8s.gcr.io/reg":   "replacement registry.k8s.io/reg@2.0.0",
				"alpine":           "replacement ghcr.io/x/alpine@3.20",
				"quay.io/old/app":  "replacement ghcr.io/new/app@1.0",
				"jade":             "replacement pug@2.0.0",
				"request":          "replacement got@12.0.0",
				"left-pad":         "patch 1.3.1",
				"cucumber":         "minor 6.1.0", // built-in, but @cucumber/cucumber has no 7.0.0 here
				"google-github-actions/release-please-action": "replacement googleapis/release-please-action@v4",
				"example.com/old": "replacement example.com/new@v2.0.0",
			},
			want: []want{
				{"Dockerfile", "FROM registry.k8s.io/pause:3.9"},
				{"Dockerfile", "FROM ghcr.io/x/alpine:3.20"},
				{"Dockerfile", "FROM ghcr.io/new/app:1.0"},
				{"k8s.yaml", "    - image: registry.k8s.io/etcd:3.5.0@" + imageDigest("registry.k8s.io/etcd", "3.5.0")},
				{"values.yaml", `  repository: "registry.k8s.io/split"`},
				{"values.yaml", "  registry: registry.k8s.io"},
				{"values.yaml", "  repository: reg"},
				{"package.json", `{"dependencies": {"pug": "^2.0.0", "got": "~12.0.0", "left-pad": "1.3.1", "cucumber": "6.1.0"}}`},
				{".github/workflows/ci.yml", "      - uses: googleapis/release-please-action@v4"},
				{"go.mod", "require example.com/new v2.0.0"},
				{"main.go", `import _ "example.com/new/pkg"`},
			},
		},
		{
			name: "missing version of a rule's replacement",
			cfg:  "rules: [{names: [request], replacementName: got, replacementVersion: 13.0.0}]\n",
			updates: map[string]string{"jade": "replacement pug@2.0.0", "left-pad": "patch 1.3.1", "cucumber": "minor 6.1.0",
				"k8s.gcr.io/pause": "replacement registry.k8s.io/pause@3.9",
				"k8s.gcr.io/etcd":  "replacement registry.k8s.io/etcd@3.5.0@" + dep.ShortDigest(imageDigest("registry.k8s.io/etcd", "3.5.0")),
				"k8s.gcr.io/split": "replacement registry.k8s.io/split@1.0.0",
				"k8s.gcr.io/reg":   "replacement registry.k8s.io/reg@2.0.0",
				"google-github-actions/release-please-action": "replacement googleapis/release-please-action@v4",
			},
			skipped: map[string]string{"request": "replacement got has no version 13.0.0"},
			looked:  []string{"alpine", "quay.io/old/app"},
		},
		{
			name: "replacements off and replacement ignored",
			cfg: `builtins: {replacements: false}
rangeStrategy: bump
rules:
  - names: [request]
    replacementName: got
  - updateTypes: [replacement]
    ignore: true
`,
			updates: map[string]string{"jade": "minor 1.12.0", "request": "patch 2.88.2", "left-pad": "patch 1.3.1"},
			looked:  []string{"k8s.gcr.io/split", "k8s.gcr.io/reg", "k8s.gcr.io/pause", "k8s.gcr.io/etcd", "alpine", "quay.io/old/app"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, maps.Clone(files))
			ms := []manager.Manager{docker.New(), npm.New(), actions.New(), gomod.New()}
			res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: ms, Datasources: map[string]Datasource{
				"docker": images,
				"npm": staticReleases{"jade": {"1.11.0", "1.12.0"}, "request": {"2.88.0", "2.88.2"}, "left-pad": {"1.3.0", "1.3.1"},
					"cucumber": {"6.0.0", "6.1.0"}, "pug": {"2.0.0"}, "got": {"12.0.0"}},
				"git-tags": fakeTags{"https://github.com/google-github-actions/release-please-action": {"v4"}, "https://github.com/googleapis/release-please-action": {"v4"}},
				"go":       fakeGo{"example.com/old": {"v1.2.0"}, "example.com/new": {"v2.0.0"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, u := range res.Updates {
				_, to := u.FromTo()
				got[u.Dep.Name] = string(u.Type) + " " + to
			}
			for name, w := range tt.updates {
				if got[name] != w {
					t.Errorf("%s: update %q, want %q", name, got[name], w)
				}
			}
			for name, g := range got {
				if _, ok := tt.updates[name]; !ok && strings.HasPrefix(g, "replacement") {
					t.Errorf("%s: unexpected %s", name, g)
				}
			}
			skipped := map[string]string{}
			for _, d := range res.Skipped {
				skipped[d.Name] = d.Skip
			}
			for name, w := range tt.skipped {
				if skipped[name] != w {
					t.Errorf("%s: skip %q, want %q", name, skipped[name], w)
				}
			}
			var looked []string
			for _, e := range res.Errors {
				var de *DepError
				name, ok := "", errors.As(e, &de)
				if ok {
					name, ok = strings.CutPrefix(de.Err.Error(), "releases listed for ")
				}
				if !ok {
					t.Errorf("resolve: %v", e)
				}
				looked = append(looked, name)
			}
			slices.Sort(looked)
			if !slices.Equal(looked, slices.Sorted(slices.Values(tt.looked))) {
				t.Errorf("looked up %v, want %v", looked, tt.looked)
			}
			if len(tt.want) == 0 {
				return
			}
			var ups []dep.Update
			for _, u := range res.Updates {
				if _, ok := tt.updates[u.Dep.Name]; ok {
					ups = append(ups, u)
				}
				if u.Dep.Name == "jade" && (len(u.Declined) != 1 || u.Declined[0].NewVersion != "1.12.0" || u.Declined[0].CommitType == "") {
					t.Errorf("jade: declined %+v, want the decorated minor update 1.12.0", u.Declined)
				}
			}
			changes, errs := Build(t.Context(), root, ms, ups, nil)
			for _, e := range errs {
				t.Errorf("build: %v", e)
			}
			content := map[string]string{}
			for _, c := range changes {
				content[c.Path] = string(c.New)
			}
			for _, w := range tt.want {
				if !slices.Contains(strings.Split(content[w.file], "\n"), w.line) {
					t.Errorf("%s: missing line %q in:\n%s", w.file, w.line, content[w.file])
				}
			}
		})
	}
}

// TestReplacementTitle names the old and the new package and version, on the dep's
// -replacement branch.
func TestReplacementTitle(t *testing.T) {
	u := dep.Update{Dep: dep.Dep{Manager: "npm", Name: "jade", Version: "1.11.0"}, Type: dep.Replacement, NewName: "pug", NewVersion: "2.0.0", Bucket: ReplacementBucket, CommitType: "fix", CommitScope: "deps"}
	g := GroupUpdates([]dep.Update{u}, "")
	if len(g) != 1 || g[0].Title != "fix(deps): replace jade with pug 2.0.0" || g[0].Branch != "deps/jade-replacement" {
		t.Errorf("groups = %+v", g)
	}
}
