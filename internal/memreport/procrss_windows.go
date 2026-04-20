//go:build windows

package memreport

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS from psapi.h.
// Only WorkingSetSize is consumed here.
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

var (
	modpsapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo    = modpsapi.NewProc("GetProcessMemoryInfo")
	processQueryLimitedInfoFlag = uint32(0x1000) // PROCESS_QUERY_LIMITED_INFORMATION
)

func procRSSBatch(pids []int) map[int]uint64 {
	out := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		if rss, ok := getWorkingSet(uint32(pid)); ok {
			out[pid] = rss
		}
	}
	return out
}

func getWorkingSet(pid uint32) (uint64, bool) {
	h, err := windows.OpenProcess(processQueryLimitedInfoFlag, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)

	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&pmc)),
		uintptr(pmc.CB),
	)
	if r == 0 {
		return 0, false
	}
	return uint64(pmc.WorkingSetSize), true
}
