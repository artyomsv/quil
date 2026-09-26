//go:build windows

package winjob

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modwtsapi32                     = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = modwtsapi32.NewProc("WTSQuerySessionInformationW")
)

const (
	wtsUserName   = 5
	wtsDomainName = 7
)

// InServiceSession reports whether this process runs in session 0 — where a
// daemon started over ssh or by a service lives, without the desktop's saved
// credentials and with no visible windows. Console and RDP sessions are ≥ 1.
func InServiceSession() bool {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return false
	}
	return id == 0
}

// UserHasInteractiveSession reports whether THIS user has a desktop session
// (Active, which includes a locked screen, or Disconnected). The logon task
// runs with InteractiveToken and can only start when one exists.
func UserHasInteractiveSession() (bool, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, fmt.Errorf("token user: %w", err)
	}
	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return false, fmt.Errorf("lookup account: %w", err)
	}

	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &sessions, &count); err != nil {
		return false, fmt.Errorf("WTSEnumerateSessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))

	for _, s := range unsafe.Slice(sessions, count) {
		name, err := wtsString(s.SessionID, wtsUserName)
		if err != nil {
			continue
		}
		dom, err := wtsString(s.SessionID, wtsDomainName)
		if err != nil {
			continue
		}
		if sessionQualifies(s.SessionID, s.State, name, dom, account, domain) {
			return true, nil
		}
	}
	return false, nil
}

func wtsString(session uint32, class uintptr) (string, error) {
	var buf *uint16
	var n uint32
	r, _, e := procWTSQuerySessionInformationW.Call(0, uintptr(session), class,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return "", e
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), nil
}
