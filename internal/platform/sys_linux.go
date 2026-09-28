package platform

import (
	"os"
	"os/exec"
)

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

// gpuInfo reports a display controller (Vulkan can offload to it, given a
// driver): /dev/dri is checked first, since it needs no other program and
// is present whenever a GPU driver is loaded; lspci, when there is one, is
// only asked for a friendlier name.
func gpuInfo() string {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return ""
	}
	has := false
	for _, e := range entries {
		if len(e.Name()) >= 6 && e.Name()[:6] == "render" {
			has = true
			break
		}
	}
	if !has {
		return ""
	}
	if out, err := exec.Command("lspci", "-mm").Output(); err == nil {
		if name := ParseLspciVGA(string(out)); name != "" {
			return name
		}
	}
	return "GPU"
}
