// Command deps updates dependencies in GitHub/Gitea/Forgejo Actions workflows and Go modules.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// usageError marks invalid flags or configuration (exit code 2).
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// errReported means problems were already printed; exit 1 without another message.
var errReported = errors.New("errors reported")

// newHTTPClient builds the one HTTP client of a command; tests replace it with a fake network.
var newHTTPClient = httpx.NewClient

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := newRoot().ExecuteContext(ctx)
	stop()
	os.Exit(exitCode(err, os.Stderr))
}

// exitCode prints err to w (unless already reported) and maps it to the process exit code:
// 0 success, 2 usage or config error, 1 anything else.
func exitCode(err error, w io.Writer) int {
	if err == nil {
		return 0
	}
	if !errors.Is(err, errReported) {
		fmt.Fprintf(w, "deps: %v\n", err)
	}
	var ue *usageError
	var ce *config.Error
	if errors.As(err, &ue) || errors.As(err, &ce) {
		return 2
	}
	return 1
}
