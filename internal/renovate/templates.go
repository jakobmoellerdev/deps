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

// titleKeys are the Renovate settings a PR title is composed of (generate.ts
// compileCommitMessage and compilePrTitle).
var titleKeys = map[string]bool{
	"prTitle": true, "commitMessage": true, "commitMessagePrefix": true, "commitMessageAction": true,
	"commitMessageTopic": true, "commitMessageExtra": true, "commitMessageSuffix": true,
	"commitMessageLowerCase": true, "semanticCommits": true,
}

// semanticPrefix is Renovate's semantic commit prefix "<type>(<scope>):" over deps' commit
// type and scope.
const semanticPrefix = "{{.Type}}{{with .Scope}}({{.}}){{end}}:"

// titlePart is a title setting in effect at a rule: the rule's own, or the top level's.
type titlePart struct {
	s        string
	set, own bool // own: set by the rule itself, not at (or inherited from) the top level
	path     string
}

// title composes the deps prTitle of Renovate's title settings own of the rule at origin
// ("" = the top level), over the top-level ones: prTitle, else the commit message
// '{{{commitMessagePrefix}}} {{{commitMessageAction}}} {{{commitMessageTopic}}}
// {{{commitMessageExtra}}} {{{commitMessageSuffix}}}' with Renovate's defaults for unset
// parts. semType is the rule's semanticCommitType (nil = the top level's). ok is false when
// own sets no title, or a part has no deps equivalent (warned); deps then keeps its titles.
func (t *translator) title(origin string, own map[string]any, semType any) (string, bool) {
	triggers := false
	for k, v := range own {
		triggers = triggers || k != "semanticCommits" || v == "disabled"
	}
	at := func(k string) string {
		if origin == "" {
			return k
		}
		return origin + "." + k
	}
	if v, ok := own["semanticCommits"]; ok && v != "disabled" {
		t.n.info(at("semanticCommits"), "deps always writes conventional commit messages")
	}
	if !triggers {
		return "", false
	}
	part := func(k string) titlePart {
		v, isOwn := own[k]
		src := own
		if !isOwn && origin != "" {
			v, src = t.titleTop[k], t.titleTop
		}
		if _, ok := src[k]; !ok {
			return titlePart{}
		}
		p := titlePart{set: true, own: isOwn && origin != "", path: at(k)}
		if !isOwn {
			p.path = k
		}
		switch v := v.(type) {
		case nil:
		case string:
			p.s = v
		default:
			t.n.warn(p.path, "must be a string; ignored")
			return titlePart{}
		}
		return p
	}
	semantic := part("semanticCommits").s != "disabled"
	prefix := part("commitMessagePrefix")
	if semType == nil {
		semType = t.semType
	}
	typ, _ := semType.(string)
	if semType == nil {
		typ = "chore"
	}
	// Renovate lowercases semantic titles of lowercase commit types.
	lower := semantic && prefix.s == "" && strings.ToLower(typ) == typ && !strings.HasPrefix(typ, ":") &&
		part("commitMessageLowerCase").s != "never"
	lc := func(s string) string {
		if lower {
			return strings.ToLower(s)
		}
		return s
	}
	vars := map[string]string{}
	failed := false
	conv := func(p titlePart) string {
		g, err := handlebars(p.s, vars, lower)
		if err != nil {
			t.n.warn(p.path, "%v; deps keeps its built-in PR titles", err)
			failed = true
		}
		return g
	}
	// orDefault converts p, or returns def when p is unset. Renovate's update-type defaults
	// (pin, digest) override the top level's settings, not a rule's.
	orDefault := func(p titlePart, def, updateType, typeDef string) string {
		s := def
		if p.set {
			s = conv(p)
		}
		if updateType != "" && !p.own {
			s = `{{if eq .UpdateType "` + updateType + `"}}` + typeDef + "{{else}}" + s + "{{end}}"
		}
		return s
	}
	switch {
	case prefix.s != "":
		vars["commitMessagePrefix"] = conv(prefix)
	case semantic:
		vars["commitMessagePrefix"] = semanticPrefix
	default:
		vars["commitMessagePrefix"] = ""
	}
	vars["commitMessageAction"] = orDefault(part("commitMessageAction"), lc("Update"), "pin", lc("Pin"))
	// The group topic overrides every other (Renovate's group config) when the PR has several
	// dependencies.
	vars["commitMessageTopic"] = "{{if and .Group (gt .Count 1)}}{{.Group}}{{else}}" +
		orDefault(part("commitMessageTopic"), lc("dependency ")+"{{.Name}}", "digest", "{{.Name}}"+lc(" digest")) + "{{end}}"
	// Renovate drops the extra of groups whose versions differ (deps: .To is empty).
	if extra := orDefault(part("commitMessageExtra"), lc("to ")+"{{.To}}", "", ""); extra != "" {
		vars["commitMessageExtra"] = "{{if .To}}" + extra + "{{end}}"
	} else {
		vars["commitMessageExtra"] = ""
	}
	vars["commitMessageSuffix"] = orDefault(part("commitMessageSuffix"), "", "", "")
	var msg string
	if cm := part("commitMessage"); cm.set {
		cm.s, _, _ = strings.Cut(cm.s, "\n") // the title is the first line
		msg = conv(cm)
	} else {
		var l []string
		for _, k := range []string{"commitMessagePrefix", "commitMessageAction", "commitMessageTopic", "commitMessageExtra", "commitMessageSuffix"} {
			if vars[k] != "" {
				l = append(l, vars[k])
			}
		}
		msg = strings.Join(l, " ")
	}
	title := msg
	if pt := part("prTitle"); pt.s != "" {
		vars["commitMessage"] = msg
		title = conv(pt)
	}
	if failed {
		if v, ok := own["semanticCommits"]; ok && v == "disabled" {
			t.n.warn(at("semanticCommits"), "deps always writes conventional commit messages")
		}
		return "", false
	}
	return title, true
}
