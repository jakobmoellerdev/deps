package config

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestRuleBaseBranches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  string
		base  string
		match bool
	}{
		{"name", "baseBranches: [main]", "main", true},
		{"other name", "baseBranches: [main]", "release/1.x", false},
		{"regex", "baseBranches: ['/^release\\//']", "release/1.x", true},
		{"regex no match", "baseBranches: ['/^release\\//']", "main", false},
		{"regex flag i", "baseBranches: ['/^RELEASE/i']", "release/1.x", true},
		{"exclusion only", "baseBranches: ['!main']", "release/1.x", true},
		{"exclusion only: excluded", "baseBranches: ['!main']", "main", false},
		{"regex exclusion wins", "baseBranches: ['/.*/', '!/^release/']", "release/1.x", false},
		{"no base branch", "baseBranches: [main]", "", false},
		{"exclusion only without base branch", "baseBranches: ['!main']", "", false},
		{"without baseBranches", "names: ['**']", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, ".deps.yaml", "rules: [{"+tc.rule+", group: g}]\n")
			if err != nil {
				t.Fatal(err)
			}
			if tc.base != "" {
				c = c.ForBaseBranch(tc.base)
			}
			if got := c.Policy(dep.Dep{Name: "x"}).Group == "g"; got != tc.match {
				t.Errorf("match = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestForBaseBranchKeepsConfig(t *testing.T) {
	c, err := load(t, ".deps.yaml", "rules: [{baseBranches: [main], group: g}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if b := c.ForBaseBranch("main"); b == c || c.Policy(dep.Dep{Name: "x"}).Group != "" || b.Policy(dep.Dep{Name: "x"}).Group != "g" {
		t.Error("ForBaseBranch must return a copy and leave c without a base branch")
	}
}

func TestExpandBaseBranches(t *testing.T) {
	remote := []string{"main", "release/1.x", "release/2.x", "feature"}
	for _, tc := range []struct {
		name  string
		cfg   string
		want  []string
		multi bool
	}{
		{"none", "", []string{"main"}, false},
		{"one name", "baseBranches: [release/1.x]", []string{"release/1.x"}, false},
		{"default", "baseBranches: [$default]", []string{"main"}, false},
		{"names", "baseBranches: [$default, release/2.x]", []string{"main", "release/2.x"}, true},
		{"regex alone is multi", "baseBranches: ['/^release\\//']", []string{"release/1.x", "release/2.x"}, true},
		{"deduplicated", "baseBranches: [main, $default, '/^(main|feature)$/']", []string{"main", "feature"}, true},
		{"missing name kept", "baseBranches: [main, gone]", []string{"main", "gone"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, ".deps.yaml", tc.cfg+"\n")
			if err != nil {
				t.Fatal(err)
			}
			if got := c.ExpandBaseBranches("main", remote); !slices.Equal(got, tc.want) {
				t.Errorf("ExpandBaseBranches = %q, want %q", got, tc.want)
			}
			if got := c.MultiBase(); got != tc.multi {
				t.Errorf("MultiBase = %v, want %v", got, tc.multi)
			}
		})
	}
}

func TestBaseBranchesValidation(t *testing.T) {
	for _, tc := range []struct {
		cfg, want string
	}{
		{"baseBranches: ['/[/']", `baseBranches[0]: invalid regex "/[/"`},
		{"baseBranches: ['/x/g']", `baseBranches[0]: invalid regex "/x/g": unknown flags "g" (only i)`},
		{"baseBranches: ['a b']", `baseBranches[0]: must be a branch name, $default or a regex /<RE2>/, got "a b"`},
		{"baseBranches: ['!main']", `baseBranches[0]: must be a branch name, $default or a regex /<RE2>/, got "!main"`},
		{"baseBranches: ['']", `baseBranches[0]: must be a branch name, $default or a regex /<RE2>/, got ""`},
		{"rules: [{baseBranches: ['!'], group: g}]", `rules[0].baseBranches[0]: must be a branch name or a regex /<RE2>/, got "!"`},
		{"rules: [{baseBranches: ['!/(/'], group: g}]", `rules[0].baseBranches[0]: invalid regex "!/(/"`},
	} {
		t.Run(tc.cfg, func(t *testing.T) {
			_, err := load(t, ".deps.yaml", tc.cfg+"\n")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
