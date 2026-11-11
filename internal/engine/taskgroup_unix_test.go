//go:build unix

package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A command that leaves a child behind has it killed with the task's process group: the child
// would otherwise keep writing into the tree BuildWithTasks deletes or ApplyTasks restores.
func TestRunTasksKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	// The child's own output goes to /dev/null, so the command does not wait for it; its pid
	// is recorded to clean up when the assertion below fails.
	const cmd = "{ while :; do echo x >> out; sleep 0.05; done; } >/dev/null 2>&1 & echo $! > pid"
	if err := runTasks(t.Context(), dir, []TaskRun{{Command: cmd}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b, err := os.ReadFile(filepath.Join(dir, "pid"))
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	size := func() int64 {
		fi, err := os.Stat(filepath.Join(dir, "out"))
		if err != nil {
			return 0
		}
		return fi.Size()
	}
	before := size()
	time.Sleep(300 * time.Millisecond)
	if after := size(); after != before {
		t.Errorf("the task's child kept writing after it returned: %d bytes, then %d", before, after)
	}
}
