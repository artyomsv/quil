//go:build windows

package winjob

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procIsProcessInJob = modkernel32.NewProc("IsProcessInJob")
)

// InKillOnCloseJob reports whether this process sits in a job that kills its
// members when the job closes — the job Win32-OpenSSH puts each session in —
// and whether that job lets a child break away.
//
// A job WITHOUT kill-on-close is reported as "not in a job": it cannot take
// the daemon down, so the normal spawn is right there. Only the innermost job
// is visible to the query; an outer job that forbids breakaway surfaces later
// as ERROR_ACCESS_DENIED from CreateProcess, which spawnLowered maps to
// ErrBreakawayDenied.
func InKillOnCloseJob() (inJob, breakawayOK bool, err error) {
	var in int32
	r, _, e := procIsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in)))
	if r == 0 {
		return false, false, fmt.Errorf("IsProcessInJob: %w", e)
	}
	if in == 0 {
		return false, false, nil
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return false, false, fmt.Errorf("QueryInformationJobObject: %w", err)
	}
	flags := info.BasicLimitInformation.LimitFlags
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		return false, false, nil
	}
	return true, flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0, nil
}
