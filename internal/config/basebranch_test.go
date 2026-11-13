package config

import (
	"errors"
	"fmt"
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

func TestMergeBase(t *testing.T) {
	c, _, err := loadExtends(t, "extends: [o/shared]\nbaseBranches: [$default, release/1.x]\nuseBaseBranchConfig: merge\npin: digest\nrules:\n  - names: [a]\n    group: checkout\n",
		map[string]string{"o/shared//.deps.yaml": "rules:\n  - names: [s]\n    group: shared\n"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{files: map[string]string{"o/base//.deps.yaml": "rangeStrategy: bump\n"}}
	base := "extends: [o/base]\nbaseBranches: [other]\nuseBaseBranchConfig: none\npin: keep\nrules:\n  - names: [b]\n    group: release\n"
	m, err := c.MergeBase(t.Context(), []byte(base), "release/1.x:.deps.yaml", "release/1.x:.deps.yaml", f)
	if err != nil {
		t.Fatal(err)
	}
	if m.Pin != PinKeep || m.RangeStrategy != "bump" || c.Pin != PinDigest {
		t.Errorf("pin = %q, rangeStrategy = %q; the checkout's pin = %q", m.Pin, m.RangeStrategy, c.Pin)
	}
	if !slices.Equal(m.BaseBranches, c.BaseBranches) || m.UseBaseBranchConfig != BaseConfigMerge || !m.MultiBase() {
		t.Errorf("baseBranches = %q, useBaseBranchConfig = %q: the checkout's must stay", m.BaseBranches, m.UseBaseBranchConfig)
	}
	var groups, sources []string
	for i := range m.Rules {
		groups = append(groups, m.Rules[i].Group)
		file, line := m.Rules[i].Source()
		sources = append(sources, fmt.Sprintf("%s:%d", file, line))
	}
	if want := []string{"shared", "checkout", "release"}; !slices.Equal(groups, want) {
		t.Errorf("rule groups = %q, want %q", groups, want)
	}
	if want := []string{"o/shared//.deps.yaml:2", ".deps.yaml:6", "release/1.x:.deps.yaml:6"}; !slices.Equal(sources, want) {
		t.Errorf("rule sources = %q, want %q", sources, want)
	}
	if got := m.Files(); !slices.Equal(got, []string{".deps.yaml", "release/1.x:.deps.yaml"}) {
		t.Errorf("Files = %q", got)
	}
	if got := c.Files(); !slices.Equal(got, []string{".deps.yaml"}) {
		t.Errorf("checkout Files = %q", got)
	}
	if got := m.KeyFile("rangeStrategy"); got != "o/base//.deps.yaml" {
		t.Errorf("KeyFile(rangeStrategy) = %q", got)
	}
	if got := Default().Files(); got != nil {
		t.Errorf("default Files = %q", got)
	}
}

// A file both the checkout's and the base branch's config extend is read once, as within one
// chain, so its rules and custom managers appear once.
func TestMergeBaseSharedExtends(t *testing.T) {
	shared := "rules:\n  - names: [s]\n    group: shared\ncustom:\n  - files: [Makefile]\n"
	c, f, err := loadExtends(t, "extends: [o/shared]\nuseBaseBranchConfig: merge\nrules:\n  - names: [a]\n    group: checkout\n",
		map[string]string{"o/shared//.deps.yaml": shared})
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	base := "extends: [o/shared]\nrules:\n  - names: [b]\n    group: release\n"
	m, err := c.MergeBase(t.Context(), []byte(base), "release/1.x:.deps.yaml", "release/1.x:.deps.yaml", f)
	if err != nil {
		t.Fatal(err)
	}
	var groups []string
	for i := range m.Rules {
		groups = append(groups, m.Rules[i].Group)
	}
	if want := []string{"shared", "checkout", "release"}; !slices.Equal(groups, want) {
		t.Errorf("rule groups = %q, want %q", groups, want)
	}
	if len(m.Custom) != 1 {
		t.Errorf("custom managers = %d, want 1", len(m.Custom))
	}
	if len(f.calls) != 0 {
		t.Errorf("fetches during MergeBase = %q, want none: the checkout's chain already read the file", f.calls)
	}
	if got, want := m.Rules[0].Group, "shared"; got != want {
		t.Errorf("rules[0].group = %q, want %q", got, want)
	}
	file, _ := m.Rules[0].Source()
	if file != "o/shared//.deps.yaml" {
		t.Errorf("rules[0] source = %q", file)
	}
}

func TestMergeBaseErrors(t *testing.T) {
	c, err := load(t, ".deps.yaml", "useBaseBranchConfig: merge\ndashboard: {enabled: true}\nrules:\n  - names: [a]\n    dashboardApproval: true\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, base, want string }{
		{"syntax", "rules: [", "b:.deps.yaml:"},
		{"invalid value", "rangeStrategy: sideways\n", "b:.deps.yaml:1:16: rangeStrategy:"},
		// The base turns the dashboard off the checkout's rule needs: the problem is in the
		// checkout's file.
		{"problem in the checkout's file", "dashboard: {enabled: false}\n", `.deps.yaml:5:24: rules[0].dashboardApproval: requires dashboard.enabled: true`},
		{"extends without fetcher", "extends: [o/x]\n", "extends is not supported by this command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.MergeBase(t.Context(), []byte(tc.base), "b:.deps.yaml", "b:.deps.yaml", nil)
			var cerr *Error
			if !errors.As(err, &cerr) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want *Error containing %q", err, tc.want)
			}
		})
	}
}

func TestUseBaseBranchConfigValidation(t *testing.T) {
	for _, v := range BaseConfigs {
		if _, err := load(t, ".deps.yaml", "useBaseBranchConfig: "+v+"\n"); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	_, err := load(t, ".deps.yaml", "useBaseBranchConfig: always\n")
	if err == nil || !strings.Contains(err.Error(), "useBaseBranchConfig: ") || !strings.Contains(err.Error(), "merge-if-present") {
		t.Errorf("err = %v", err)
	}
}

func TestRead(t *testing.T) {
	f := &fakeFetcher{files: map[string]string{"o/x//.deps.yaml": "pin: digest\n"}}
	c, err := Read(t.Context(), []byte("extends: [o/x]\nuseBaseBranchConfig: merge\n"), "main:.deps.yaml", "main:.deps.yaml", f)
	if err != nil || c.Pin != PinDigest || c.UseBaseBranchConfig != BaseConfigMerge || c.Path() != "main:.deps.yaml" {
		t.Fatalf("Read = %+v, %v", c, err)
	}
	if _, err := Read(t.Context(), []byte("pin: x\n"), "main:.deps.yaml", "main:.deps.yaml", nil); err == nil || !strings.Contains(err.Error(), "main:.deps.yaml:1:6: pin:") {
		t.Errorf("err = %v", err)
	}
}
