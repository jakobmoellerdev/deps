package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradle"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradlewrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/mavenwrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/swift"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
)

func newApply(o *rootOpts) *cli.Command {
	return &cli.Command{
		Name:         "apply",
		Usage:        "Write the updates to the files, including go mod tidy, package-lock.json and Cargo.lock",
		Description:  helpText("apply"),
		Metadata:     map[string]any{docgen.MetaExample: "  deps apply && git diff --stat"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return local(ctx, cmd, o, true)
		},
		Flags: []cli.Flag{allowTasksFlag(o)},
	}
}

// postProcessors regenerate go.sum (go mod tidy), package-lock.json (npm), Cargo.lock
// (cargo update), uv.lock, poetry.lock, pdm.lock and Pipfile.lock (pytool), .terraform.lock.hcl
// (terraform init), Gemfile.lock (bundle lock), Gradle lockfiles and verification metadata and
// the Gradle and Maven wrapper files (jvmtool) on temporary copies, Chart.lock and
// Package.resolved in process and requirements files (pytool.Compiler: pip-compile/uv output
// regenerated, other hashes from the session's pypi datasource), for apply and run.
func postProcessors(s *session) map[string]engine.PostProcessor {
	hashes, _ := s.sources[pypi.Name].(pytool.Hasher)
	return map[string]engine.PostProcessor{
		gomod.Name: gotool.Tidier{Imports: gotool.NewImportCache()}, npm.Name: nodetool.Locker{}, helm.Name: helm.Locker{}, cargo.Name: cargo.Locker{}, bundler.Name: bundler.Locker{},
		terraform.Name: terraform.Locker{},
		pep621.Name:    pytool.Locker{}, poetry.Name: pytool.Locker{}, pipenv.Name: pytool.Locker{},
		piprequirements.Name: pytool.Compiler{Hashes: hashes},
		gradle.Name:          jvmtool.GradleLocker{}, gradlewrapper.Name: jvmtool.GradleWrapper{}, mavenwrapper.Name: jvmtool.MavenWrapper{},
		swift.Name: swiftLocker(s),
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
