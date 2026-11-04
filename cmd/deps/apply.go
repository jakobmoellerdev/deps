package main

import (
	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
)

func newApply(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:     "apply",
		Short:   "Write the updates to the files, including go mod tidy and package-lock.json",
		Long:    helpText("apply"),
		Example: "  deps apply && git diff --stat",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return local(cmd, o, true)
		},
	}
}

// postProcessors regenerate go.sum (go mod tidy) and package-lock.json (npm) for apply and
// run, on temporary copies.
func postProcessors() map[string]engine.PostProcessor {
	return map[string]engine.PostProcessor{gomod.Name: gotool.Tidier{}, npm.Name: nodetool.Locker{}}
}
