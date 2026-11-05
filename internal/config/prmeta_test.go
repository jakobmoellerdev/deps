package config

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestPolicyPRMetadata(t *testing.T) {
	c, err := load(t, ".deps.yaml", `labels: [dependencies]
reviewers: [alice, team:platform]
assignees: [bob]
rules:
  - manager: gomod
    addLabels: [go, dependencies]
  - names: ["golang.org/x/*"]
    labels: [x]
    reviewers: []
  - manager: gomod
    updateTypes: [major]
    addLabels: [major]
    assignees: [carol]
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                         string
		d                            dep.Dep
		kind                         dep.UpdateType
		labels, reviewers, assignees []string
	}{
		{"top level", dep.Dep{Manager: "npm", Name: "left-pad"}, dep.Minor, []string{"dependencies"}, []string{"alice", "team:platform"}, []string{"bob"}},
		{"addLabels appends once", dep.Dep{Manager: "gomod", Name: "a.b/c"}, dep.Minor, []string{"dependencies", "go"}, []string{"alice", "team:platform"}, []string{"bob"}},
		{"labels and [] replace", dep.Dep{Manager: "gomod", Name: "golang.org/x/net"}, dep.Minor, []string{"x"}, []string{}, []string{"bob"}},
		{"updateTypes rule", dep.Dep{Manager: "gomod", Name: "golang.org/x/net"}, dep.Major, []string{"x", "major"}, []string{}, []string{"carol"}},
	} {
		p := c.PolicyFor(tc.d, tc.kind)
		if !slices.Equal(p.Labels, tc.labels) || !slices.Equal(p.Reviewers, tc.reviewers) || !slices.Equal(p.Assignees, tc.assignees) {
			t.Errorf("%s: labels %q reviewers %q assignees %q", tc.name, p.Labels, p.Reviewers, p.Assignees)
		}
	}
	// addLabels must not write into the top-level list shared by every policy.
	if !slices.Equal(c.Labels, []string{"dependencies"}) {
		t.Errorf("top-level labels changed: %q", c.Labels)
	}
}

func TestValidatePRMetadata(t *testing.T) {
	_, err := load(t, ".deps.yaml", "labels: [' ']\nreviewers: ['team:', 'a b']\nrules:\n  - addLabels: ['a,b']\n    assignees: ['@x']\n")
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		`labels[0]: must be a non-blank label name without ',', got " "`,
		`reviewers[0]: must be a user name or team:<slug>, got "team:"`,
		`reviewers[1]: must be a user name or team:<slug>, got "a b"`,
		`rules[0].addLabels[0]: must be a non-blank label name without ',', got "a,b"`,
		`rules[0].assignees[0]: must be a user name, got "@x"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("errors lack %q:\n%v", want, err)
		}
	}
}
