//go:build windows

package proctree

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Enumeration syscalls only. CI is Linux and never compiles this file, so the
// logic it carries is kept to the minimum the platform genuinely requires.
//
// Uses windows.NewLazySystemDLL rather than syscall.NewLazyDLL, matching
// internal/memreport/procrss_windows.go — the system variant resolves only from
// the system directory.

var (
	modkernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snap   = modkernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW        = modkernel32.NewProc("Process32FirstW")
	procProcess32NextW         = modkernel32.NewProc("Process32NextW")
	procGetProcessTimesFn      = modkernel32.NewProc("GetProcessTimes")
	th32csSnapProcess          = uint32(0x00000002)
	processQueryLimitedInfoAcc = uint32(0x00001000)
)

const maxPath = 260

// processEntry32 mirrors PROCESSENTRY32W.
//
// Note what is NOT here: a creation time. There is no Windows call that returns
// a process table WITH start times — GetProcessTimes needs an open handle, per
// process. That is why this platform needs two passes; see enrichStarts.
type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [maxPath]uint16
}

// tableHasStarts is false: see enrichStarts.
const tableHasStarts = false

// cpuIsSampled reports whether the CPU figure is a delta over our own window.
const cpuIsSampled = true

// readTable returns PID, PPID and image name for every process.
//
// Deliberately does NOT open a handle per process. The removed attempt called
// OpenProcess + GetProcessTimes + QueryFullProcessImageNameW for every process
// on the machine on every tick; at a few hundred processes that is a few
// hundred handle opens every five seconds, forever.
func readTable() ([]ProcessEntry, error) {
	h, _, err := procCreateToolhelp32Snap.Call(uintptr(th32csSnapProcess), 0)
	if h == uintptr(windows.InvalidHandle) {
		return nil, err
	}
	defer windows.CloseHandle(windows.Handle(h))

	var e processEntry32
	e.Size = uint32(unsafe.Sizeof(e))

	ok, _, err := procProcess32FirstW.Call(h, uintptr(unsafe.Pointer(&e)))
	if ok == 0 {
		return nil, err
	}

	var out []ProcessEntry
	for {
		out = append(out, ProcessEntry{
			PID:  int(e.ProcessID),
			PPID: int(e.ParentProcessID),
			Name: windows.UTF16ToString(e.ExeFile[:]),
		})
		next, _, _ := procProcess32NextW.Call(h, uintptr(unsafe.Pointer(&e)))
		if next == 0 {
			break
		}
	}
	return out, nil
}

// enrichStarts fills Start for the given PIDs only — the second pass.
//
// The chicken-and-egg this resolves: Build's splice rejection needs start times
// to decide which parent links are real, but which PIDs are pane descendants is
// only known after building a tree. The collector therefore builds a TENTATIVE
// tree from PPID links alone, hands those PIDs here, and rebuilds. The handle
// count is bounded by one pane's descendants rather than by the machine.
//
// A PID whose handle cannot be opened keeps a zero Start, which every consumer
// already treats as unknown.
func enrichStarts(table []ProcessEntry, pids []int) []ProcessEntry {
	if len(pids) == 0 {
		return table
	}
	want := make(map[int]bool, len(pids))
	for _, p := range pids {
		want[p] = true
	}
	for i := range table {
		if !want[table[i].PID] {
			continue
		}
		if start, ok := processStart(uint32(table[i].PID)); ok {
			table[i].Start = start
		}
	}
	return table
}

// processStart reads one process's creation time.
func processStart(pid uint32) (time.Time, bool) {
	h, err := windows.OpenProcess(processQueryLimitedInfoAcc, false, pid)
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	return handleStart(h)
}

// handleStart reads the creation time from an ALREADY OPEN handle.
//
// Split out because the kill path needs exactly this: open once, verify the
// identity on the handle, then terminate the same handle. Re-opening between
// the check and the kill would reintroduce the PID-reuse window that
// verification exists to close.
func handleStart(h windows.Handle) (time.Time, bool) {
	var creation, exit, kernel, user windows.Filetime
	ok, _, _ := procGetProcessTimesFn.Call(uintptr(h),
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ok == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}

// readCPU returns cumulative kernel+user CPU time per PID.
func readCPU(pids []int) cpuReading {
	out := make(map[int]time.Duration, len(pids))
	for _, pid := range pids {
		h, err := windows.OpenProcess(processQueryLimitedInfoAcc, false, uint32(pid))
		if err != nil {
			continue
		}
		var creation, exit, kernel, user windows.Filetime
		ok, _, _ := procGetProcessTimesFn.Call(uintptr(h),
			uintptr(unsafe.Pointer(&creation)),
			uintptr(unsafe.Pointer(&exit)),
			uintptr(unsafe.Pointer(&kernel)),
			uintptr(unsafe.Pointer(&user)),
		)
		if ok != 0 {
			// Filetime intervals are 100 ns.
			total := filetimeDuration(kernel) + filetimeDuration(user)
			out[pid] = total
		}
		if err := windows.CloseHandle(h); err != nil {
			// Nothing useful to do; the next tick reopens regardless.
			_ = err
		}
	}
	return cpuReading{Cumulative: out, Supported: true}
}

func filetimeDuration(ft windows.Filetime) time.Duration {
	v := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	return time.Duration(v) * 100
}
