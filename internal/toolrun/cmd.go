package toolrun

import (
	"context"
	"os/exec"
	"time"
)

const (
	// timeout bounds one tool run, so a hung tool cannot hold a post-processor slot forever.
	timeout = 30 * time.Minute
	// waitDelay bounds the wait for a killed tool's children (node, Python build backends,
	// git, Gradle daemons) to release its output pipes: Wait copies output written to a
	// non-*os.File writer and returns only once every holder of the pipe has closed it.
	waitDelay = 5 * time.Second
)

// Command returns exec.CommandContext(ctx, name, args...), killed after timeout and waiting
// at most waitDelay for the killed tool's output pipes. The caller calls cancel once the
// command is done.
func Command(ctx context.Context, name string, args ...string) (cmd *exec.Cmd, cancel context.CancelFunc) {
	ctx, cancel = context.WithTimeout(ctx, timeout)
	cmd = exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	return cmd, cancel
}
