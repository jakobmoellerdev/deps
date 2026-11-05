package main

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
)

// helpFS holds the long help texts (markdown) of the commands and help topics; `deps docs`
// renders them into docs/reference/cli.md.
//
//go:embed help/*.md
var helpFS embed.FS

// helpText returns help/<name>.md without the trailing newline. A missing file is a
// programming error caught by the tests.
func helpText(name string) string {
	b, err := helpFS.ReadFile("help/" + name + ".md")
	if err != nil {
		panic(err)
	}
	return strings.TrimRight(string(b), "\n")
}

// newHelpTopics returns the additional help topics (`deps help <topic>`): commands without
// Action that only carry text.
func newHelpTopics() []*cli.Command {
	topic := map[string]any{docgen.MetaTopic: true}
	return []*cli.Command{
		{Name: "environment", Usage: "Environment variables deps reads", Description: helpText("environment"), Metadata: topic},
		{Name: "exit-codes", Usage: "Exit codes of every command", Description: helpText("exit-codes"), Metadata: topic},
		{Name: "output", Usage: "Report lines, summary lines, diagnostics and the CI step summary", Description: helpText("output"), Metadata: topic},
	}
}

func init() { cli.HelpPrinter = printHelp }

// newHelp is `deps help [command]...`; it replaces urfave/cli's built-in help command, which
// has no --help and an "h" alias.
func newHelp() *cli.Command {
	return &cli.Command{
		Name:      "help",
		Usage:     "Help about any command",
		ArgsUsage: "[command]",
		Description: "Help provides help for any command in the application.\n" +
			"Simply type deps help [path to command] for full details.",
		Action: func(_ context.Context, cmd *cli.Command) error {
			c := cmd.Root()
			for _, name := range cmd.Args().Slice() {
				sub := c.Command(name)
				if sub == nil {
					fmt.Fprintf(c.Writer, "Unknown help topic %#q\n", cmd.Args().Slice())
					return cli.ShowRootCommandHelp(c)
				}
				c = sub
			}
			cli.HelpPrinter(cmd.Root().Writer, "", c)
			return nil
		},
	}
}

// printHelp is the cli.HelpPrinter: it writes helpString instead of urfave/cli's template.
// cli.HelpPrinterFunc has no error result, so a failed write is dropped, as urfave/cli does.
func printHelp(w io.Writer, _ string, data any) {
	_, _ = io.WriteString(w, helpString(data.(*cli.Command)))
}

// helpString renders the help of c: the long description, then usage, examples,
// subcommands, flags, inherited (global) flags and help topics.
func helpString(c *cli.Command) string {
	var b strings.Builder
	if text := cmp.Or(c.Description, c.Usage); text != "" {
		b.WriteString(strings.TrimRightFunc(text, unicode.IsSpace))
		b.WriteString("\n\n")
	}
	if docgen.IsTopic(c) {
		return b.String()
	}
	subs := docgen.Available(c, true)
	b.WriteString("Usage:\n  " + c.FullName())
	if len(subs) > 0 {
		b.WriteString(" [command]")
	} else {
		if c.ArgsUsage != "" {
			b.WriteString(" " + c.ArgsUsage)
		}
		b.WriteString(" [flags]")
	}
	if len(c.Aliases) > 0 {
		b.WriteString("\n\nAliases:\n  " + strings.Join(c.Names(), ", "))
	}
	if ex := docgen.Example(c); ex != "" {
		b.WriteString("\n\nExamples:\n" + ex)
	}
	if len(subs) > 0 {
		pad := 11
		for _, s := range c.Commands {
			pad = max(pad, len(s.Name))
		}
		b.WriteString("\n\nAvailable Commands:")
		for _, s := range subs {
			fmt.Fprintf(&b, "\n  %-*s %s", pad, s.Name, s.Usage)
		}
	}
	local, _ := docgen.Flags(c.Flags)
	local = append(local, docgen.FlagDoc{Name: "help", Short: "h", Type: "bool", Zero: true, Usage: "help for " + c.Name})
	slices.SortFunc(local, func(a, b docgen.FlagDoc) int { return strings.Compare(a.Name, b.Name) })
	b.WriteString("\n\nFlags:\n" + flagUsages(local))
	var inherited []cli.Flag
	for _, p := range c.Lineage()[1:] {
		for _, f := range p.Flags {
			if l, ok := f.(cli.LocalFlag); !ok || !l.IsLocal() {
				inherited = append(inherited, f)
			}
		}
	}
	if global, _ := docgen.Flags(inherited); len(global) > 0 {
		b.WriteString("\n\nGlobal Flags:\n" + flagUsages(global))
	}
	if topics := docgen.Topics(c); len(topics) > 0 {
		pad := 11
		for _, s := range c.Commands {
			pad = max(pad, len(c.FullName()+" "+s.Name))
		}
		b.WriteString("\n\nAdditional help topics:")
		for _, t := range topics {
			fmt.Fprintf(&b, "\n  %-*s %s", pad, c.FullName()+" "+t.Name, t.Usage)
		}
	}
	if len(subs) > 0 {
		fmt.Fprintf(&b, "\n\nUse %q for more information about a command.", c.FullName()+" [command] --help")
	}
	b.WriteString("\n")
	return b.String()
}

// flagUsages lists flags one per line: names and value type, then the usage, aligned, and the
// default unless it is the zero value.
func flagUsages(fs []docgen.FlagDoc) string {
	left := make([]string, len(fs))
	width := 0
	for i, f := range fs {
		l := "      --" + f.Name
		if f.Short != "" {
			l = "  -" + f.Short + ", --" + f.Name
		}
		if f.Type != "bool" {
			l += " " + f.Type
		}
		left[i] = l
		width = max(width, len(l))
	}
	var b strings.Builder
	for i, f := range fs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(left[i] + strings.Repeat(" ", width-len(left[i])+3) + f.Usage)
		switch {
		case f.Zero:
		case f.Type == "string":
			fmt.Fprintf(&b, " (default %q)", f.Default)
		default:
			fmt.Fprintf(&b, " (default %s)", f.Default)
		}
	}
	return b.String()
}
