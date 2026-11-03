package main

import (
	"context"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
)

func newApply(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Write the updates to the files, including go mod tidy",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return local(cmd, o, true)
		},
	}
}

// goTidier runs the real `go mod tidy` on a temporary copy of go.mod/go.sum.
type goTidier struct{}

func (goTidier) Tidy(ctx context.Context, dir string, mod, sum []byte) ([]byte, []byte, error) {
	return gotool.Tidy(ctx, dir, mod, sum)
}
