package main

import (
	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/pypi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
)

func newApply(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:     "apply",
		Short:   "Write the updates to the files, including go mod tidy, package-lock.json and Cargo.lock",
		Long:    helpText("apply"),
		Example: "  deps apply && git diff --stat",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return local(cmd, o, true)
		},
	}
}

// postProcessors regenerate go.sum (go mod tidy), package-lock.json (npm), Cargo.lock
// (cargo update), uv.lock, poetry.lock, pdm.lock and Pipfile.lock (pytool), .terraform.lock.hcl
// (terraform init), Gradle lockfiles and verification metadata and the Gradle and Maven wrapper
// files (jvmtool) on temporary copies, Chart.lock in process and the hashes of requirements files (pytool.Compiler:
// pip-compile/uv, else from the session's pypi datasource), for apply and run.
func postProcessors(s *session) map[string]engine.PostProcessor {
	hashes, _ := s.sources[pypi.Name].(pytool.Hasher)
	return map[string]engine.PostProcessor{
		gomod.Name: gotool.Tidier{Imports: gotool.NewImportCache()}, npm.Name: nodetool.Locker{}, helm.Name: helm.Locker{}, cargo.Name: cargo.Locker{},
		terraform.Name: terraform.Locker{},
		pep621.Name:    pytool.Locker{}, poetry.Name: pytool.Locker{}, pipenv.Name: pytool.Locker{},
		piprequirements.Name: pytool.Compiler{Hashes: hashes},
		gradle.Name:          jvmtool.GradleLocker{}, gradlewrapper.Name: jvmtool.GradleWrapper{}, mavenwrapper.Name: jvmtool.MavenWrapper{},
	}
}

// planPostProcessors are the post-processors plan runs: those that work in process, without
// tools or network (Chart.lock).
func planPostProcessors() map[string]engine.PostProcessor {
	return map[string]engine.PostProcessor{helm.Name: helm.Locker{}}
}
