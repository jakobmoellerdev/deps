package config

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
)

func TestPRTemplates(t *testing.T) {
	c, err := load(t, ".deps.yaml", `prTitle: '{{.Type}}: bump {{.Name}} to {{.To}}'
prHeader: 'Header of {{.Name}}'
prFooter: |
  Footer
  {{- if .IsSecurity}} (security){{end}}
rules:
  - manager: docker
    prTitle: 'docker: {{.Name}}'
    prFooter: ''
  - manager: docker
    updateTypes: [major]
    prHeader: 'Major {{.From}} -> {{.To}}'
  - names: [plain]
    prTitle: ''
    prHeader: ''
`)
	if err != nil {
		t.Fatal(err)
	}
	data := prtemplate.Data{Type: "chore", Name: "x", From: "1", To: "2"}
	render := func(tpl *prtemplate.Template) string {
		if tpl == nil {
			return "<nil>"
		}
		s, err := tpl.Execute(data)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct {
		name                  string
		p                     Policy
		title, header, footer string
	}{
		{"top level", c.Policy(dep.Dep{Manager: "gomod", Name: "x"}), "chore: bump x to 2", "Header of x", "Footer"},
		{"rule title, footer reset", c.Policy(dep.Dep{Manager: "docker", Name: "x"}), "docker: x", "Header of x", "<nil>"},
		{"updateTypes rule", c.PolicyFor(dep.Dep{Manager: "docker", Name: "x"}, dep.Major), "docker: x", "Major 1 -> 2", "<nil>"},
		{"reset to the defaults", c.Policy(dep.Dep{Manager: "gomod", Name: "plain"}), "<nil>", "<nil>", "Footer"},
		{"lock file maintenance", Policy{Templates: c.PRTemplates()}, "chore: bump x to 2", "Header of x", "Footer"},
	} {
		got := []string{render(tc.p.Templates.Title), render(tc.p.Templates.Header), render(tc.p.Templates.Footer)}
		if want := []string{tc.title, tc.header, tc.footer}; !slices.Equal(got, want) {
			t.Errorf("%s: templates render %q, want %q", tc.name, got, want)
		}
	}
	if d := Default(); d.PRTemplates() != (prtemplate.Set{}) {
		t.Errorf("default templates = %+v", d.PRTemplates())
	}
}

// Invalid templates fail the load at their line, with the line and column in the template.
func TestPRTemplateErrors(t *testing.T) {
	_, err := load(t, ".deps.yaml", `prTitle: '{{.Type}} {{.DepName}}'
prHeader: '{{.Name'
rules:
  - manager: gomod
    prFooter: |
      ok
      {{.Count.Max}}
  - manager: npm
    prTitle: '{{if false}}x{{end}}'
`)
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{
		".deps.yaml: prTitle: line 1, column 13: unknown field .DepName (fields: .Type, .Scope, .Name, .Group, .From, .To, .UpdateType, .Count, .IsSecurity)",
		".deps.yaml: prHeader: line 1: unclosed action",
		".deps.yaml: rules[0].prFooter: line 2, column 9: .Count has no field .Max",
		".deps.yaml: rules[1].prTitle: renders an empty title",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	lines := make([]int, len(ce.Problems))
	for i, p := range ce.Problems {
		lines[i] = p.Line
	}
	if !slices.Equal(lines, []int{1, 2, 5, 9}) {
		t.Errorf("problem lines = %v, want [1 2 5 9] (%+v)", lines, ce.Problems)
	}
}
