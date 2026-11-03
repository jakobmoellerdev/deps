// Command deps updates dependencies in GitHub/Gitea/Forgejo Actions workflows and Go modules.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// usageError marks invalid flags or configuration (exit code 2).
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// errReported means problems were already printed; exit 1 without another message.
var errReported = errors.New("errors reported")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := newRoot().ExecuteContext(ctx)
	stop()
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if !errors.Is(err, errReported) {
		fmt.Fprintf(os.Stderr, "deps: %v\n", err)
	}
	var ue *usageError
	var ce *config.Error
	if errors.As(err, &ue) || errors.As(err, &ce) {
		return 2
	}
	return 1
}
