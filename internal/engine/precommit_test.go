package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/precommit"
)

// TestPreCommitRevs resolves pre-commit revs that are no plain tag: a bare SHA a tag points at
// is that tag's version and gets pre-commit's `# frozen:` comment, an untagged one is ignored
// by default as a github-actions commit pin is, and a SHA frozen at a tag that is no version
// (lychee's `# frozen: nightly`) follows that tag's commit.
func TestPreCommitRevs(t *testing.T) {
	const file = ".pre-commit-config.yaml"
	c := func(s string) string { return sha("lycheeverse/lychee@" + s) }
	ms := []manager.Manager{precommit.New()}
	tests := []struct {
		name, cfg, rev string
		tags           map[string]string
		want           []wantUpdate
		line, skip     string
	}{
		{
			name: "bare SHA at a tag", rev: c("v0.20.0"),
			tags: map[string]string{"v0.20.0": c("v0.20.0"), "v0.21.0": c("v0.21.0")},
			want: []wantUpdate{{typ: dep.Minor, version: "v0.21.0", digest: c("v0.21.0")}},
			line: "    rev: " + c("v0.21.0") + " # frozen: v0.21.0",
		},
		{name: "bare SHA without a tag", rev: c("main"), tags: map[string]string{"v0.21.0": c("v0.21.0")}, skip: config.UnversionedReason},
		{
			name: "frozen at a moved non-version tag", rev: c("nightly-old") + "  # frozen: nightly",
			tags: map[string]string{"nightly": c("nightly"), "v0.21.0": c("v0.21.0")},
			want: []wantUpdate{{typ: dep.Digest, version: "nightly", digest: c("nightly")}},
			line: "    rev: " + c("nightly") + "  # frozen: nightly",
		},
		{
			name: "frozen at a deleted non-version tag", rev: c("nightly-old") + "  # frozen: nightly",
			tags: map[string]string{"v0.21.0": c("v0.21.0")}, skip: "current version nightly not found upstream (pin an existing version)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "repos:\n  - repo: https://github.com/lycheeverse/lychee\n    rev: " + tt.rev + "\n    hooks:\n      - id: lychee\n"
			root, cfg := setup(t, tt.cfg, map[string]string{file: src})
			res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: ms, Datasources: map[string]Datasource{"git-tags": &fakeRepo{tags: tt.tags}}, Now: ageNow})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) > 0 {
				t.Fatal(res.Errors)
			}
			if got := gotUpdates(res); !slices.Equal(got, tt.want) {
				t.Errorf("updates = %+v, want %+v", got, tt.want)
			}
			var skips []string
			for _, d := range res.Skipped {
				skips = append(skips, d.Skip)
			}
			if (tt.skip == "") != (len(skips) == 0) || tt.skip != "" && skips[0] != tt.skip {
				t.Errorf("skipped = %q, want %q", skips, tt.skip)
			}
			if tt.line == "" {
				return
			}
			changes, errs := Build(t.Context(), root, ms, res.Updates[:1], nil)
			if len(errs) > 0 || len(changes) != 1 {
				t.Fatalf("changes = %+v, errors %v", changes, errs)
			}
			if got := line(3, string(changes[0].New)); got != tt.line {
				t.Errorf("line:\n got %s\nwant %s", got, tt.line)
			}
		})
	}
}

// TestPreCommitAdditionalDependencies updates the additional_dependencies of ruff's and
// airflow's hooks under the default rangeStrategy (no lockfile: an admitted version keeps the
// range) and `pin: digest`, which pins only the hook repository: npm, PyPI and Go packages
// have no digest.
func TestPreCommitAdditionalDependencies(t *testing.T) {
	const file = ".pre-commit-config.yaml"
	src := `repos:
  - repo: https://github.com/rhysd/actionlint
    rev: v1.7.7
    hooks:
      - id: actionlint
        language: golang
        additional_dependencies:
          - "github.com/wasilibs/go-shellcheck/cmd/shellcheck@v0.11.1"
  - repo: local
    hooks:
      - id: run-skill-eval
        language: node
        additional_dependencies: ['promptfoo@0.121.17', 'prettier@^3.6.2']
      - id: check
        language: python
        additional_dependencies: ['black==26.1.0', 'rich>=13.6.0']
`
	ds := map[string]Datasource{
		"git-tags": fakeTags{"https://github.com/rhysd/actionlint": {"v1.7.7"}},
		"go":       fakeGo{"github.com/wasilibs/go-shellcheck/cmd/shellcheck": {"v0.11.1", "v0.12.0"}},
		"npm":      staticReleases{"promptfoo": {"0.121.17", "0.122.0"}, "prettier": {"3.6.2", "3.7.0"}},
		"pypi":     staticReleases{"black": {"26.1.0", "26.3.1"}, "rich": {"13.6.0", "15.0.0"}},
	}
	ms := []manager.Manager{precommit.New()}
	root, cfg := setup(t, "pin: digest\n", map[string]string{file: src})
	res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: ms, Datasources: ds, Now: ageNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, u.Dep.DepType+" "+u.Dep.Name+" "+string(u.Type)+" "+u.NewVersion+" "+dep.ShortDigest(u.NewDigest))
	}
	want := []string{
		"repository rhysd/actionlint pin v1.7.7 " + dep.ShortDigest(sha("https://github.com/rhysd/actionlint@v1.7.7")),
		"pre-commit-golang github.com/wasilibs/go-shellcheck/cmd/shellcheck minor v0.12.0 ",
		"pre-commit-node promptfoo minor 0.122.0 ",
		"pre-commit-python black minor 26.3.1 ",
	}
	if !slices.Equal(got, want) {
		t.Errorf("updates:\n got %q\nwant %q", got, want)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 1 {
		t.Fatalf("changes = %+v, errors %v", changes, errs)
	}
	out := string(changes[0].New)
	for _, s := range []string{"shellcheck@v0.12.0", "'promptfoo@0.122.0', 'prettier@^3.6.2'", "'black==26.3.1', 'rich>=13.6.0'"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
}
