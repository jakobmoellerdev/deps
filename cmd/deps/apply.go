package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bazel"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/beam"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/composer"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradle"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradlewrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/julia"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/mavenwrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nix"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nuget"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pub"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/swift"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
)

func newApply(o *rootOpts) *cli.Command {
	return &cli.Command{
		Name:         "apply",
		Usage:        "Write the updates to the files, including go mod tidy, package-lock.json and Cargo.lock",
		Metadata:     map[string]any{docgen.MetaExample: "  deps apply && git diff --stat"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return local(ctx, cmd, o, true)
		},
		Flags: append(scanFlags(o, scanResults), allowTasksFlag(o)),
	}
}

// postProcessors regenerate go.sum (go mod tidy), package-lock.json (npm), Cargo.lock
// (cargo update), uv.lock, poetry.lock, pdm.lock and Pipfile.lock (pytool), .terraform.lock.hcl
// (terraform init; next to a terragrunt.hcl only by lock file maintenance), Gemfile.lock (bundle lock), composer.lock (composer update), Manifest.toml
// (julia Pkg), pubspec.lock (dart pub), MODULE.bazel.lock (bazel mod deps), mix.lock (mix deps.update),
// manifest.toml (gleam deps update), flake.lock (nix flake update), packages.lock.json (dotnet
// restore), Gradle lockfiles and verification
// metadata and the Gradle and Maven wrapper files (jvmtool) on temporary copies, Chart.lock and
// Package.resolved in process and requirements files (pytool.Compiler: pip-compile/uv output
// regenerated, other hashes from the session's pypi datasource), for apply and run.
func postProcessors(s *session) map[string]engine.PostProcessor {
	hashes, _ := s.sources[pypi.Name].(pytool.Hasher)
	return map[string]engine.PostProcessor{
		gomod.Name: gotool.Tidier{Imports: gotool.NewImportCache()}, npm.Name: nodetool.Locker{}, helm.Name: helm.Locker{}, cargo.Name: cargo.Locker{}, bundler.Name: bundler.Locker{}, composer.Name: composer.Locker{},
		terraform.Name: terraform.Locker{}, terraform.TerragruntName: terraform.TerragruntLocker{}, bazel.Name: bazel.Locker{},
		pep621.Name: pytool.Locker{}, poetry.Name: pytool.Locker{}, pipenv.Name: pytool.Locker{},
		piprequirements.Name: pytool.Compiler{Hashes: hashes},
		gradle.Name:          jvmtool.GradleLocker{}, gradlewrapper.Name: jvmtool.GradleWrapper{}, mavenwrapper.Name: jvmtool.MavenWrapper{},
		swift.Name: swiftLocker(s), julia.Name: julia.Locker{}, pub.Name: pub.Locker{}, beam.NameMix: beam.MixLocker{}, beam.NameGleam: beam.GleamLocker{},
		nix.Name: nixLocker(s), nuget.Name: nuget.Locker{},
	}
}

// planPostProcessors are the post-processors plan runs: those that work in process, without
// tools (Chart.lock, and Package.resolved with the commits of the tags the session's git-tags
// datasource listed).
func planPostProcessors(s *session) map[string]engine.PostProcessor {
	return map[string]engine.PostProcessor{helm.Name: helm.Locker{}, swift.Name: swiftLocker(s)}
}

// swiftLocker is the Package.resolved post-processor on the session's git-tags datasource.
func swiftLocker(s *session) *swift.Locker {
	return swift.NewLocker(s.sources[swift.Datasource])
}

// nixLocker is the flake.lock post-processor with the session's github.com token, which nix
// sends to the GitHub API instead of its anonymous rate limit.
func nixLocker(s *session) nix.Locker {
	_, token, _ := s.auth.Static("github.com")
	return nix.Locker{GitHubToken: token}
}
