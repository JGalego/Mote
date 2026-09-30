//go:build windows

package proc

import (
	"os/exec"
	"strconv"
)

// Tree makes cmd, when its context ends, kill itself and every process it has
// started. Call it before starting the command.
func Tree(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = killGrace
}
