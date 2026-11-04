package docgen

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ActionUsage is one usage snippet of the action reference: a workflow step using the action
// as Uses (e.g. owner/repo@v1), under a heading Title, with an optional note.
type ActionUsage struct {
	Title, Note, Uses string
}

// actionInput is one input of action.yml.
type actionInput struct {
	Name, Description, Default string
	Required                   bool
}

// Action renders the action reference from action.yml (src): the action's description and
// intro (markdown), its runtime, an inputs table and outputs, and one workflow-step snippet per
// usage that sets every input to its default. Input order is the file's order.
func Action(src []byte, intro string, usages []ActionUsage) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("action.yml: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("action.yml: not a mapping")
	}
	top := root.Content[0]
	name, desc := scalar(top, "name"), scalar(top, "description")
	if name == "" {
		return nil, errors.New("action.yml: no name")
	}
	inputs, err := namedEntries(get(top, "inputs"), "inputs")
	if err != nil {
		return nil, err
	}
	outputs, err := namedEntries(get(top, "outputs"), "outputs")
	if err != nil {
		return nil, err
	}
	d := newDoc("Action reference")
	d.para("`" + name + "`: " + desc + ".")
	d.para(intro)
	if runs := get(top, "runs"); runs != nil {
		using := scalar(runs, "using")
		var steps []string
		if s := get(runs, "steps"); s != nil && s.Kind == yaml.SequenceNode {
			for _, st := range s.Content {
				if n := scalar(st, "name"); n != "" {
					steps = append(steps, n)
				}
			}
		}
		line := "Runs as `" + using + "`"
		if len(steps) > 0 {
			line += " with the steps " + strings.Join(quoteAll(steps), ", ")
		}
		d.para(line + ".")
	}
	d.heading(2, "Inputs")
	if len(inputs) == 0 {
		d.para("None.")
	} else {
		rows := make([][]string, 0, len(inputs))
		for _, in := range inputs {
			req := "no"
			if in.Required {
				req = "yes"
			}
			def := "none"
			if in.Default != "" {
				def = codeSpan(in.Default)
			}
			rows = append(rows, []string{codeSpan(in.Name), req, def, in.Description})
		}
		d.table([]string{"Input", "Required", "Default", "Description"}, rows)
	}
	if len(outputs) > 0 {
		d.heading(2, "Outputs")
		rows := make([][]string, 0, len(outputs))
		for _, o := range outputs {
			rows = append(rows, []string{codeSpan(o.Name), o.Description})
		}
		d.table([]string{"Output", "Description"}, rows)
	}
	if len(usages) > 0 {
		d.heading(2, "Usage")
		for _, u := range usages {
			d.heading(3, u.Title)
			d.para(u.Note)
			d.code("yaml", step(u.Uses, inputs))
		}
	}
	return d.bytes()
}

// step is a workflow step using uses with every input set to its default (or a placeholder for
// required inputs without one), each preceded by the first sentence of its description.
func step(uses string, inputs []actionInput) string {
	var b strings.Builder
	b.WriteString("- uses: " + uses + "\n")
	if len(inputs) == 0 {
		return b.String()
	}
	b.WriteString("  with:\n")
	for _, in := range inputs {
		if c := firstSentence(in.Description); c != "" {
			b.WriteString("    # " + c + "\n")
		}
		v := in.Default
		if v == "" && in.Required {
			v = "<" + in.Name + ">"
		}
		b.WriteString("    " + in.Name + ": " + yamlString(v) + "\n")
	}
	return b.String()
}

// firstSentence returns s up to its first ". " (keeping the period) that does not end an
// abbreviation such as "e.g.", on one line.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for i := 0; ; {
		j := strings.Index(s[i:], ". ")
		if j < 0 {
			return s
		}
		end := i + j + 1
		if !strings.HasSuffix(s[:end], "e.g.") && !strings.HasSuffix(s[:end], "i.e.") {
			return s[:end]
		}
		i = end
	}
}

// yamlString quotes v unless YAML reads it back as the same plain string (not a bool, number,
// null, mapping or comment); values with YAML indicator characters are quoted for readability.
func yamlString(v string) string {
	var doc yaml.Node
	if !strings.ContainsAny(v, "{}[]\"'#:") && yaml.Unmarshal([]byte(v), &doc) == nil && len(doc.Content) == 1 {
		if n := doc.Content[0]; n.Kind == yaml.ScalarNode && n.Tag == "!!str" && n.Style == 0 && n.Value == v {
			return v
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func namedEntries(m *yaml.Node, field string) ([]actionInput, error) {
	if m == nil {
		return nil, nil
	}
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("action.yml: %s: not a mapping", field)
	}
	out := make([]actionInput, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		v := m.Content[i+1]
		out = append(out, actionInput{
			Name:        m.Content[i].Value,
			Description: strings.TrimSpace(scalar(v, "description")),
			Default:     scalar(v, "default"),
			Required:    scalar(v, "required") == "true",
		})
	}
	return out, nil
}

// get returns the value of key in mapping m, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalar returns the scalar value of key in mapping m, or "".
func scalar(m *yaml.Node, key string) string {
	if v := get(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = `"` + x + `"`
	}
	return out
}
