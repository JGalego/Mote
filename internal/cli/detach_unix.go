//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, so it outlives the command
// that started it and a Ctrl-C there does not reach it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
