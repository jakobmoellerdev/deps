package main

import (
	"context"
	"errors"

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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/conda"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/copier"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/deno"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradle"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradlewrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/jsonnetbundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/julia"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/mavenwrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nix"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nuget"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/paket"
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
	var into string
	return &cli.Command{
		Name:  "apply",
		Usage: "Write the updates to the files, including go mod tidy, package-lock.json and Cargo.lock",
		Metadata: map[string]any{docgen.MetaExample: "  deps apply && git diff --stat\n" +
			"  # apply to clones of remote repositories, kept in work/<owner>/<name>\n" +
			"  deps apply --repositories 'my-org/*' --into work"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return applyCmd(ctx, cmd, o, into)
		},
		Flags: append(scanFlags(o, scanResults), allowTasksFlag(o),
			repositoriesFlag(&o.repositories, "apply to"),
			&cli.StringFlag{Name: "into", Destination: &into, Usage: "directory the clones of --repositories are made in, as <into>/<owner>/<name>, and kept after the command, so the changes can be inspected, tested and committed (required with --repositories; each clone directory must not exist or be empty)"},
		),
	}
}

// applyCmd runs apply on the checkout of --dir, or on kept clones of --repositories in into.
func applyCmd(ctx context.Context, cmd *cli.Command, o *rootOpts, into string) error {
	entries := repositoryEntries(cmd, o.repositories)
	switch {
	case len(entries) == 0 && into != "":
		return &usageError{errors.New("--into needs --repositories; apply writes into the checkout of --dir")}
	case len(entries) > 0 && into == "":
		return &usageError{errors.New("--repositories needs --into: apply writes files, so its clones must be kept somewhere")}
	case len(entries) > 0:
		return applyRepositories(ctx, cmd, o, entries, into)
	}
	return local(ctx, cmd, o, true)
}

// postProcessors regenerate go.sum (go mod tidy), package-lock.json (npm), Cargo.lock
// (cargo update), uv.lock, poetry.lock, pdm.lock and Pipfile.lock (pytool), .terraform.lock.hcl
// (terraform init; next to a terragrunt.hcl only by lock file maintenance), Gemfile.lock (bundle lock), composer.lock (composer update), Manifest.toml
// (julia Pkg), pubspec.lock (dart pub), MODULE.bazel.lock (bazel mod deps, also for .bazelversion), mix.lock (mix deps.update),
// manifest.toml (gleam deps update), flake.lock (nix flake update), packages.lock.json (dotnet
// restore), conda-lock.yml (conda-lock lock), Podfile.lock (pod install), deno.lock (deno install), jsonnetfile.lock.json and
// vendor (jb update), Gradle lockfiles and verification
// metadata and the Gradle and Maven wrapper files (jvmtool) and Copier projects (copier update) on temporary copies, Chart.lock and
// Package.resolved in process and requirements files (pytool.Compiler: pip-compile/uv output
// regenerated, other hashes from the session's pypi datasource), for apply and run.
func postProcessors(s *session) map[string]engine.PostProcessor {
	hashes, _ := s.sources[pypi.Name].(pytool.Hasher)
	// One lock root lookup cache per run: the engine asks for the scope of every patched file
	// of every build, and a workspace member's lookup reads its root's manifests.
	py := pytool.Locker{Scopes: pytool.NewScopeCache()}
	return map[string]engine.PostProcessor{
		gomod.Name: gotool.Tidier{Imports: gotool.NewImportCache()}, npm.Name: nodetool.Locker{Scopes: nodetool.NewScopes()}, helm.Name: helm.Locker{}, cargo.Name: cargo.Locker{}, bundler.Name: bundler.Locker{}, composer.Name: composer.Locker{IgnorePlatformReqs: s.env.ComposerIgnorePlatformReqs},
		terraform.Name: terraform.Locker{}, terraform.TerragruntName: terraform.TerragruntLocker{}, bazel.Name: bazel.Locker{}, bazel.BazeliskName: bazel.Locker{},
		pep621.Name: py, poetry.Name: py, pipenv.Name: py,
		piprequirements.Name: pytool.Compiler{Hashes: hashes, Outputs: piprequirements.NewOutputCache()},
		gradle.Name:          jvmtool.GradleLocker{Scopes: jvmtool.NewScopeCache()}, gradlewrapper.Name: jvmtool.GradleWrapper{}, mavenwrapper.Name: jvmtool.MavenWrapper{},
		swift.Name: swiftLocker(s), julia.Name: julia.Locker{}, pub.Name: pub.Locker{}, beam.NameMix: beam.MixLocker{}, beam.NameGleam: beam.GleamLocker{},
		nix.Name: nixLocker(s), nuget.Name: nuget.Locker{Locks: nuget.NewLockCache()}, conda.Name: conda.Locker{}, paket.Name: paket.Locker{}, bundler.NameCocoaPods: bundler.PodLocker{}, deno.Name: deno.Locker{}, jsonnetbundler.Name: jsonnetbundler.Locker{}, copier.Name: copier.Updater{},
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
