//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Tree makes cmd start a process group of its own and, when its context ends,
// kills the command and everything it started. Call it before starting the
// command.
func Tree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	cmd.WaitDelay = killGrace
}
