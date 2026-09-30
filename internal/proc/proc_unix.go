//go:build !windows

package proc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Tree makes cmd, when its context ends, kill itself and every process it has
// started, however deep. Call it before starting the command.
func Tree(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		killTree(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = killGrace
}

// killTree kills pid and its descendants. Each round kills what is found; a
// process started while the tree was being read is found in the next.
func killTree(pid int) {
	for round := 0; round < 3; round++ {
		tree := descendants(pid)
		if round > 0 && len(tree) == 0 {
			return
		}
		// Children first, so none is left to be adopted and forgotten.
		for i := len(tree) - 1; i >= 0; i-- {
			syscall.Kill(tree[i], syscall.SIGKILL)
		}
		if round == 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// descendants lists the processes below pid, parents before their children.
func descendants(pid int) []int {
	children := map[int][]int{}
	for child, parent := range parents() {
		children[parent] = append(children[parent], child)
	}
	var out []int
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

// parents maps every process to its parent: from /proc where there is one,
// else from ps.
func parents() map[int]int {
	m := map[int]int{}
	if dirs, err := filepath.Glob("/proc/[0-9]*/stat"); err == nil && len(dirs) > 0 {
		for _, stat := range dirs {
			b, err := os.ReadFile(stat)
			if err != nil {
				continue // it ended while being read
			}
			// pid (comm) state ppid ...; comm may hold spaces and parentheses.
			i := bytes.LastIndexByte(b, ')')
			if i < 0 {
				continue
			}
			f := strings.Fields(string(b[i+1:]))
			pid, err1 := strconv.Atoi(filepath.Base(filepath.Dir(stat)))
			ppid, err2 := 0, error(nil)
			if len(f) > 1 {
				ppid, err2 = strconv.Atoi(f[1])
			}
			if err1 == nil && err2 == nil && len(f) > 1 {
				m[pid] = ppid
			}
		}
		return m
	}
	// macOS and the BSDs. ps is looked for where it always is, since the PATH
	// a command runs with may not have it.
	out, err := exec.Command("/bin/ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			m[pid] = ppid
		}
	}
	return m
}
