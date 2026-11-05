package renovate

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

func TestLoadReplacements(t *testing.T) {
	nuget, _, _ := strings.Cut(config.ReplacementsSkipped[0], "[") // the nuget preset deps cannot apply
	const allOff = `  - # replacements:clang-format-to-maintenance-fork (not in replacements:all)
    manager: npm
    names: [clang-format]
    updateTypes: [replacement]
    ignore: true
  - # replacements:corentinmusard-otel-cicd-action-to-dash0hq (not in replacements:all)
    manager: github-actions
    names: [corentinmusard/otel-cicd-action]
    updateTypes: [replacement]
    ignore: true`
	allNotes := "info replacements:all: deps' built-in replacements cover it (replacements: true)" +
		"|info replacements:all: deps cannot apply " + config.ReplacementsSkipped[0] +
		"|info replacements:all: replacements:all does not extend clang-format-to-maintenance-fork from deps' built-in list; switched off by a rule" +
		"|info replacements:all: replacements:all does not extend corentinmusard-otel-cicd-action-to-dash0hq from deps' built-in list; switched off by a rule"
	tests := []struct {
		name, src string
		on        bool
		rules     string
		prefix    bool   // rules are only the first translated rules
		notes     string // the replacement notes, joined by |
	}{
		{
			name:  "no replacements preset switches the built-in list off",
			src:   `{}`,
			rules: strings.TrimPrefix(preCommitExclusion, "\n"),
		},
		{
			name:  "replacements:all keeps the built-in list",
			src:   `{extends: ['replacements:all']}`,
			on:    true,
			rules: allOff + preCommitExclusion,
			notes: allNotes,
		},
		{
			name:   "config:recommended extends replacements:all",
			src:    `{extends: ['config:recommended']}`,
			on:     true,
			rules:  allOff,
			prefix: true,
			notes:  allNotes,
		},
		{
			name: "a preset outside replacements:all with replacements:all is covered",
			src:  `{extends: ['replacements:all', 'replacements:clang-format-to-maintenance-fork', 'replacements:k8s-registry-move']}`,
			on:   true,
			rules: `  - # replacements:corentinmusard-otel-cicd-action-to-dash0hq (not in replacements:all)
    manager: github-actions
    names: [corentinmusard/otel-cicd-action]
    updateTypes: [replacement]
    ignore: true` + preCommitExclusion,
			notes: "info replacements:all: deps' built-in replacements cover it (replacements: true)" +
				"|info replacements:all: deps cannot apply " + config.ReplacementsSkipped[0] +
				"|info replacements:clang-format-to-maintenance-fork: deps' built-in replacements cover it (replacements: true)" +
				"|info replacements:k8s-registry-move: deps' built-in replacements cover it (replacements: true)" +
				"|info replacements:all: replacements:all does not extend corentinmusard-otel-cicd-action-to-dash0hq from deps' built-in list; switched off by a rule",
		},
		{
			name: "single presets become rules",
			src:  `{extends: ['replacements:k8s-registry-move', 'replacements:cpx-to-maintenance-fork', 'replacements:` + nuget + `', 'replacements:nope']}`,
			rules: `  - # replacements:k8s-registry-move
    manager: docker
    names: [/^k8s\.gcr\.io/.+$/]
    replacementName: registry.k8s.io/
    replacementPrefix: k8s.gcr.io/
  - # replacements:k8s-registry-move
    manager: gitlabci
    names: [/^k8s\.gcr\.io/.+$/]
    replacementName: registry.k8s.io/
    replacementPrefix: k8s.gcr.io/
  - # replacements:k8s-registry-move
    manager: kustomize
    names: [/^k8s\.gcr\.io/.+$/]
    replacementName: registry.k8s.io/
    replacementPrefix: k8s.gcr.io/
  - # replacements:k8s-registry-move
    manager: terraform
    names: [/^k8s\.gcr\.io/.+$/]
    replacementName: registry.k8s.io/
    replacementPrefix: k8s.gcr.io/
  - # replacements:k8s-registry-move
    manager: devcontainer
    names: [/^k8s\.gcr\.io/.+$/]
    replacementName: registry.k8s.io/
    replacementPrefix: k8s.gcr.io/
  - # replacements:cpx-to-maintenance-fork
    manager: npm
    names: [cpx]
    replacementName: cpx2
    replacementVersion: 2.0.0` + preCommitExclusion,
			notes: "info replacements:k8s-registry-move: translated into rules from deps' built-in replacements" +
				"|info replacements:cpx-to-maintenance-fork: translated into rules from deps' built-in replacements" +
				"|warn replacements:" + nuget + ": deps cannot apply " + config.ReplacementsSkipped[0] + "; skipped" +
				"|warn replacements:nope: not in deps' built-in replacements; skipped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := load(t, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			want := "\nreplacements: false\n"
			if tt.on {
				want = "\nreplacements: true\n"
			}
			if !strings.Contains(string(res.YAML), want) {
				t.Errorf("want %q in\n%s", want, res.YAML)
			}
			if got := section(t, res, "rules"); got != tt.rules && (!tt.prefix || !strings.HasPrefix(got, tt.rules+"\n")) {
				t.Errorf("rules:\n%s\nwant:\n%s", got, tt.rules)
			}
			var got []string
			for _, n := range noteLines(res) {
				if strings.Contains(n, "replacements") {
					got = append(got, n)
				}
			}
			var wantNotes []string
			if tt.notes != "" {
				wantNotes = strings.Split(tt.notes, "|")
			}
			if !slices.Equal(got, wantNotes) {
				t.Errorf("notes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantNotes, "\n"))
			}
		})
	}
}
