package beam

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func extract(t *testing.T, m manager.Manager, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := m.Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return ds
}

// summary is one line per dep: line, name, dep type, datasource, range, version, skip.
func summary(ds []dep.Dep) map[string]string {
	out := map[string]string{}
	for _, d := range ds {
		out[d.Name] = fmt.Sprintf("%d %s %s %q %q %q %s", d.Line, d.DepType, d.Datasource, d.Range, d.Version, d.Skip, d.Source)
	}
	return out
}

func TestMatch(t *testing.T) {
	mix, gleam := NewMix(), NewGleam()
	for p, want := range map[string]bool{"mix.exs": true, "apps/web/mix.exs": true, "xmix.exs": false, "mix.lock": false} {
		if mix.Match(p) != want {
			t.Errorf("mix Match(%q) = %v", p, !want)
		}
	}
	for p, want := range map[string]bool{"gleam.toml": true, "a/gleam.toml": true, "manifest.toml": false} {
		if gleam.Match(p) != want {
			t.Errorf("gleam Match(%q) = %v", p, !want)
		}
	}
	if mix.Name() != "mix" || gleam.Name() != "gleam" {
		t.Error("names")
	}
}

// Real-world mix.exs files (fetched 2026-11-06): plausible/analytics, livebook-dev/livebook,
// hexpm/hexpm and the elixir-lsp/elixir-ls umbrella.
func TestExtractMixFixtures(t *testing.T) {
	for file, want := range map[string]map[string]string{
		"testdata/plausible/mix.exs": {
			"bamboo":         `76 prod hex "~> 2.3" "2.3.0" "" `,
			"bypass":         `80 dev hex "~> 2.1" "2.1.0" "" `,
			"excoveralls":    `94 dev hex "~> 0.10" "0.10.0" "" `,
			"phoenix":        `130 prod hex "~> 1.8.2" "1.8.2" "" `,
			"esbuild":        `164 prod hex "~> 0.7" "0.7.0" "" `,
			"location":       `105 prod git-tags "" "" "git dependency" `,
			"con_cache":      `172 prod git-tags "" "" "git dependency" `,
			"happy_tcp":      `176 dev git-tags "" "" "git dependency" `,
			"logger_backend": "",
		},
		"testdata/livebook/mix.exs": {
			"phoenix":    `126 prod hex "~> 1.8" "1.8.0" "" `,
			"tidewave":   `150 dev hex "~> 0.5" "0.5.0" "" `,
			"elixirkit":  `162 prod git-tags "" "" "git dependency" `,
			"aws_signer": "",
		},
		"testdata/hexpm/mix.exs": {
			"hex_core": `64 prod hex "~> 0.18" "0.18.0" "" `,
			"finch":    `56 prod git-tags "" "" "git dependency" `,
			"req":      `162 prod hex "0.7.5" "0.7.5" "" `,
		},
		"testdata/elixir-ls/mix.exs": {"spec_lint": `31 dev hex "~> 0.1" "0.1.0" "" `},
		"testdata/elixir-ls/apps/language_server/mix.exs": {
			"elixir_ls_utils": `45 prod hex "" "" "umbrella dependency" `,
			"elixir_sense":    `46 prod git-tags "" "" "git dependency" `,
			"typed_struct":    `58 prod hex "~> 0.3" "0.3.0" "" `,
		},
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		got := summary(extract(t, NewMix(), file, src))
		for name, w := range want {
			if got[name] != w {
				t.Errorf("%s: %s = %s, want %s", file, name, got[name], w)
			}
		}
	}
	src, _ := os.ReadFile("testdata/plausible/mix.exs")
	if n := len(extract(t, NewMix(), "mix.exs", src)); n != 100 {
		t.Errorf("plausible deps = %d, want 100", n)
	}
	src, _ = os.ReadFile("testdata/hexpm/mix.exs")
	if n := len(extract(t, NewMix(), "mix.exs", src)); n != 116 {
		t.Errorf("hexpm deps = %d, want 116", n)
	}
}

const tricky = `defmodule App.MixProject do
  use Mix.Project

  @version "1.0.0"
  @ecto "~> 3.0"

  def project do
    [app: :app, version: @version, deps: deps() ++ extra_deps(Mix.target()),
     docs: [extras: [{"README.md", title: "Overview"}]]]
  end

  # {:commented, "~> 1.0"} is a comment
  defp deps do
    [
      {:plain, "~> 1.7"},
      {:spaced , ">= 1.0.0 and < 2.0.0" , only: :test},
      {:prod_too, "~> 2.0", only: [:dev, :prod]},
      {:renamed, "~> 0.4", hex: :real_name},
      {:renamed_str, "~> 0.4", hex: "real_str"},
      {:private, "~> 1.0", organization: "acme"},
      {:private_repo, "~> 1.0", repo: "hexpm:corp"},
      {:selfhosted, "~> 1.0", repo: "mirror"},
      {:attr, @ecto},
      {:interp, "~> #{@version}"},
      {:escaped, "~> 1.\x30"},
      {:no_req, override: true},
      {:bracketed, "~> 1.1", [only: :dev, runtime: false]},
      {:tagged, git: "https://github.com/acme/tagged.git", tag: "v1.2.0"},
      {:gh_tagged,
       github: "acme/gh",   # a comment with "quotes" and {:braces, "1"}
       tag: "v0.3.0"},
      {:branch, github: "acme/branch", branch: "main"},
      {:ssh, git: "git@github.com:acme/ssh.git", tag: "v2"},
      {:local, path: "../local"},
      {:umbrella, in_umbrella: true},
      {:sigil, ~s(not "a" dep), only: :dev},
      {:charlist, 'abc'},
      {:heredoc, """
      not a requirement
      """},
      {:char, "~> 1.0", only: if(?} == ?}, do: :test, else: :prod)}
    ]
  end

  defp extra_deps(:host), do: [{:one_liner, "~> 0.2"}]
  defp extra_deps(_), do: []

  defp other_list do
    [{:ignored, "~> 1.0"}]
  end
end
`

func TestExtractMix(t *testing.T) {
	ds := extract(t, NewMix(), "mix.exs", []byte(tricky))
	got := summary(ds)
	for name, want := range map[string]string{
		"plain":             `15 prod hex "~> 1.7" "1.7.0" "" `,
		"spaced":            `16 dev hex ">= 1.0.0 and < 2.0.0" "1.0.0" "" `,
		"prod_too":          `17 prod hex "~> 2.0" "2.0.0" "" `,
		"real_name":         `18 prod hex "~> 0.4" "0.4.0" "" `,
		"real_str":          `19 prod hex "~> 0.4" "0.4.0" "" `,
		"private:acme":      `20 prod hex "~> 1.0" "1.0.0" "" `,
		"private_repo:corp": `21 prod hex "~> 1.0" "1.0.0" "" `,
		"selfhosted":        `22 prod hex "" "~> 1.0" "custom hex repository" `,
		"attr":              `23 prod hex "" "@ecto" "unsupported requirement" `,
		"interp":            `24 prod hex "" "~> #{@version}" "cannot locate value" `,
		"escaped":           `25 prod hex "" "~> 1.\\x30" "cannot locate value" `,
		"no_req":            `26 prod hex "" "" "no requirement" `,
		"bracketed":         `27 dev hex "~> 1.1" "1.1.0" "" `,
		"tagged":            `28 prod git-tags "" "v1.2.0" "" https://github.com/acme/tagged`,
		"gh_tagged":         `29 prod git-tags "" "v0.3.0" "" https://github.com/acme/gh`,
		"branch":            `32 prod git-tags "" "" "git dependency" `,
		"ssh":               `33 prod git-tags "" "v2" "" https://github.com/acme/ssh`,
		"local":             `34 prod hex "" "" "path dependency" `,
		"umbrella":          `35 prod hex "" "" "umbrella dependency" `,
		"sigil":             `36 dev hex "" "~s(not \"a\" dep)" "unsupported requirement" `,
		"charlist":          `37 prod hex "" "'abc'" "unsupported requirement" `,
		"heredoc":           "38 prod hex \"\" \"\\\"\\\"\\\"\\n      not a requirement\\n      \\\"\\\"\\\"\" \"unsupported requirement\" ",
		"char":              `41 prod hex "~> 1.0" "1.0.0" "" `,
		"one_liner":         `45 prod hex "~> 0.2" "0.2.0" "" `,
		"commented":         "",
		"ignored":           "",
		"README.md":         "",
		"Overview":          "",
	} {
		if got[name] != want {
			t.Errorf("%s = %s, want %s", name, got[name], want)
		}
	}
	if len(ds) != 24 {
		t.Errorf("deps = %d, want 24: %v", len(ds), got)
	}
	// Edits replace the requirement or tag in place.
	for _, d := range ds {
		if d.Skip == "" && d.Range != "" && tricky[d.RefSpan.Start:d.RefSpan.End] != d.Range {
			t.Errorf("%s: span %q", d.Name, tricky[d.RefSpan.Start:d.RefSpan.End])
		}
		if d.Skip == "" && d.Range == "" && tricky[d.RefSpan.Start:d.RefSpan.End] != d.Version {
			t.Errorf("%s: tag span %q", d.Name, tricky[d.RefSpan.Start:d.RefSpan.End])
		}
	}
}

func TestEdits(t *testing.T) {
	src := []byte("defp deps, do: [{:a, \"~> 1.7\"}, {:b, github: \"o/b\", tag: \"v1\"}]\n")
	ds := extract(t, NewMix(), "mix.exs", src)
	m := NewMix().(interface {
		manager.Manager
		manager.RangeRewriter
	})
	es, err := m.Edits(dep.Update{Dep: ds[0], NewVersion: "2.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	es2, err := m.Edits(dep.Update{Dep: ds[1], NewVersion: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, append(es, es2...))
	if err != nil || string(out) != "defp deps, do: [{:a, \"~> 2.1\"}, {:b, github: \"o/b\", tag: \"v2\"}]\n" {
		t.Errorf("patched = %q, %v", out, err)
	}
	if _, err := m.Edits(dep.Update{Dep: ds[0], NewVersion: "junk"}); err == nil {
		t.Error("edit to an invalid version")
	}
	if r, ok := m.NewRange(ds[0], "2.1.0", "widen"); !ok || r != "~> 1.7 or ~> 2.1" {
		t.Errorf("NewRange = %q, %v", r, ok)
	}
}

func TestScanner(t *testing.T) {
	// Opaque text never ends a body early or opens one.
	src := []byte(`defp deps do
  x = ~r/end"/i
  y = ~S"""
  end do
  """
  z = 'end' <> "do #{"end"} \" end"
  fn -> :end end
  [{:ok_dep, "~> 1.0"}]
end
def deps_doc, do: "not a list"
`)
	ds := extract(t, NewMix(), "mix.exs", src)
	if len(ds) != 1 || ds[0].Name != "ok_dep" || ds[0].Line != 8 {
		t.Errorf("deps = %+v", ds)
	}
	for _, s := range []string{"", "defp deps do", "defp deps, do: [", "{", "\"unterminated", "~", "?", "~s", "defp deps do [{:a,"} {
		if _, err := NewMix().Extract("mix.exs", []byte(s), manager.Env{}); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if got := trim([]byte("  # c\n  x # d\n"), span{0, 14}); got != (span{8, 9}) {
		t.Errorf("trim = %v", got)
	}
	if !strings.Contains(tricky, "{:char,") {
		t.Fatal("fixture")
	}
}
