//go:build !linux && !darwin && !freebsd && !windows

package platform

// DiskFreeMB is not implemented on this OS.
func DiskFreeMB(string) int { return 0 }
