package config

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestExpandBranchPrefix(t *testing.T) {
	root := dep.Dep{Manager: "github-actions", File: "action.yml"}
	nested := dep.Dep{Manager: "npm", File: "packages/web app/package.json"}
	for _, tc := range []struct {
		tpl  string
		d    dep.Dep
		want string
	}{
		{"", nested, ""},
		{"tools-", nested, "tools-"},
		{"{{manager}}/", root, "github-actions/"},
		{"{{{manager}}}-", root, "github-actions-"},
		{"{{ manager }}/", root, "github-actions/"},
		{"{{parentDir}}-", nested, "webapp-"},
		{"{{packageFileDir}}/", nested, "packages/webapp/"},
		// At the root both directories are empty: no leading '-' or '/' is left.
		{"{{parentDir}}-", root, ""},
		{"{{packageFileDir}}/", root, ""},
		{"{{parentDir}}/", dep.Dep{File: "a~b:c/x.json"}, "a-b-c/"},
		{"{{manager}}/{{packageFileDir}}/", root, "github-actions/"},
	} {
		if got := ExpandBranchPrefix(tc.tpl, tc.d); got != tc.want {
			t.Errorf("ExpandBranchPrefix(%q, %s) = %q, want %q", tc.tpl, tc.d.File, got, tc.want)
		}
	}
}
