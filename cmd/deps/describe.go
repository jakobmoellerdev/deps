package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
)

// describeWidth is the column describe wraps its text at.
const describeWidth = 100

func newConfigDescribe() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		ArgsUsage: "[key]",
		Usage:     "Describe the configuration keys, or one key in full",
		Metadata: map[string]any{docgen.MetaExample: "  deps config describe\n" +
			"  deps config describe rules[].allow\n" +
			"  deps config describe customDatasources.<name>.versions"},
		ArgValidator: func(_ context.Context, cmd *cli.Command) error {
			if n := cmd.Args().Len(); n > 1 {
				return &usageError{fmt.Errorf("want at most one key, got %d", n)}
			}
			return nil
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			s, err := reflectConfig()
			if err != nil {
				return err
			}
			keys, err := docgen.ConfigKeys(s)
			if err != nil {
				return err
			}
			var b strings.Builder
			if !cmd.Args().Present() {
				listKeys(&b, keys)
			} else {
				k, ok := findKey(keys, cmd.Args().First())
				if !ok {
					return unknownKeyError(keys, cmd.Args().First())
				}
				describeKey(&b, docgen.Facts(s, k), k)
			}
			if _, err := fmt.Fprint(cmd.Root().Writer, b.String()); err != nil {
				return fmt.Errorf("write description: %w", err)
			}
			return nil
		},
	}
}

// listKeys writes one line per key: its path and the gist of its description (shortSummary).
func listKeys(b *strings.Builder, keys []docgen.ConfigKey) {
	width := 0
	for _, k := range keys {
		width = max(width, len(k.Path))
	}
	for _, k := range keys {
		fmt.Fprintf(b, "%-*s  %s\n", width, k.Path, shortSummary(docgen.MarkdownToPlain(k.Summary(), 1<<30)))
	}
}

// describeKey writes the path, the facts (type, default, values, …) and the full description of k.
func describeKey(b *strings.Builder, facts []docgen.Fact, k docgen.ConfigKey) {
	b.WriteString(k.Path + "\n\n")
	width := 0
	for _, f := range facts {
		width = max(width, utf8.RuneCountInString(f.Label)+1)
	}
	for _, f := range facts {
		value := docgen.MarkdownToPlain(f.Value, describeWidth-width-2)
		value = strings.ReplaceAll(value, "\n", "\n"+strings.Repeat(" ", width+2))
		fmt.Fprintf(b, "%-*s  %s\n", width, f.Label+":", value)
	}
	for _, part := range []string{k.Summary(), k.Details()} {
		if part != "" {
			b.WriteString("\n" + docgen.MarkdownToPlain(part, describeWidth) + "\n")
		}
	}
}

// shortSummary returns the first sentence of s (". " not after e.g. or i.e.) up to its first
// ": " or "; " outside parentheses after at least three words, without the final period.
func shortSummary(s string) string {
	for i := 0; ; {
		j := strings.Index(s[i:], ". ")
		if j < 0 {
			break
		}
		end := i + j + 1
		if !strings.HasSuffix(s[:end], "e.g.") && !strings.HasSuffix(s[:end], "i.e.") {
			s = s[:end]
			break
		}
		i = end
	}
	depth := 0
	for i := 0; i+1 < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth = max(0, depth-1) // ranges such as [1.0,2.0) close nothing
		case ':', ';':
			// A short lead-in ("With separateMajorMinor:") says nothing on its own.
			if depth == 0 && s[i+1] == ' ' && len(strings.Fields(s[:i])) >= 3 {
				return s[:i]
			}
		}
	}
	return strings.TrimSuffix(s, ".")
}

// indexRe matches a list index or the [] list marker of a key path.
var indexRe = regexp.MustCompile(`\[\d*\]`)

// canonicalKey removes list markers and indexes: rules[2].allow and rules[].allow both become
// rules.allow.
func canonicalKey(path string) string { return indexRe.ReplaceAllString(path, "") }

// findKey returns the key named by q: a path as listed (rules[].allow), without list markers
// (rules.allow), with an index (rules[2].allow, as in validation errors) or with a map key in
// place of <name> (customDatasources.envtest.url).
func findKey(keys []docgen.ConfigKey, q string) (docgen.ConfigKey, bool) {
	want := strings.Split(canonicalKey(q), ".")
	for _, k := range keys {
		have := strings.Split(canonicalKey(k.Path), ".")
		if len(have) != len(want) {
			continue
		}
		match := true
		for i := range have {
			if have[i] != want[i] && have[i] != "<name>" {
				match = false
				break
			}
		}
		if match {
			return k, true
		}
	}
	return docgen.ConfigKey{}, false
}

// unknownKeyError is the usage error of an unknown key q: it suggests the keys whose last part
// is q (allow → rules[].allow), else the closest key path.
func unknownKeyError(keys []docgen.ConfigKey, q string) error {
	var hint []string
	if !strings.ContainsAny(q, ".[") {
		for _, k := range keys {
			if k.Path != q && strings.HasSuffix(k.Path, "."+q) {
				hint = append(hint, k.Path)
			}
		}
	}
	if len(hint) == 0 {
		canon := make([]string, len(keys))
		for i, k := range keys {
			canon[i] = canonicalKey(k.Path)
		}
		if c := config.Closest(canonicalKey(q), canon); c != "" {
			for _, k := range keys {
				if canonicalKey(k.Path) == c {
					hint = append(hint, k.Path)
					break
				}
			}
		}
	}
	msg := fmt.Sprintf("unknown config key %q", q)
	if len(hint) > 0 {
		msg += "; did you mean " + strings.Join(hint, ", ") + "?"
	}
	return &usageError{fmt.Errorf("%s (deps config describe lists every key)", msg)}
}
