package docgen

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func fakeTree() *cobra.Command {
	run := func(*cobra.Command, []string) {}
	root := &cobra.Command{Use: "tool", Short: "Do things to <files>", Long: "Long **markdown** text."}
	root.PersistentFlags().String("dir", ".", "directory to scan")
	root.PersistentFlags().BoolP("verbose", "v", false, "print more")
	root.PersistentFlags().String("secret", "", "hidden flag")
	_ = root.PersistentFlags().MarkHidden("secret")

	build := &cobra.Command{Use: "build <target>...", Short: "Build targets", Long: "Builds `<target>`.", Example: "  tool build a\n    tool build b c", Run: run}
	build.Flags().Int("jobs", 4, "parallel jobs; '0' means one per CPU")
	build.Flags().String("mode", "fast", "mode: fast|slow")
	build.Flags().Bool("old", false, "deprecated flag")
	_ = build.Flags().MarkDeprecated("old", "use --mode")

	cache := &cobra.Command{Use: "cache", Short: "Manage the cache"}
	cache.AddCommand(&cobra.Command{Use: "clear", Short: "Clear the cache", Run: run})

	hidden := &cobra.Command{Use: "internal", Short: "Internal", Hidden: true, Run: run}
	topic := &cobra.Command{Use: "exit-codes", Short: "Exit codes", Long: "| Code | When |\n|---|---|\n| `0` | ok |"}
	root.AddCommand(build, cache, hidden, topic)
	return root
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
	for _, absent := range []string{"internal", "secret", "--old", "## help"} {
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
