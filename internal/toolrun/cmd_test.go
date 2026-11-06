package toolrun

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// startedWriter closes started on the first write.
type startedWriter struct {
	once    sync.Once
	started chan struct{}
}

func (w *startedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return len(p), nil
}

// TestCommandCancelReturnsDespiteHeldPipe checks that a canceled run returns even though a
// child of the killed tool keeps its output pipe open.
func TestCommandCancelReturnsDespiteHeldPipe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	ctx, stop := context.WithCancel(t.Context())
	cmd, cancel := Command(ctx, "sh", "-c", "sleep 60 & echo started; wait")
	defer cancel()
	out := &startedWriter{started: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	<-out.started
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait = nil after cancel, want an error")
		}
	case <-time.After(waitDelay + 3*time.Second):
		t.Fatal("Wait did not return after cancel: the sleep still holds the output pipe")
	}
}
