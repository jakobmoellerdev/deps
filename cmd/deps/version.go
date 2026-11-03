package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// version is set with -ldflags "-X main.version=…".
var version = "dev"

func init() { httpx.Version = version }

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the deps version",
		Args:  noArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}
