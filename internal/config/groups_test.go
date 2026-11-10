package config

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestGroupSettingsPolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `automerge: false
labels: [dependencies]
rules:
  - names: ['example.com/k8s/**']
    group: k8s
    automerge: false
    labels: [rule]
  - names: ['example.com/k8s/late']
    labels: [late]
    addLabels: [added]
    schedule: ['0 5 * * 1']
  - names: ['example.com/mini']
    updateTypes: [minor]
    group: mini
  - names: ['example.com/other']
    automerge: true
groups:
  k8s:
    automerge: true
    labels: [k8s]
  mini:
    prCreation: not-pending
  kubernetes monorepo:
    draftPR: true
`)
	if err != nil {
		t.Fatal(err)
	}
	gomod := func(name string) dep.Dep { return dep.Dep{Manager: "gomod", Name: name, DepType: "direct"} }
	for _, tc := range []struct {
		name      string
		d         dep.Dep
		kind      dep.UpdateType
		group     string
		settings  bool
		automerge bool
		labels    []string
		draft     bool
		creation  string
	}{
		// Precedence: top level < rules < group settings, also over later rules.
		{"group settings override the rule that set the group", gomod("example.com/k8s/a"), dep.Minor, "k8s", true, true, []string{"k8s"}, false, PRCreationImmediate},
		{"and later rules", gomod("example.com/k8s/late"), dep.Major, "k8s", true, true, []string{"k8s"}, false, PRCreationImmediate},
		{"pre-policy too", gomod("example.com/k8s/a"), "", "k8s", true, true, []string{"k8s"}, false, PRCreationImmediate},
		{"keys the entry leaves out keep the rules' value", gomod("example.com/mini"), dep.Minor, "mini", true, false, []string{"dependencies"}, false, PRCreationNotPending},
		{"an updateTypes group only for its types", gomod("example.com/mini"), dep.Major, "", false, false, []string{"dependencies"}, false, PRCreationImmediate},
		{"a built-in group", gomod("k8s.io/api"), dep.Patch, "kubernetes monorepo", true, false, []string{"dependencies"}, true, PRCreationImmediate},
		{"no group", gomod("example.com/other"), dep.Patch, "", false, true, []string{"dependencies"}, false, PRCreationImmediate},
	} {
		p := c.PolicyFor(tc.d, tc.kind)
		if tc.kind == "" {
			p = c.Policy(tc.d)
		}
		if p.Group != tc.group || p.GroupSettings != tc.settings || p.Automerge != tc.automerge || !slices.Equal(p.Labels, tc.labels) || p.DraftPR != tc.draft || p.PRCreation != tc.creation {
			t.Errorf("%s: group %q settings %v automerge %v labels %v draft %v prCreation %q, want %q %v %v %v %v %q", tc.name,
				p.Group, p.GroupSettings, p.Automerge, p.Labels, p.DraftPR, p.PRCreation, tc.group, tc.settings, tc.automerge, tc.labels, tc.draft, tc.creation)
		}
	}
	// A key the entry does not set stays the later rule's.
	if p := c.PolicyFor(gomod("example.com/k8s/late"), dep.Minor); p.Schedule == nil || p.Schedule.String() != "0 5 * * 1" {
		t.Errorf("schedule = %v, want the later rule's", p.Schedule)
	}
	g := c.Groups["k8s"]
	if file, line := g.Source(); file != ".deps.yaml" || line != 18 {
		t.Errorf("groups.k8s source = %s:%d, want .deps.yaml:18", file, line)
	}
}

func TestGroupSettingsLint(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - {managers: [gomod], group: go, securityGroup: sec}
groups:
  go: {automerge: true}
  sec: {automerge: true}
  typo: {automerge: true}
  kubernetes monorepo: {automerge: true}
  ruby: {automerge: true}
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Warning{
		{"groups.sec", `no rule sets group "sec", so its settings apply to nothing (a securityGroup names no groups entry: security updates follow the entry of their dep's group)`},
		{"groups.typo", `no rule sets group "typo", so its settings apply to nothing`},
	}
	if got := c.Lint(); !slices.Equal(got, want) {
		t.Errorf("Lint() =\n%v\nwant\n%v", got, want)
	}
	off, err := load(t, ".deps.yaml", "builtins: {monorepos: false}\ngroups: {ruby: {automerge: true}}\n")
	if err != nil {
		t.Fatal(err)
	}
	want = []Warning{{"groups.ruby", `no rule sets group "ruby" and the built-in groups are off (builtins.monorepos: false), so its settings apply to nothing`}}
	if got := off.Lint(); !slices.Equal(got, want) {
		t.Errorf("Lint() with builtins.monorepos: false =\n%v\nwant\n%v", got, want)
	}
}

func TestLoadExtendsGroups(t *testing.T) {
	base := `rules:
  - {managers: [gomod], group: go}
  - {managers: [npm], group: js}
groups:
  go:
    automerge: true
    labels: [go]
  js:
    schedule: ['0 5 * * 1']
`
	local := `extends: [org/cfg]
groups:
  go:
    labels: [golang]
`
	c, _, err := loadExtends(t, local, map[string]string{"org/cfg//.deps.yaml": base})
	if err != nil {
		t.Fatal(err)
	}
	// Merged by name: the local entry replaces the extended one whole; other entries stay.
	if g := c.Groups["go"]; g == nil || g.Automerge != nil || !slices.Equal(g.Labels, []string{"golang"}) {
		t.Errorf("groups.go = %+v, want the local entry", g)
	}
	if file, line := c.Groups["go"].Source(); file != ".deps.yaml" || line != 3 {
		t.Errorf("groups.go source = %s:%d", file, line)
	}
	js := c.Groups["js"]
	if js == nil || !slices.Equal(js.Schedule, []string{"0 5 * * 1"}) {
		t.Fatalf("groups.js = %+v, want the extended entry", js)
	}
	if file, line := js.Source(); file != "org/cfg//.deps.yaml" || line != 8 {
		t.Errorf("groups.js source = %s:%d", file, line)
	}
	if p := c.PolicyFor(dep.Dep{Manager: "npm", Name: "x"}, dep.Minor); !p.GroupSettings || p.Schedule == nil {
		t.Errorf("js policy: settings %v schedule %v", p.GroupSettings, p.Schedule)
	}
	// A problem in an extended entry is reported at the extends entry it came through.
	_, _, err = loadExtends(t, local, map[string]string{"org/cfg//.deps.yaml": "groups: {js: {pin: digest}}\n"})
	if err == nil || !strings.HasSuffix(err.Error(), `.deps.yaml:1:11: org/cfg//.deps.yaml:1:20: groups.js.pin: cannot be set for a group: the pin mode is decided before the group is known; set it in a rule`) {
		t.Errorf("err = %v", err)
	}
}
