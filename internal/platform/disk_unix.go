//go:build linux || darwin || freebsd

package platform

import "syscall"

// DiskFreeMB returns free space available to the user at path, or 0.
func DiskFreeMB(path string) int {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int(uint64(st.Bavail) * uint64(st.Bsize) / (1 << 20))
}
