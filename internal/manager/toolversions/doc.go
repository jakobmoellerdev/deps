package toolversions

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// toolDatasources are the datasources of the tool table plus extra (the mise backends'), sorted.
func toolDatasources(extra ...string) []string {
	seen := map[string]bool{}
	for _, t := range tools {
		seen[t.datasource] = true
	}
	for _, ds := range extra {
		seen[ds] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// nodeSkips are the skip reasons of a Node.js version (nvm, nodenv).
var nodeSkips = []refdoc.Skip{
	{Reason: "non-version value", When: "empty or a word or alias such as `lts/*`, `lts/iron`, `node`, `stable`, `system`: they float to the newest matching release by themselves (Renovate's node versioning does not update them either)"},
	{Reason: "unsupported range", When: "anything else that is not a version with an optional operator or x-range, such as `>=20 <22` or `20 || 22`"},
}

// toolSkips are the skip reasons of a tool version (asdf, mise).
var toolSkips = []refdoc.Skip{
	{Reason: "unsupported tool", When: "a tool that is not in the tool table (or, for mise, an unsupported backend or name form)"},
	{Reason: "non-version value", When: "empty or a word such as `latest`, `system`, `lts`"},
	{Reason: "git ref", When: "`ref:…`; `branch:…` and `rev:…` of a mise cargo git tool; a commit hash of a mise go tool"},
	{Reason: "local path", When: "`path:…`"},
	{Reason: "unsupported range", When: "`prefix:…` or anything else that is not a version"},
	{Reason: "partial version", When: "`X` for go (Go has no `go 1` line)"},
	{Reason: "unsupported distribution", When: "a `java` version of a vendor other than Temurin/AdoptOpenJDK (`corretto-…`, `zulu-…`, `oracle-graalvm-…`), or a full version of mise's openjdk shorthand (`21.0.2`, `openjdk-21.0.2`): their builds are not in Adoptium's list"},
}

var nodeVersionItems = []string{
	"A full `[v]X.Y.Z[-pre]` uses `semver`, and the update keeps the `v` style.",
	"A partial `20` or `v20.11` uses `tag` versioning and keeps its precision (`20` → `22`); a leading `v` stays outside the replaced token.",
	"An npm operator (`>=20`, `^20.11.1`, `~20.11`, `=20`) or x-range (`20.x`, `20.11.*`) stays as written and the version in it moves as above (`>=20` → `>=24`, `20.x` → `24.x`), as the [npm](#npm) manager treats `engines`.",
	"Only the version token is replaced; comments and the rest of the file are kept.",
}

// DocNvm is the reference documentation of the nvm manager (docs/reference/managers.md).
var DocNvm = refdoc.Manager{
	Name:    NameNvm,
	Summary: "Updates the Node.js version in `.nvmrc`: the first token of the first line that is neither blank nor a `#` comment.",
	Files:   []string{"any `.nvmrc`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNode, What: "the Node.js version (name `node`)", Versioning: "`semver`; partial versions `tag`", Datasources: []string{"node-version"}},
	},
	Skips:    nodeSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: nodeVersionItems}},
}

// DocNodenv is the reference documentation of the nodenv manager (docs/reference/managers.md).
var DocNodenv = refdoc.Manager{
	Name:    NameNodenv,
	Summary: "Updates the Node.js version in `.node-version`: the first token of the first line that is neither blank nor a `#` comment.",
	Files:   []string{"any `.node-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeNode, What: "the Node.js version (name `node`)", Versioning: "`semver`; partial versions `tag`", Datasources: []string{"node-version"}},
	},
	Skips:    nodeSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: nodeVersionItems}},
}

// DocRubyVersion is the reference documentation of the ruby-version manager
// (docs/reference/managers.md).
var DocRubyVersion = refdoc.Manager{
	Name: NameRubyVersion,
	Summary: "Updates the Ruby version in `.ruby-version` (rbenv, chruby, rvm, asdf, mise, Bundler's `ruby file: \".ruby-version\"`): the first token of the first line that is neither blank nor a `#` comment. " +
		"It shares the built-in `ruby` group with the Gemfile `ruby` directive and asdf/mise `ruby` (see [bundler](#bundler)), so a Ruby bump is one PR.",
	Files: []string{"any `.ruby-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeRuby, What: "the Ruby version (name `ruby`), optionally with the `ruby-` prefix (`ruby-3.4.7`)", Versioning: "`ruby`; partial versions `tag`", Datasources: []string{"ruby-version"}},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-version value", When: "empty, another engine (`jruby-9.4.8.0`, `truffleruby-24.1.0`) or a word such as `system`"},
		{Reason: "unsupported range", When: "anything else that is not `[ruby-]X[.Y[.Z[-pre]]]`, such as `3.4.7@gemset`"},
	},
	Sections: []refdoc.Section{{Title: "Versions", Items: rubyVersionItems}},
}

var rubyVersionItems = []string{
	"A full `X.Y.Z[-pre]` uses `ruby` versioning (Gem::Version order); previews such as `3.5.0-preview1` are only proposed from a preview.",
	"A partial `3` or `3.4` uses `tag` versioning and keeps its precision (`3.4` → `4.0`).",
	"The `ruby-` prefix stays outside the replaced token; comments and the rest of the file are kept.",
}

// pythonSkips are the skip reasons of a Python version (pyenv, runtime-version).
var pythonSkips = []refdoc.Skip{
	{Reason: "non-version value", When: "empty, another implementation or distribution (`pypy3.10-7.3.17`, `miniconda3-latest`, `graalpy-24.1.0`) or a word such as `system`"},
	{Reason: "unsupported range", When: "anything else that is not `X[.Y[.Z[{a,b,rc}N]]][t]`, such as `3.14-dev` or `>=3.12`"},
}

var pythonVersionItems = []string{
	"A full `X.Y.Z` uses `pep440` versioning; pre-releases such as `3.15.0rc1` are only proposed from a pre-release.",
	"A partial `3` or `3.13` uses `tag` versioning and keeps its precision (`3.13` → `3.14`), as pyenv and uv resolve it to the newest patch.",
	"The free-threaded build's `t` (`3.13t`, `3.13.1t`) stays outside the replaced token; comments and the rest of the file are kept.",
	"Renovate uses the `docker` datasource (the `python` image tags) for these files; deps uses the python.org release list ([python-version](#datasources)), which asdf, mise and the devcontainer Python feature share.",
}

// DocPyenv is the reference documentation of the pyenv manager (docs/reference/managers.md).
var DocPyenv = refdoc.Manager{
	Name: NamePyenv,
	Summary: "Updates the Python version in `.python-version` (pyenv, uv, asdf and mise read it): the first token of the first line that is neither blank nor a `#` comment. " +
		"Further versions (`pyenv local 3.13 3.12` writes one per line) are kept.",
	Files: []string{"any `.python-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypePython, What: "the Python version (name `python`)", Versioning: "`pep440`; partial versions `tag`", Datasources: []string{dsPython}},
	},
	Skips:    pythonSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: pythonVersionItems}},
}

// DocRuntimeVersion is the reference documentation of the runtime-version manager
// (docs/reference/managers.md).
var DocRuntimeVersion = refdoc.Manager{
	Name: NameRuntimeVersion,
	Summary: "Updates the Python version in `runtime.txt` (Heroku, Scalingo, Binder): `python-<version>` as the first token of the first line that is neither blank nor a `#` comment. " +
		"A runtime.txt naming another runtime (Binder's `r-4.4-2024-10-01`) has no dep.",
	Files: []string{"any `runtime.txt`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypePython, What: "the Python version after `python-` (name `python`)", Versioning: "`pep440`; partial versions `tag`", Datasources: []string{dsPython}},
	},
	Skips: pythonSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: []string{
		"`python-3.13.1` uses `pep440` versioning, `python-3.13` `tag` versioning at its precision; the `python-` prefix stays outside the replaced token.",
		pythonVersionItems[3],
	}}},
}

// DocBunVersion is the reference documentation of the bun-version manager
// (docs/reference/managers.md).
var DocBunVersion = refdoc.Manager{
	Name: NameBunVersion,
	Summary: "Updates the Bun version in `.bun-version` (read by `oven-sh/setup-bun`'s `bun-version-file`): the first token of the first line that is neither blank nor a `#` comment. " +
		"As in Renovate, the version comes from the `bun` npm package, whose versions are Bun's releases.",
	Files: []string{"any `.bun-version`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeBun, What: "the Bun version (name `bun`)", Versioning: "`semver`; partial versions `tag`", Datasources: []string{dsNpm}},
	},
	Skips: []refdoc.Skip{
		{Reason: "non-version value", When: "empty or a word such as `latest` or `canary`: setup-bun resolves it by itself"},
		{Reason: "unsupported range", When: "anything else that is not `[v]X[.Y[.Z[-pre]]]`, such as `>=1.1` or `1.x`"},
	},
	Sections: []refdoc.Section{{Title: "Versions", Items: []string{
		"A full `[v]X.Y.Z[-pre]` uses `semver`, and the update keeps the `v` style.",
		"A partial `1` or `1.2` uses `tag` versioning and keeps its precision (`1.2` → `1.3`) from release lines deps derives from the stable releases, as for asdf; Renovate would replace it with a full version. A leading `v` stays outside the replaced token.",
		"Only the version token is replaced; comments and the rest of the file are kept.",
	}}},
}

var toolVersionItems = []string{
	"A full `[v]X.Y.Z[-pre]` uses `semver`, and the update keeps the `v` style; go-toolchain tools use `tag`, `ruby` uses `ruby` (as in `.ruby-version`), `python` uses `pep440` (as in `.python-version`), pypi tools use `pep440`.",
	"A partial version (`X`, `X.Y`) uses `tag` versioning and keeps its precision (`2.5` → `2.6`, `29` → `31`); a leading `v` stays outside the replaced token. node, go (`1.27`), ruby, python, rust (`1.82`) and crate list their release lines themselves; for the other datasources (github-releases, git-tags, npm, pypi, go, …) deps derives a line `X.Y` and `X` from every stable `[v]X.Y.Z` release (and the bare `X.Y` of a `vX.Y` release), dated by the line's first release. Renovate would replace a partial version by a full one.",
	"`flutter` versions may carry asdf-flutter's channel suffix (`3.24.5-stable`), which stays outside the token.",
	"`maven` and `gradle` use `maven` versioning, under which `8.11` and `3.9` are full versions.",
	"`ruby` shares the built-in `ruby` group with the Gemfile `ruby` directive and `.ruby-version` (see [bundler](#bundler)).",
	"`python` is read as in [`.python-version`](#pyenv): `pypy3.10-7.3.17`, `miniconda3-latest` are skipped as `non-version value`, a free-threaded `3.13t` keeps its `t`.",
	"`rust` is read as in [rust-toolchain](#rust-toolchain): `X.Y.Z` uses `semver`, `X.Y` `tag` ([rust-version](#datasources) lists the release lines), and a dated nightly (`nightly-2025-10-10`) moves to the newest nightly as a patch update; the channels `stable`, `beta` and `nightly` are skipped as `non-version value`. " +
		"A pinned `rust` of a mise config is the toolchain cargo resolves `Cargo.lock` with (see [cargo](#cargo)).",
	"`elixir` uses [hexpm-bob](#datasources), the builds asdf and mise install: a full `1.17.3` or `1.17.3-otp-27` uses a `regex:` versioning whose compatibility is the OTP release, so `1.17.3-otp-27` moves to the newest `-otp-27` build and a plain `1.17.3` to the newest plain one; release candidates (`1.18.0-rc.0`) only from a release candidate; a partial `1.17` keeps its precision. Branch builds (`main-otp-27`) are skipped as `non-version value`.",
	"`erlang` uses the `OTP-<version>` tags of `erlang/otp` (Renovate's choice; asdf and mise build OTP from them) with `loose` versioning, as OTP releases have two to four parts (`27.1`, `27.1.2`, `26.2.5.4`); `27.0-rc1` is a pre-release, a single `27` keeps its precision.",
	"`java` uses the `java-version` datasource (Eclipse Temurin releases of the Adoptium API), as Renovate's asdf and mise tables: asdf-java's and mise's `temurin-` and `adoptopenjdk-` versions are dep `java-jdk`, `temurin-jre-` and `adoptopenjdk-jre-` ones `java-jre`; the vendor prefix stays outside the token, and a full version is Adoptium's semver (`temurin-21.0.5+11.0.LTS`, `semver`). mise's vendorless shorthand (openjdk, also `openjdk-`) is `java-jdk` at its release line only (`21` → `25`, `tag`).",
	"Only the version token is replaced; quotes, comments and the rest of the file are kept.",
}

// DocAsdf is the reference documentation of the asdf manager (docs/reference/managers.md).
var DocAsdf = refdoc.Manager{
	Name:    NameAsdf,
	Summary: "Updates `<tool> <version> [fallback versions…]` lines of `.tool-versions`. Only the first version of a line is a dep; `#` starts a comment.",
	Files:   []string{"any `.tool-versions`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTool, What: "a tool line; the dep name comes from the tool table", Datasource: "per tool (see Tools)", Versioning: "`semver`, `tag`, `pep440`, `ruby` or `maven`", Datasources: toolDatasources()},
	},
	Skips:    toolSkips,
	Sections: []refdoc.Section{{Title: "Versions", Items: toolVersionItems}, toolsSection()},
}

// DocMise is the reference documentation of the mise manager (docs/reference/managers.md).
var DocMise = refdoc.Manager{
	Name: NameMise,
	Summary: "Updates the `[tools]` table of mise config files. The file is scanned as TOML without decoding it, " +
		"so only the version bytes change.",
	Files: []string{"`mise.toml`, `.mise.toml`", "`mise/config.toml`, `.mise/config.toml`", "`.config/mise.toml`, `.config/mise/config.toml`"},
	DepTypes: []refdoc.DepType{
		{Name: DepTypeTool, What: "a `[tools]` entry; the dep name comes from the tool table or the backend", Datasource: "per tool (see Tools) or backend", Versioning: "`semver`, `tag`, `pep440`, `ruby` or `maven`", Datasources: toolDatasources(dsCrate, dsGoMod)},
	},
	Skips: append(slices.Clip(toolSkips), refdoc.Skip{Reason: "cannot locate value", When: "a string with escapes or a multi-line string"}),
	Sections: []refdoc.Section{
		{Title: "Extracted", Items: []string{
			"`tool = \"v\"`, `tool = [\"v\", …]` (the first version only) and `tool = { version = \"v\", … }` in `[tools]`.",
			"Dotted keys (`tools.node = \"22\"`, `node.version = \"22\"` in `[tools]`) and `[tools.<tool>]` tables with a `version` key.",
			"Keys with a backend (tool options in `[…]` are ignored), as Renovate's mise manager maps them:",
			"`core:<tool>` uses the tool table.",
			"`github:<owner>/<repo>` and `ubi:<owner>/<repo>` use `github-releases`.",
			"`aqua:<owner>/<repo>` uses the repository's `git-tags` with extractVersion `^v?(?<version>.+)$`; a leading `v` of the version stays outside the token. A package of a repository (`aqua:ahmetb/kubectx/kubens`) uses the repository's tags.",
			"`cargo:<crate>` uses `crate` (`semver`; a partial version `tag`); `cargo:<git URL>` uses the repository's `git-tags` for a `tag:<tag>` version (`tag:` stays), `branch:`/`rev:` are `git ref`.",
			"`go:<package>` uses `go` with extractVersion `^v(?<version>.+)$`: the package (`go:github.com/golangci/golangci-lint/v2/cmd/golangci-lint`) resolves to the module that provides it, as `go install` does; a leading `v` stays outside the token; a commit hash is a `git ref`.",
			"`npm:<package>` uses `npm`.",
			"`pipx:` and `pypi:<package>` use `pypi`; `<owner>/<repo>` and `git+https://github.com/<owner>/<repo>.git` use the repository's `git-tags`; other URLs are `unsupported tool`.",
			"Other backends (`conda:`, `gem:`, `dotnet:`, `spm:`, `gitlab:`, `http:`, `vfox:`, `asdf:`, …) are skipped as `unsupported tool`.",
		}},
		{Title: "Versions", Items: toolVersionItems},
		toolsSection(),
	},
}

// toolsSection documents the tool table, sorted by tool name.
func toolsSection() refdoc.Section {
	var b strings.Builder
	b.WriteString("| Tool | Datasource | Name | Release tags |\n|---|---|---|---|\n")
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		t := tools[name]
		tags := ""
		if t.extract != "" {
			tags = "extractVersion `" + t.extract + "`"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s |\n", name, t.datasource, t.name, tags)
	}
	return refdoc.Section{
		Title: "Tools",
		Body:  strings.TrimSuffix(b.String(), "\n"),
		Items: []string{
			"github-releases and git-tags tags are the version with or without a leading `v`, unless the table lists an extractVersion; a rule's `extractVersion` overrides it.",
			"Tools that are not listed are skipped as `unsupported tool`.",
		},
	}
}
