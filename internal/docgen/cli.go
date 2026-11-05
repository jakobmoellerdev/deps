package docgen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"
)

// Metadata keys (cli.Command.Metadata) of what urfave/cli has no field for.
const (
	// MetaExample holds the examples of a command (string), indented for the terminal.
	MetaExample = "example"
	// MetaTopic marks an additional help topic (any value): a command that only carries the
	// text of its Description, shown by `<root> help <name>`.
	MetaTopic = "topic"
)

// Example returns the examples of c (MetaExample).
func Example(c *cli.Command) string {
	s, _ := c.Metadata[MetaExample].(string)
	return s
}

// IsTopic reports whether c is an additional help topic (MetaTopic).
func IsTopic(c *cli.Command) bool {
	_, ok := c.Metadata[MetaTopic]
	return ok
}

// Available returns the subcommands of c that help and the reference list, sorted by name:
// neither hidden, deprecated nor help topics. The built-in help command is left out unless
// withHelp.
func Available(c *cli.Command, withHelp bool) []*cli.Command {
	var out []*cli.Command
	for _, s := range c.Commands {
		if s.Hidden || s.Deprecated != "" || IsTopic(s) || (s.Name == "help" && !withHelp) {
			continue
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *cli.Command) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Topics returns the additional help topics below c, sorted by name.
func Topics(c *cli.Command) []*cli.Command {
	var out []*cli.Command
	for _, s := range c.Commands {
		if IsTopic(s) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b *cli.Command) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// FlagDoc is what help and the reference show of a flag.
type FlagDoc struct {
	Name, Short string // Short is the one-letter alias, if any
	Type        string // string, int or bool
	Default     string // the declared default, unquoted
	Zero        bool   // the default is the type's zero value
	Usage       string
}

// Flags describes the visible flags of fs, sorted by name: neither hidden, deprecated nor the
// built-in help flag. Only string, int and bool flags are supported.
func Flags(fs []cli.Flag) ([]FlagDoc, error) {
	var out []FlagDoc
	for _, f := range fs {
		if v, ok := f.(cli.VisibleFlag); ok && !v.IsVisible() {
			continue
		}
		if d, ok := f.(cli.DeprecatedFlag); ok && d.GetDeprecated() != "" {
			continue
		}
		names := f.Names()
		if names[0] == "help" {
			continue
		}
		d := FlagDoc{Name: names[0]}
		for _, a := range names[1:] {
			if len(a) == 1 {
				d.Short = a
			}
		}
		switch f := f.(type) {
		case *cli.StringFlag:
			d.Type, d.Default, d.Usage = "string", f.Value, f.Usage
			d.Zero = f.Value == ""
		case *cli.IntFlag:
			d.Type, d.Default, d.Usage = "int", strconv.Itoa(f.Value), f.Usage
			d.Zero = f.Value == 0
		case *cli.BoolFlag:
			d.Type, d.Default, d.Usage = "bool", strconv.FormatBool(f.Value), f.Usage
			d.Zero = !f.Value
		default:
			return nil, fmt.Errorf("flag --%s: unsupported type %T", d.Name, f)
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b FlagDoc) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// CLI renders the CLI reference of the urfave/cli tree under root: the root's long description
// and usage, a command index, the global flags (those of root), one section per available
// command (see Available) with usage, description, examples and flags, and one section per
// additional help topic (see IsTopic), titled by its name.
func CLI(root *cli.Command) ([]byte, error) {
	d := newDoc("CLI reference")
	d.para(PlainToMarkdown(root.Usage) + ".")
	d.code("", root.Name+" [command] [flags]")
	d.para(root.Description)
	cmds := walk(root)
	topics := Topics(root)
	d.heading(2, "Commands")
	rows := make([][]string, 0, len(cmds))
	for _, c := range cmds {
		rows = append(rows, []string{"[" + codeSpan(c.path) + "](#" + Anchor(c.path) + ")", PlainToMarkdown(c.Usage)})
	}
	for _, t := range topics {
		rows = append(rows, []string{"[" + codeSpan(root.Name+" help "+t.Name) + "](#" + Anchor(topicTitle(t)) + ")", PlainToMarkdown(t.Usage)})
	}
	d.table([]string{"Command", "Description"}, rows)
	global, err := Flags(root.Flags)
	if err != nil {
		return nil, err
	}
	if len(global) > 0 {
		d.heading(2, "Global flags")
		d.para("Every command accepts these flags.")
		flagTable(d, global)
	}
	for _, c := range cmds {
		fs, err := Flags(c.Flags)
		if err != nil {
			return nil, err
		}
		subs := Available(c.Command, false)
		d.heading(2, c.path)
		d.para(PlainToMarkdown(c.Usage) + ".")
		d.code("", usage(c.path, c.Command, len(fs) > 0, len(subs) > 0))
		d.para(c.Description)
		if ex := Example(c.Command); ex != "" {
			d.heading(3, c.path+" examples")
			d.code("sh", dedent(ex))
		}
		if len(fs) > 0 {
			d.heading(3, c.path+" flags")
			flagTable(d, fs)
		}
		if len(subs) > 0 {
			items := make([]string, 0, len(subs))
			for _, s := range subs {
				p := c.path + " " + s.Name
				items = append(items, "["+codeSpan(p)+"](#"+Anchor(p)+"): "+PlainToMarkdown(s.Usage))
			}
			d.para("Subcommands:")
			d.list(items)
		}
	}
	for _, t := range topics {
		d.heading(2, topicTitle(t))
		d.para(PlainToMarkdown(t.Usage) + ". Also shown by " + codeSpan(root.Name+" help "+t.Name) + ".")
		d.para(t.Description)
	}
	return d.bytes()
}

// pathCommand is a command with its path from the root ("deps completion bash").
type pathCommand struct {
	*cli.Command
	path string
}

// walk returns the available commands below root depth-first in name order.
func walk(root *cli.Command) []pathCommand {
	var cmds []pathCommand
	var rec func(c *cli.Command, path string)
	rec = func(c *cli.Command, path string) {
		for _, s := range Available(c, false) {
			p := path + " " + s.Name
			cmds = append(cmds, pathCommand{s, p})
			rec(s, p)
		}
	}
	rec(root, root.Name)
	return cmds
}

// dedent removes the indentation all non-blank lines of s share (examples are indented for
// the terminal).
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
func topicTitle(c *cli.Command) string {
	n := strings.ReplaceAll(c.Name, "-", " ")
	return strings.ToUpper(n[:1]) + n[1:]
}

// usage is the usage line of the command c at path: its arguments, then [flags] when it has
// flags of its own and [command] when it has subcommands.
func usage(path string, c *cli.Command, flags, subs bool) string {
	u := path
	if c.ArgsUsage != "" {
		u += " " + c.ArgsUsage
	}
	if flags {
		u += " [flags]"
	}
	if subs {
		u += " [command]"
	}
	return u
}

func flagTable(d *doc, fs []FlagDoc) {
	rows := make([][]string, 0, len(fs))
	for _, f := range fs {
		name := "--" + f.Name
		if f.Short != "" {
			name = "-" + f.Short + ", " + name
		}
		def := f.Default
		if f.Type == "string" {
			def = `"` + def + `"`
		}
		rows = append(rows, []string{codeSpan(name), f.Type, codeSpan(def), PlainToMarkdown(f.Usage)})
	}
	d.table([]string{"Flag", "Type", "Default", "Description"}, rows)
}
