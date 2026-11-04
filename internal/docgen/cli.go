package docgen

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CLI renders the CLI reference of the cobra tree under root: the root's long description and
// usage, a command index, the global (persistent) flags, one section per available command
// (hidden and deprecated commands are left out) with usage, description, examples and flags,
// and one section per additional help topic (a command without Run, e.g. `deps help
// environment`), titled by its name.
func CLI(root *cobra.Command) ([]byte, error) {
	d := newDoc("CLI reference")
	d.para(PlainToMarkdown(root.Short) + ".")
	d.code("", root.CommandPath()+" [command] [flags]")
	d.para(root.Long)
	cmds, topics := walk(root)
	d.heading(2, "Commands")
	rows := make([][]string, 0, len(cmds))
	for _, c := range cmds {
		rows = append(rows, []string{"[" + codeSpan(c.CommandPath()) + "](#" + Anchor(c.CommandPath()) + ")", PlainToMarkdown(c.Short)})
	}
	for _, t := range topics {
		rows = append(rows, []string{"[" + codeSpan(t.Root().Name()+" help "+t.Name()) + "](#" + Anchor(topicTitle(t)) + ")", PlainToMarkdown(t.Short)})
	}
	d.table([]string{"Command", "Description"}, rows)
	if fs := visible(root.PersistentFlags()); len(fs) > 0 {
		d.heading(2, "Global flags")
		d.para("Every command accepts these flags.")
		flagTable(d, fs)
	}
	for _, c := range cmds {
		d.heading(2, c.CommandPath())
		d.para(PlainToMarkdown(c.Short) + ".")
		d.code("", usage(c))
		d.para(c.Long)
		if c.Example != "" {
			d.heading(3, c.CommandPath()+" examples")
			d.code("sh", dedent(c.Example))
		}
		if fs := visible(c.LocalNonPersistentFlags()); len(fs) > 0 {
			d.heading(3, c.CommandPath()+" flags")
			flagTable(d, fs)
		}
		if subs := available(c); len(subs) > 0 {
			items := make([]string, 0, len(subs))
			for _, s := range subs {
				items = append(items, "["+codeSpan(s.CommandPath())+"](#"+Anchor(s.CommandPath())+"): "+PlainToMarkdown(s.Short))
			}
			d.para("Subcommands:")
			d.list(items)
		}
	}
	for _, t := range topics {
		d.heading(2, topicTitle(t))
		d.para(PlainToMarkdown(t.Short) + ". Also shown by " + codeSpan(t.Root().Name()+" help "+t.Name()) + ".")
		d.para(t.Long)
	}
	return d.bytes()
}

// walk returns the available commands below root depth-first in cobra's (sorted) order, and the
// additional help topics of root.
func walk(root *cobra.Command) (cmds, topics []*cobra.Command) {
	var rec func(c *cobra.Command)
	rec = func(c *cobra.Command) {
		for _, s := range available(c) {
			cmds = append(cmds, s)
			rec(s)
		}
	}
	rec(root)
	for _, c := range root.Commands() {
		if c.IsAdditionalHelpTopicCommand() {
			topics = append(topics, c)
		}
	}
	return cmds, topics
}

func available(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, s := range c.Commands() {
		if s.IsAvailableCommand() && s.Name() != "help" {
			out = append(out, s)
		}
	}
	return out
}

// dedent removes the indentation all non-blank lines of s share (cobra examples are indented
// for the terminal).
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	prefix, first := "", true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ws := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if first {
			prefix, first = ws, false
		}
		for !strings.HasPrefix(ws, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, prefix)
	}
	return strings.Join(lines, "\n")
}

// topicTitle is the heading of a help topic: its name with '-' as spaces, capitalized
// ("exit-codes" → "Exit codes").
func topicTitle(c *cobra.Command) string {
	n := strings.ReplaceAll(c.Name(), "-", " ")
	return strings.ToUpper(n[:1]) + n[1:]
}

func usage(c *cobra.Command) string {
	u := c.UseLine()
	if c.HasAvailableSubCommands() {
		u += " [command]"
	}
	return u
}

func visible(fs *pflag.FlagSet) []*pflag.Flag {
	var out []*pflag.Flag
	fs.VisitAll(func(f *pflag.Flag) {
		if !f.Hidden && f.Deprecated == "" {
			out = append(out, f)
		}
	})
	return out
}

func flagTable(d *doc, fs []*pflag.Flag) {
	rows := make([][]string, 0, len(fs))
	for _, f := range fs {
		name := "--" + f.Name
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", " + name
		}
		def := f.DefValue
		if f.Value.Type() == "string" {
			def = `"` + def + `"`
		}
		rows = append(rows, []string{codeSpan(name), f.Value.Type(), codeSpan(def), PlainToMarkdown(f.Usage)})
	}
	d.table([]string{"Flag", "Type", "Default", "Description"}, rows)
}
