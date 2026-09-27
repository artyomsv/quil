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

// JobState describes the innermost job this process is in — for an ssh
// session, the job Win32-OpenSSH puts it in. Only the innermost job is
// visible, so an outer kill-on-close job can sit behind it unseen; see
// JobInfo. jobInfo holds the flag mapping.
func JobState() (JobInfo, error) {
	var in int32
	r, _, e := procIsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in)))
	if r == 0 {
		return JobInfo{}, fmt.Errorf("IsProcessInJob: %w", e)
	}
	if in == 0 {
		return JobInfo{}, nil
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return JobInfo{}, fmt.Errorf("QueryInformationJobObject: %w", err)
	}
	return jobInfo(true, info.BasicLimitInformation.LimitFlags), nil
}
