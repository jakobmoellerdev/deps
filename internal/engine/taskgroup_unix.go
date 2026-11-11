//go:build unix

package engine

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// taskGroup starts cmd in a process group of its own and kills that group when the command's
// context ends. Without it a timed out or canceled run only loses its shell: exec.Cmd signals
// the shell alone, and the make, npm or go generate it started keeps running, and writing into
// the temporary tree BuildWithTasks deletes, or into the checkout ApplyTasks restores files in.
func taskGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killTaskGroup(cmd) }
}

// killTaskGroup kills what is left of cmd's process group, which taskGroup made a group of its
// own (so the signal never reaches deps itself). It reports os.ErrProcessDone when the group
// is gone, which keeps exec.Cmd on the command's own exit status.
func killTaskGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return os.ErrProcessDone
}
