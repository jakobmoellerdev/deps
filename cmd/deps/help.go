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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
)

// helpFS holds the long help texts (markdown) of the commands and help topics, one file per
// command (helpName); `deps docs` renders them into docs/reference/cli.md.
//
//go:embed help/*.md
var helpFS embed.FS

// helpText returns help/<name>.md without the trailing newline, or "" when there is none.
func helpText(name string) string {
	b, err := helpFS.ReadFile("help/" + name + ".md")
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

// helpName is the help file name of c: its path below the root joined by '-' (config-show),
// or root. It needs the parents urfave/cli sets when the tree runs.
func helpName(c *cli.Command) string {
	l := c.Lineage()
	if len(l) == 1 {
		return "root"
	}
	names := make([]string, 0, len(l)-1)
	for i := len(l) - 2; i >= 0; i-- {
		names = append(names, l[i].Name)
	}
	return strings.Join(names, "-")
}

// envTopic is the help topic listing the environment variables (environmentHelp).
const envTopic = "environment"

// newHelpTopics returns the additional help topics (`deps help <topic>`): commands without
// Action that only carry text.
func newHelpTopics() []*cli.Command {
	topic := map[string]any{docgen.MetaTopic: true}
	return []*cli.Command{
		{Name: envTopic, Usage: "Environment variables deps reads", Metadata: topic},
		{Name: "exit-codes", Usage: "Exit codes of every command", Metadata: topic},
		{Name: "output", Usage: "Report lines, summary lines, diagnostics and the CI step summary", Metadata: topic},
	}
}

// fillHelp sets the Description of c, unless set, from its help file (helpName); the
// environment topic gets environmentHelp. newRoot leaves Description empty: every invocation
// builds the command tree, but only help and `deps docs` show the texts.
func fillHelp(c *cli.Command) {
	fillNamed(c.Root(), c, helpName(c))
}

func fillNamed(root, c *cli.Command, name string) {
	if c.Description != "" {
		return
	}
	if name == envTopic {
		c.Description = environmentHelp(root)
	} else {
		c.Description = helpText(name)
	}
}

// fillAllHelp fills the help of root and of every command below it (fillHelp); the tree
// need not have run.
func fillAllHelp(root *cli.Command) {
	var rec func(c *cli.Command, name string)
	rec = func(c *cli.Command, name string) {
		fillNamed(root, c, name)
		for _, sub := range c.Commands {
			n := sub.Name
			if c != root {
				n = name + "-" + sub.Name
			}
			rec(sub, n)
		}
	}
	rec(root, "root")
}

// environmentHelp is help/environment.md with one row per flag variable (flagEnv) appended
// to its first table; variables the hand-written rows already describe are left out.
func environmentHelp(root *cli.Command) string {
	text := helpText(envTopic)
	var described strings.Builder // the hand-written table rows
	for l := range strings.Lines(text) {
		if strings.HasPrefix(l, "|") {
			described.WriteString(l)
		}
	}
	type row struct {
		env, usage string
		cmds       []string
	}
	var rows []*row
	byEnv := map[string]*row{}
	add := func(fs []cli.Flag, cmd string) {
		docs, _ := docgen.Flags(fs) // hidden flags and help are left out
		for _, f := range docs {
			env := flagEnv(f.Name)
			if strings.Contains(described.String(), "`"+env+"`") {
				continue
			}
			r := byEnv[env]
			if r == nil {
				r = &row{env: env, usage: "default of `--" + f.Name + "`: " + f.Usage}
				byEnv[env] = r
				rows = append(rows, r)
			}
			if !slices.Contains(r.cmds, cmd) {
				r.cmds = append(r.cmds, cmd)
			}
		}
	}
	add(root.Flags, "all")
	var walk func(c *cli.Command, path string)
	walk = func(c *cli.Command, path string) {
		// In name order, without hidden commands, help topics and help.
		for _, sub := range docgen.Available(c, false) {
			// urfave/cli adds completion when the tree runs; it has no flags.
			if c == root && sub.Name == "completion" {
				continue
			}
			p := strings.TrimPrefix(path+" "+sub.Name, " ")
			if sub.Action != nil {
				add(sub.Flags, "`"+p+"`")
			}
			walk(sub, p)
		}
	}
	walk(root, "")
	slices.SortFunc(rows, func(a, b *row) int { return strings.Compare(a.env, b.env) })
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("| `" + r.env + "` | " + strings.Join(r.cmds, ", ") + " | " + strings.ReplaceAll(r.usage, "|", `\|`) + " |\n")
	}
	lines := strings.SplitAfter(text, "\n")
	end := len(lines)
	inTable := false
	for i, l := range lines {
		if strings.HasPrefix(l, "|") {
			inTable = true
		} else if inTable {
			end = i
			break
		}
	}
	return strings.TrimRight(strings.Join(lines[:end], "")+ensureNewline(b.String(), end == len(lines))+strings.Join(lines[end:], ""), "\n")
}

// ensureNewline prefixes s with a newline when it follows the unterminated last line.
func ensureNewline(s string, atEnd bool) string {
	if atEnd {
		return "\n" + s
	}
	return s
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

// helpWidth is the column plain help text is wrapped at.
const helpWidth = 80

// printHelp is the cli.HelpPrinter: it writes helpString instead of urfave/cli's template.
// On a terminal the markdown of the description is rendered as plain text; piped help and
// `deps docs` keep the markdown. cli.HelpPrinterFunc has no error result, so a failed write
// is dropped, as urfave/cli does.
func printHelp(w io.Writer, _ string, data any) {
	c := data.(*cli.Command)
	fillHelp(c)
	if logx.IsTerminal(w) {
		desc := c.Description
		c.Description = docgen.MarkdownToPlain(desc, helpWidth)
		defer func() { c.Description = desc }()
	}
	_, _ = io.WriteString(w, helpString(c))
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
