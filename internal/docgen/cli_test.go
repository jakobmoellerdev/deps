package docgen

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func fakeTree() *cli.Command {
	run := func(context.Context, *cli.Command) error { return nil }
	build := &cli.Command{
		Name: "build", ArgsUsage: "<target>...", Usage: "Build targets", Description: "Builds `<target>`.", Action: run,
		Metadata: map[string]any{MetaExample: "  tool build a\n    tool build b c"},
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "jobs", Value: 4, Usage: "parallel jobs; '0' means one per CPU"},
			&cli.StringFlag{Name: "mode", Value: "fast", Usage: "mode: fast|slow"},
			&cli.BoolFlag{Name: "old", Usage: "deprecated flag", Deprecated: "use --mode"},
		},
	}
	cache := &cli.Command{Name: "cache", Usage: "Manage the cache", Commands: []*cli.Command{{Name: "clear", Usage: "Clear the cache", Action: run}}}
	hidden := &cli.Command{Name: "internal", Usage: "Internal", Hidden: true, Action: run}
	topic := &cli.Command{Name: "exit-codes", Usage: "Exit codes", Description: "| Code | When |\n|---|---|\n| `0` | ok |", Metadata: map[string]any{MetaTopic: true}}
	help := &cli.Command{Name: "help", Usage: "Help", Action: run}
	return &cli.Command{
		Name: "tool", Usage: "Do things to <files>", Description: "Long **markdown** text.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Value: ".", Usage: "directory to scan"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Usage: "print more"},
			&cli.StringFlag{Name: "secret", Usage: "hidden flag", Hidden: true},
			&cli.BoolFlag{Name: "help", Aliases: []string{"h"}, Usage: "show help"},
		},
		Commands: []*cli.Command{topic, hidden, cache, build, help},
	}
}

func TestCLI(t *testing.T) {
	b, err := CLI(fakeTree())
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		Header + "\n# CLI reference\n\nDo things to \\<files>.\n\n```\ntool [command] [flags]\n```\n\nLong **markdown** text.\n",
		"| [`tool build`](#tool-build) | Build targets |\n| [`tool cache`](#tool-cache) | Manage the cache |\n" +
			"| [`tool cache clear`](#tool-cache-clear) | Clear the cache |\n| [`tool help exit-codes`](#exit-codes) | Exit codes |\n",
		"## Global flags\n\nEvery command accepts these flags.\n\n| Flag | Type | Default | Description |\n|---|---|---|---|\n" +
			"| `--dir` | string | `\".\"` | directory to scan |\n| `-v, --verbose` | bool | `false` | print more |\n\n## tool build",
		"## tool build\n\nBuild targets.\n\n```\ntool build <target>... [flags]\n```\n\nBuilds `<target>`.\n",
		"### tool build examples\n\n```sh\ntool build a\n  tool build b c\n```\n",
		"### tool build flags\n\n| Flag | Type | Default | Description |\n|---|---|---|---|\n" +
			"| `--jobs` | int | `4` | parallel jobs; `0` means one per CPU |\n| `--mode` | string | `\"fast\"` | mode: fast\\|slow |\n\n",
		"## tool cache\n\nManage the cache.\n\n```\ntool cache [command]\n```\n\nSubcommands:\n\n- [`tool cache clear`](#tool-cache-clear): Clear the cache\n",
		"## tool cache clear\n",
		"## Exit codes\n\nExit codes. Also shown by `tool help exit-codes`.\n\n| Code | When |\n|---|---|\n| `0` | ok |\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks\n%s\n--- output:\n%s", want, out)
		}
	}
	for _, absent := range []string{"internal", "secret", "--old", "## help", "--help"} {
		if strings.Contains(out, absent) {
			t.Errorf("output contains hidden/deprecated %q", absent)
		}
	}
}

func TestDedent(t *testing.T) {
	for in, want := range map[string]string{
		"  a\n    b\n\n  c": "a\n  b\n\nc",
		"\ta\n\t\tb":        "a\n\tb",
		"a\n  b":            "a\n  b",
		"    x\n  y":        "  x\ny",
	} {
		if got := dedent(in); got != want {
			t.Errorf("dedent(%q) = %q, want %q", in, got, want)
		}
	}
}
