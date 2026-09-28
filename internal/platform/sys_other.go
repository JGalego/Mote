//go:build !linux && !darwin && !windows

package platform

func totalRAMMB() int             { return 0 }
func cpuInfo() (string, []string) { return "", nil }
func gpuInfo() string             { return "" }
