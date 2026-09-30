//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

// Tree makes cmd lead a process group and, when its context ends, kills the
// group. Call it before starting the command.
func Tree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		// A negative pid is the group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = killGrace
}
