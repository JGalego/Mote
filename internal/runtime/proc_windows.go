package runtime

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// peakRSSMB returns the peak working set of a live process.
func peakRSSMB(pid int) int {
	const queryLimited = 0x1000
	h, err := syscall.OpenProcess(queryLimited, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(h)
	var c processMemoryCounters
	c.CB = uint32(unsafe.Sizeof(c))
	if r, _, _ := procMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.CB)); r == 0 {
		return 0
	}
	return int(c.PeakWorkingSetSize / (1 << 20))
}

func exitRSSMB(*os.ProcessState) int { return 0 }

func stop(p *os.Process) { p.Kill() }
