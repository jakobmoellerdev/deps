package prtemplate

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name, src string
		title     bool
		err       string // substring of the error; "" = valid
	}{
		{name: "every field", src: "{{.Type}}({{.Scope}}) {{.Name}} {{.Group}} {{.From}} {{.To}} {{.UpdateType}} {{.Count}} {{.IsSecurity}}", title: true},
		{name: "conditions and functions", src: `{{.Type}}{{with .Scope}}({{.}}){{end}}: {{if eq .Count 1}}bump {{.Name}}{{else}}{{printf "%d deps" .Count}}{{end}}`, title: true},
		{name: "root variable", src: "{{.Name}}{{with .Scope}} {{$.Type}}({{.}}){{end}}", title: true},
		{name: "defined template", src: `{{define "n"}}{{.Name}}{{end}}x {{template "n" .}}`, title: true},
		{name: "unknown field", src: "{{.Type}}: {{.DepName}}", err: "line 1, column 14: unknown field .DepName (fields: .Type, .Scope, .Name, .Group, .From, .To, .UpdateType, .Count, .IsSecurity)"},
		{name: "unknown field in an untaken branch", src: "a\n{{if .IsSecurity}}{{else if false}}{{.Advisories}}{{end}}", err: "line 2, column 38: unknown field .Advisories"},
		{name: "unknown root variable field", src: "{{with .Scope}}{{$.Bogus}}{{end}}", err: "unknown field .Bogus"},
		{name: "unknown field in a defined template", src: `{{define "x"}}{{.Nope}}{{end}}`, err: "unknown field .Nope"},
		{name: "field of a field", src: "{{.Name.Length}}", err: "line 1, column 8: .Name has no field .Length"},
		{name: "syntax error", src: "x {{.Name", err: "line 1: unclosed action"},
		{name: "unknown function", src: "{{lower .Name}}", err: `line 1: function "lower" not defined`},
		{name: "execution error", src: "{{.Name}}\n{{slice .Name 99}}", err: "line 2, column 2: <slice .Name 99>: error calling slice"},
		{name: "empty title", src: "{{if false}}x{{end}}  \n", title: true, err: "renders an empty title"},
		{name: "empty header", src: "{{if false}}x{{end}}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parse := Parse
			if tt.title {
				parse = ParseTitle
			}
			tpl, err := parse(tt.src)
			switch {
			case tt.err == "" && err != nil:
				t.Fatalf("Parse(%q) = %v", tt.src, err)
			case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
				t.Fatalf("Parse(%q) = %v, want error containing %q", tt.src, err, tt.err)
			case tt.err == "" && tpl == nil, tt.err != "" && tpl != nil:
				t.Fatalf("Parse(%q) template = %v", tt.src, tpl)
			}
		})
	}
}

func TestExecute(t *testing.T) {
	d := Data{Type: "fix", Scope: "deps", Name: "a/x", From: "v1", To: "v2", UpdateType: "major", Count: 1}
	title, err := ParseTitle("  {{.Type}}({{.Scope}}):\n\tbump  {{.Name}} {{.From}} -> {{.To}} ({{.UpdateType}})  ")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := title.Execute(d); err != nil || got != "fix(deps): bump a/x v1 -> v2 (major)" {
		t.Errorf("title = %q, %v", got, err)
	}
	header, err := Parse("\n**{{.Name}}**\n\n{{if .IsSecurity}}security{{else}}regular{{end}} update of {{.Count}}\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := header.Execute(d); err != nil || got != "**a/x**\n\nregular update of 1" {
		t.Errorf("header = %q, %v", got, err)
	}
	if _, err := title.Execute(Data{}); err != nil {
		t.Errorf("title of empty data: %v", err)
	}
	blank, err := ParseTitle("{{if ne .Name \"\"}}{{.Name}}{{end}}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := blank.Execute(Data{}); err == nil || got != "" {
		t.Errorf("blank title = %q, %v", got, err)
	}
}
