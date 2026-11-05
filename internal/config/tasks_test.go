package config

import (
	"reflect"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestPolicyPostUpgradeTasks(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - managers: [gomod]
    postUpgradeTasks: ["go generate ./...", "make fmt"]
  - names: ["golang.org/x/*"]
    postUpgradeTasks: []
  - names: ["example.com/*"]
    postUpgradeTasks: {commands: ["make gen"], executionMode: update, files: ["gen/**", "!**/*.bak"]}
  - names: ["example.com/b*"]
    postUpgradeTasks: {commands: ["make b"]}
`)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		d    dep.Dep
		want dep.Tasks
	}{
		"list":             {dep.Dep{Manager: "gomod", Name: "a.b/c"}, dep.Tasks{Commands: []string{"go generate ./...", "make fmt"}}},
		"[] clears":        {dep.Dep{Manager: "gomod", Name: "golang.org/x/net"}, dep.Tasks{Commands: []string{}}},
		"update mode":      {dep.Dep{Manager: "gomod", Name: "example.com/m"}, dep.Tasks{Commands: []string{"make gen"}, Update: true, Files: []string{"gen/**", "!**/*.bak"}}},
		"branch default":   {dep.Dep{Manager: "gomod", Name: "example.com/b"}, dep.Tasks{Commands: []string{"make b"}}},
		"no matching rule": {dep.Dep{Manager: "npm", Name: "left-pad"}, dep.Tasks{}},
	} {
		if got := c.PolicyFor(tc.d, dep.Minor).Tasks; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: tasks %+v, want %+v", name, got, tc.want)
		}
	}
}

func TestValidatePostUpgradeTasks(t *testing.T) {
	for body, want := range map[string]string{
		"rules:\n  - postUpgradeTasks: ['make', ' ']\n":                             "rules[0].postUpgradeTasks[1]: must be a non-blank command",
		"rules:\n  - postUpgradeTasks: {commands: [''], executionMode: update}\n":   "rules[0].postUpgradeTasks.commands[0]: must be a non-blank command",
		"rules:\n  - postUpgradeTasks: {commands: [make], executionMode: always}\n": `rules[0].postUpgradeTasks.executionMode: must be one of branch, update, got "always"`,
		"rules:\n  - postUpgradeTasks: {commands: [make], fileFilters: ['*.go']}\n": "rules[0].postUpgradeTasks.fileFilters: unknown key (Renovate's fileFilters is files here; doublestar globs, not minimatch)",
		"rules:\n  - postUpgradeTasks: {commands: [make], files: [a, '!']}\n":       `rules[0].postUpgradeTasks.files[1]: invalid glob "!": missing pattern after !`,
	} {
		if _, err := load(t, ".deps.yaml", body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}
