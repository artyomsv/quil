//go:build windows

package procscan

import (
	"syscall"
	"time"
	"unsafe"
)

// Enumeration syscalls only — no classification logic lives here, because CI is
// Linux and never compiles this file. Follows internal/clipboard's NewProc
// idiom: no CGo, no new dependency.

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snap    = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW         = kernel32.NewProc("Process32FirstW")
	procProcess32NextW          = kernel32.NewProc("Process32NextW")
	procOpenProcess             = kernel32.NewProc("OpenProcess")
	procGetProcessTimes         = kernel32.NewProc("GetProcessTimes")
	procQueryFullProcessImageNm = kernel32.NewProc("QueryFullProcessImageNameW")
)

const (
	th32csSnapProcess = 0x00000002
	// PROCESS_QUERY_LIMITED_INFORMATION: enough for GetProcessTimes and the
	// image name, and obtainable for processes PROCESS_QUERY_INFORMATION is
	// refused on.
	processQueryLimitedInformation = 0x1000
	maxPath                        = 260
)

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

// Snapshot returns the current process table.
//
// Best-effort per field: a process that refuses OpenProcess keeps a zero Start
// and an image-name-only Cmdline. Classify treats an unknown start as "live",
// which is the direction that never invites the user to kill something in use.
func Snapshot() ([]Process, error) {
	h, _, err := procCreateToolhelp32Snap.Call(uintptr(th32csSnapProcess), 0)
	if syscall.Handle(h) == syscall.InvalidHandle {
		return nil, err
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	var e processEntry32
	e.Size = uint32(unsafe.Sizeof(e))

	ok, _, err := procProcess32FirstW.Call(h, uintptr(unsafe.Pointer(&e)))
	if ok == 0 {
		return nil, err
	}

	var out []Process
	for {
		name := syscall.UTF16ToString(e.ExeFile[:])
		p := Process{
			PID:  int(e.ProcessID),
			PPID: int(e.ParentProcessID),
			Name: name,
		}
		p.Start, p.Cmdline = processDetail(e.ProcessID, name)
		out = append(out, p)

		ok, _, _ := procProcess32NextW.Call(h, uintptr(unsafe.Pointer(&e)))
		if ok == 0 {
			break
		}
	}
	return out, nil
}

// processDetail reads the start time and full image path for one PID.
//
// Returns zero values rather than an error when the process refuses to open —
// system processes always do, and a diagnostic that fails wholesale because one
// process is protected is useless.
//
// The full image PATH stands in for a command line. Reading the real one needs
// NtQueryInformationProcess against the PEB of another process, which is both
// undocumented and cross-bitness-fragile; the path is enough for the matching
// this package does, since IsBridge keys on the binary name plus the subcommand
// and the subcommand comes from the ProcessEntry name for our own children.
func processDetail(pid uint32, name string) (time.Time, string) {
	h, _, _ := procOpenProcess.Call(uintptr(processQueryLimitedInformation), 0, uintptr(pid))
	if h == 0 {
		return time.Time{}, name
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	var creation, exit, kernel, user syscall.Filetime
	ok, _, _ := procGetProcessTimes.Call(h,
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	var start time.Time
	if ok != 0 {
		start = time.Unix(0, creation.Nanoseconds())
	}

	buf := make([]uint16, maxPath*2)
	size := uint32(len(buf))
	ok, _, _ = procQueryFullProcessImageNm.Call(h, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	path := name
	if ok != 0 {
		path = syscall.UTF16ToString(buf[:size])
	}
	return start, path
}
