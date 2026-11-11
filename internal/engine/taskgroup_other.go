//go:build !unix

package engine

import (
	"os"
	"os/exec"
)

// taskGroup does nothing where processes have no groups: exec.CommandContext kills the command
// itself, and what it started outlives it.
func taskGroup(*exec.Cmd) {}

// killTaskGroup has no group to kill.
func killTaskGroup(*exec.Cmd) error { return os.ErrProcessDone }
