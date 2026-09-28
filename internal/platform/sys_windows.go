package platform

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"unsafe"

	"syscall"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func totalRAMMB() int {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := kernel32.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return int(m.TotalPhys / (1 << 20))
}

func cpuInfo() (string, []string) {
	present := kernel32.NewProc("IsProcessorFeaturePresent")
	var feats []string
	for id, name := range map[uintptr]string{39: "avx", 40: "avx2", 41: "avx512"} {
		if r, _, _ := present.Call(id); r != 0 {
			feats = append(feats, name)
		}
	}
	sort.Strings(feats)
	return os.Getenv("PROCESSOR_IDENTIFIER"), feats
}

// gpuInfo asks PowerShell for the first video controller's name. Detecting
// a GPU here does not mean a Vulkan driver for it is installed.
func gpuInfo() string {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-CimInstance Win32_VideoController | Select-Object -First 1 -ExpandProperty Name)").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DiskFreeMB returns free space available to the user at path, or 0.
func DiskFreeMB(path string) int {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free uint64
	r, _, _ := kernel32.NewProc("GetDiskFreeSpaceExW").Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if r == 0 {
		return 0
	}
	return int(free / (1 << 20))
}
