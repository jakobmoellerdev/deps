package main

import (
	"embed"
	"strings"

	"github.com/spf13/cobra"
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
// Run that only carry text.
func newHelpTopics() []*cobra.Command {
	return []*cobra.Command{
		{Use: "environment", Short: "Environment variables deps reads", Long: helpText("environment")},
		{Use: "exit-codes", Short: "Exit codes of every command", Long: helpText("exit-codes")},
		{Use: "output", Short: "Report lines, summary lines, diagnostics and the CI step summary", Long: helpText("output")},
	}
}
