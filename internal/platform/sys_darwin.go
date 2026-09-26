package platform

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func sysctl(name string) string {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func totalRAMMB() int {
	n, err := strconv.ParseInt(sysctl("hw.memsize"), 10, 64)
	if err != nil {
		return 0
	}
	return int(n / (1 << 20))
}

func cpuInfo() (string, []string) {
	name := sysctl("machdep.cpu.brand_string")
	if runtime.GOARCH == "arm64" {
		feats := []string{"neon"}
		if sysctl("hw.optional.arm.FEAT_DotProd") == "1" {
			feats = append(feats, "dotprod")
		}
		if sysctl("hw.optional.arm.FEAT_I8MM") == "1" {
			feats = append(feats, "i8mm")
		}
		return name, feats
	}
	raw := strings.Fields(sysctl("machdep.cpu.features") + " " + sysctl("machdep.cpu.leaf7_features"))
	return name, filterFeatures(raw)
}
