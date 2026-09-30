//go:build !windows

package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether a process exists and has not exited. A process that
// has exited but not been collected still answers a signal, so it is asked
// about in the process table.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.TrimSpace(string(out)) != "" && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func TestTreeKillsWhatTheCommandStarted(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The shell starts a sleep of its own and waits for it: killing only the
	// shell would leave the sleep behind.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 60 & echo $! > "+pidfile+"; wait")
	Tree(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var child int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(pidfile); err == nil && strings.TrimSpace(string(b)) != "" {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			break
		}
	}
	if child == 0 || !alive(child) {
		t.Fatalf("the sleep did not start (pid %d)", child)
	}
	t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) })

	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not stop")
	}
	for deadline := time.Now().Add(5 * time.Second); alive(child) && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	if alive(child) {
		t.Errorf("the sleep the shell started is still running (pid %d)", child)
	}
}

func TestTreeLeavesACommandThatFinishesAlone(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "echo done")
	Tree(cmd)
	out, err := cmd.Output()
	if err != nil || string(out) != "done\n" {
		t.Errorf("%q %v", out, err)
	}
}

func TestTreeDoesNotHangOnAPipeAnEscapedProcessHolds(t *testing.T) {
	// A process that left the group keeps the output pipe open; Wait must not
	// wait for it for ever.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "setsid sleep 30 & wait")
	Tree(cmd)
	start := time.Now()
	cmd.Output()
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("waited %v", took)
	}
	exec.Command("pkill", "-x", "-f", "sleep 30").Run()
}
