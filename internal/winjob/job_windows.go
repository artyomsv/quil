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

// InKillOnCloseJob reports whether this process may sit in a job that kills its
// members when the job closes — the job Win32-OpenSSH puts each session in —
// and whether a child may break away from it.
//
// Only the innermost job is visible to the query, and an inner job can hide an
// outer kill-on-close one. So an innermost job WITHOUT kill-on-close that
// allows breakaway is still reported as a job with breakaway: the daemon may
// die with an unseen outer job, and breaking away is safe either way. Only an
// innermost job that allows neither is reported as "not in a job", because
// breakaway is impossible there and the normal spawn is the only one that can
// work. jobVerdict holds the table.
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
	inJob, breakawayOK = jobVerdict(true, info.BasicLimitInformation.LimitFlags)
	return inJob, breakawayOK, nil
}
