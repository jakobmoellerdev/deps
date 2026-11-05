package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// version is set with -ldflags "-X main.version=…".
var version = "dev"

func init() { httpx.Version = version }

func newVersion() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the deps version",
		Description: "Prints the version set at build time with `-ldflags \"-X main.version=<v>\"` (`dev` otherwise). " +
			"Release binaries print their tag, e.g. `v0.1.0`; the action prints it after download.",
		ArgValidator: noArgs,
		Action: func(_ context.Context, cmd *cli.Command) error {
			_, err := fmt.Fprintln(cmd.Root().Writer, version)
			return err
		},
	}
}
