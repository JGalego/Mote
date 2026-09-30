//go:build !windows

package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
)

func TestStoppingAShellStageStopsWhatItStarted(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: io.Discard}
	pidfile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.runShell(ctx, stage{text: "sh: sleeper", shell: "sleep 60 & echo $! > " + pidfile + "; wait"}, task.Value{})
		done <- err
	}()
	var child int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(pidfile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			if child > 0 {
				break
			}
		}
	}
	if child == 0 {
		t.Fatal("the stage did not start its command")
	}
	t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a stage that was stopped succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stage did not stop")
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(child, 0) != nil {
			return
		}
		// Gone, or a zombie nobody has collected yet.
		if out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(child)).Output(); strings.HasPrefix(strings.TrimSpace(string(out)), "Z") || strings.TrimSpace(string(out)) == "" {
			return
		}
	}
	t.Errorf("what the stage started (pid %d) is still running", child)
}
