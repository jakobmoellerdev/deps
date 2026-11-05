package renovate

import (
	"fmt"
	"strings"
)

// hbFields maps the Renovate template fields deps' PR templates have (prtemplate.Data).
var hbFields = map[string]string{
	"depName":        "{{.Name}}",
	"packageName":    "{{.Name}}",
	"newVersion":     "{{.To}}",
	"newValue":       "{{.To}}",
	"currentVersion": "{{.From}}",
	"currentValue":   "{{.From}}",
	"groupName":      "{{.Group}}",
	"updateType":     "{{.UpdateType}}",
}

// handlebars converts a Renovate (Handlebars) template into a deps Go text/template. Plain
// text passes (lowercased when lower is set, as Renovate lowercases semantic titles), field
// references {{f}} and {{{f}}} become the fields of hbFields or the Go templates of vars,
// comments are dropped and \{{ is a literal {{. Every other expression (helpers, block
// helpers, other fields) is an error.
func handlebars(src string, vars map[string]string, lower bool) (string, error) {
	var b strings.Builder
	// write appends s; a { before an action would merge with its delimiter.
	write := func(s string) {
		if strings.HasPrefix(s, "{{") {
			if out, ok := strings.CutSuffix(b.String(), "{"); ok {
				b.Reset()
				b.WriteString(out + `{{"{"}}`)
			}
		}
		b.WriteString(s)
	}
	text := func(s string) {
		if lower {
			s = strings.ToLower(s)
		}
		write(strings.ReplaceAll(s, "{{", `{{"{{"}}`))
	}
	for src != "" {
		i := strings.Index(src, "{{")
		if i < 0 {
			text(src)
			break
		}
		lit := src[:i]
		if strings.HasSuffix(lit, `\`) {
			if !strings.HasSuffix(lit, `\\`) {
				// \{{ is a literal {{.
				text(lit[:len(lit)-1] + "{{")
				src = src[i+2:]
				continue
			}
			lit = lit[:len(lit)-1] // \\{{x}} is a backslash and the expression
		}
		text(lit)
		src = src[i:]
		open, closing := "{{", "}}"
		switch {
		case strings.HasPrefix(src, "{{!--"):
			open, closing = "{{!--", "--}}"
		case strings.HasPrefix(src, "{{!"):
			open = "{{!"
		case strings.HasPrefix(src, "{{{"):
			open, closing = "{{{", "}}}"
		}
		j := strings.Index(src[len(open):], closing)
		if j < 0 {
			return "", fmt.Errorf("unclosed %s", open)
		}
		expr := src[:len(open)+j+len(closing)]
		inner := strings.TrimSpace(src[len(open) : len(open)+j])
		src = src[len(expr):]
		if strings.HasPrefix(open, "{{!") {
			continue
		}
		if g, ok := vars[inner]; ok {
			write(g)
		} else if g, ok := hbFields[inner]; ok {
			write(g)
		} else {
			return "", fmt.Errorf("unsupported Handlebars expression %s", expr)
		}
	}
	return b.String(), nil
}

// prText translates a prHeader or prFooter value at path; null is none ("").
func (t *translator) prText(path string, v any) (string, bool) {
	if v == nil {
		return "", true
	}
	s, ok := v.(string)
	if !ok {
		t.n.warn(path, "must be a string; ignored")
		return "", false
	}
	g, err := handlebars(s, nil, false)
	if err != nil {
		t.n.warn(path, "%v; dropped", err)
		return "", false
	}
	return g, true
}
