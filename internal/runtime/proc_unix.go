//go:build !windows

package runtime

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// peakRSSMB reads the peak resident set size of a live process (Linux only;
// elsewhere exitRSSMB is used after the process ends).
func peakRSSMB(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmHWM:" {
			kb, _ := strconv.Atoi(f[1])
			return kb / 1024
		}
	}
	return 0
}

func exitRSSMB(ps *os.ProcessState) int {
	if ps == nil {
		return 0
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int(ru.Maxrss / (1 << 20)) // bytes
	}
	return int(ru.Maxrss / 1024) // KiB
}

func stop(p *os.Process) { p.Signal(syscall.SIGTERM) }
