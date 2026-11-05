// Package prtemplate parses and renders the PR templates of .deps.yaml (prTitle, prHeader and
// prFooter): Go text/template over Data. Parse checks every field a template references
// against Data and executes it on sample data, so a template that loads renders.
package prtemplate

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
)

// Data is what a PR template sees of the updates of one PR.
type Data struct {
	Type       string // conventional commit type of the title: fix, chore, ...; fix for security updates
	Scope      string // conventional commit scope, "" = none
	Name       string // the dependency, or the group name when the PR updates several ("lock file maintenance" for that PR)
	Group      string // policy group (or shared version definition) of the PR, "" = none
	From       string // current version of every update (the locked one for lockfile-only updates), "" when they differ
	To         string // new version of every update, "" when they differ
	UpdateType string // major, minor, patch, digest, pin or lockFileMaintenance when every update has it, "" otherwise
	Count      int    // number of dependencies (lockfiles for lock file maintenance)
	IsSecurity bool   // some update fixes advisories
}

// Fields are the names of the fields of Data, in declaration order.
var Fields = func() []string {
	t := reflect.TypeFor[Data]()
	out := make([]string, t.NumField())
	for i := range out {
		out[i] = t.Field(i).Name
	}
	return out
}()

// samples are executed by Parse: a single dependency and a security group without shared
// versions, so both sides of the common conditions run.
var samples = []Data{
	{Type: "chore", Scope: "deps", Name: "example/dep", From: "1.0.0", To: "1.1.0", UpdateType: "minor", Count: 1},
	{Type: "fix", Name: "example", Group: "example", Count: 2, IsSecurity: true},
}

// Template is a parsed PR template.
type Template struct {
	t     *template.Template
	title bool
}

// Set holds the templates of a PR; a nil member renders the default title, or no header
// or footer.
type Set struct{ Title, Header, Footer *Template }

// Parse parses the header or footer template src.
func Parse(src string) (*Template, error) { return parseTemplate(src, false) }

// ParseTitle parses the title template src; on the sample data it must render a non-blank
// title.
func ParseTitle(src string) (*Template, error) { return parseTemplate(src, true) }

func parseTemplate(src string, title bool) (*Template, error) {
	t, err := template.New(name).Parse(src)
	if err != nil {
		return nil, tidy(err)
	}
	for _, tt := range t.Templates() {
		if err := checkFields(src, tt.Root); err != nil {
			return nil, err
		}
	}
	p := &Template{t: t, title: title}
	for _, d := range samples {
		if _, err := p.Execute(d); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// name is the name of every parsed template; tidy strips it from errors.
const name = "template"

// Execute renders t for d. A title is one line: runs of white space, newlines included,
// become one space; it must not be blank. A header or footer is trimmed.
func (t *Template) Execute(d Data) (string, error) {
	var b strings.Builder
	if err := t.t.Execute(&b, d); err != nil {
		return "", tidy(err)
	}
	if !t.title {
		return strings.TrimSpace(b.String()), nil
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	if s == "" {
		return "", errors.New("renders an empty title")
	}
	return s, nil
}

// errRe matches the errors of text/template: "template: <name>:<line>[:<col>]: [executing
// "<name>" at <<node>>: ]<msg>".
var errRe = regexp.MustCompile(`^template: ` + name + `:(\d+)(?::(\d+))?: (?:executing "[^"]*" at (<[^>]*>): )?(.*)$`)

// tidy rewrites a text/template error as "line <n>[, column <c>]: [<node>: ]<msg>".
func tidy(err error) error {
	m := errRe.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	loc := "line " + m[1]
	if m[2] != "" {
		loc += ", column " + m[2]
	}
	if m[3] != "" {
		loc += ": " + m[3]
	}
	return fmt.Errorf("%s: %s", loc, m[4])
}

// checkFields reports the first field reference below n that names no field of Data (also
// in branches the samples do not take). Every field of Data is a scalar, so a reference to a
// field of a field is reported too.
func checkFields(src string, n parse.Node) error {
	var err error
	check := func(n parse.Node, idents []string) {
		if err != nil || len(idents) == 0 {
			return
		}
		if !slices.Contains(Fields, idents[0]) {
			err = fmt.Errorf("%s: unknown field .%s (fields: .%s)", position(src, n), idents[0], strings.Join(Fields, ", ."))
		} else if len(idents) > 1 {
			err = fmt.Errorf("%s: .%s has no field .%s", position(src, n), idents[0], idents[1])
		}
	}
	var walk func(parse.Node)
	walk = func(n parse.Node) {
		if err != nil || n == nil || reflect.ValueOf(n).IsNil() {
			return
		}
		switch n := n.(type) {
		case *parse.ListNode:
			for _, c := range n.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.IfNode:
			walkBranch(walk, &n.BranchNode)
		case *parse.RangeNode:
			walkBranch(walk, &n.BranchNode)
		case *parse.WithNode:
			walkBranch(walk, &n.BranchNode)
		case *parse.TemplateNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			for _, c := range n.Cmds {
				walk(c)
			}
		case *parse.CommandNode:
			for _, a := range n.Args {
				walk(a)
			}
		case *parse.ChainNode:
			walk(n.Node)
		case *parse.FieldNode:
			check(n, n.Ident)
		case *parse.VariableNode:
			if n.Ident[0] == "$" {
				check(n, n.Ident[1:])
			}
		}
	}
	walk(n)
	return err
}

func walkBranch(walk func(parse.Node), b *parse.BranchNode) {
	walk(b.Pipe)
	walk(b.List)
	walk(b.ElseList)
}

// position renders the line and column of n in src.
func position(src string, n parse.Node) string {
	pos := min(int(n.Position()), len(src))
	line := 1 + strings.Count(src[:pos], "\n")
	col := pos - strings.LastIndexByte(src[:pos], '\n')
	return "line " + strconv.Itoa(line) + ", column " + strconv.Itoa(col)
}
