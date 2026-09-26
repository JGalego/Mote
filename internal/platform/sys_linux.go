package platform

import "os"

func totalRAMMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return ParseMeminfo(string(b))
}

func cpuInfo() (string, []string) {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", nil
	}
	return ParseCPUInfo(string(b))
}
