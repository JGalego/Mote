package cli

import (
	"os/exec"
	"syscall"
)

// detach starts cmd without a console and in a process group of its own,
// so it outlives the command that started it and a Ctrl-C there does not
// reach it.
func detach(cmd *exec.Cmd) {
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess, HideWindow: true}
}
