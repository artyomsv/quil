//go:build windows

package notify

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procEnumWindows            = user32.NewProc("EnumWindows")
	procGetWindowThreadProcID  = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procShowWindow             = user32.NewProc("ShowWindow")
	procIsIconic               = user32.NewProc("IsIconic")
	procAllowSetForegroundWin  = user32.NewProc("AllowSetForegroundWindow")
	procGetWindowTextLengthW   = user32.NewProc("GetWindowTextLengthW")
	procAttachThreadInput      = user32.NewProc("AttachThreadInput")
	procGetForegroundWindow    = user32.NewProc("GetForegroundWindow")
	procGetCurrentThreadIdUser = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

const (
	swRestore = 9
)

// RaiseWindowFor brings the terminal window hosting pid to the foreground.
//
// Best-effort by design: a failure means the user alt-tabs, which is exactly
// where they were before. It never reports an error upward for that reason.
//
// The window does not belong to the quil process. Quil runs inside a terminal —
// under Windows Terminal, GetConsoleWindow returns a hidden ConPTY
// "PseudoConsoleWindow" rather than anything the user can see (the same trap
// window_windows.go documents for size restore). So this walks UP the process
// tree from the TUI and looks for a visible top-level window owned by any
// ancestor: quil-dev.exe -> pwsh.exe -> WindowsTerminal.exe, and it is the last
// of those that owns the window.
//
// Known ceiling: Windows Terminal hosts many tabs in one window and exposes no
// way to select one, so this raises the WINDOW. If quil shares a WT window with
// other tabs, the user may still land on a different tab.
func RaiseWindowFor(pid int) {
	// Windows only grants SetForegroundWindow to a process that currently has
	// foreground rights. A process launched by the user clicking a toast does —
	// but it must hand that right to the target, which is what
	// AllowSetForegroundWindow is for.
	for _, cand := range ancestorPIDs(uint32(pid)) {
		hwnd := visibleTopLevelWindow(cand)
		if hwnd == 0 {
			continue
		}
		procAllowSetForegroundWin.Call(uintptr(cand))
		if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
			procShowWindow.Call(hwnd, swRestore)
		}
		if ok, _, _ := procSetForegroundWindow.Call(hwnd); ok != 0 {
			return
		}
		// SetForegroundWindow is refused when the caller has lost foreground
		// rights (a slow click, a stolen focus). Attaching to the foreground
		// thread's input queue is the documented way to borrow them.
		if forceForeground(hwnd) {
			return
		}
	}
}

// ancestorPIDs returns pid followed by its parents, nearest first.
//
// Bounded rather than looping to the root: a corrupt or racing snapshot can
// present a cycle, and this runs in a process spawned by a toast click where a
// hang is indistinguishable from the click doing nothing.
func ancestorPIDs(pid uint32) []uint32 {
	const maxDepth = 8
	out := []uint32{pid}

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)

	parents := make(map[uint32]uint32, 256)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return out
	}
	for {
		parents[e.ProcessID] = e.ParentProcessID
		if err := windows.Process32Next(snap, &e); err != nil {
			break
		}
	}

	seen := map[uint32]bool{pid: true}
	cur := pid
	for i := 0; i < maxDepth; i++ {
		p, ok := parents[cur]
		if !ok || p == 0 || seen[p] {
			break
		}
		seen[p] = true
		out = append(out, p)
		cur = p
	}
	return out
}

// visibleTopLevelWindow finds a visible, titled top-level window owned by pid.
//
// The title check is what skips the ConPTY ghost: it is marked visible (sitting
// at a zero rect) but carries no caption, so IsWindowVisible alone is not
// enough — the same reason window_windows.go discriminates by window class.
func visibleTopLevelWindow(pid uint32) uintptr {
	var found uintptr
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var owner uint32
		procGetWindowThreadProcID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner != pid {
			return 1 // keep enumerating
		}
		if vis, _, _ := procIsWindowVisible.Call(hwnd); vis == 0 {
			return 1
		}
		if n, _, _ := procGetWindowTextLengthW.Call(hwnd); n == 0 {
			return 1
		}
		found = hwnd
		return 0 // stop
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// forceForeground borrows the foreground thread's input queue so
// SetForegroundWindow is permitted. Falls back cleanly when it is not.
func forceForeground(hwnd uintptr) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	var fgPID uint32
	fgThread, _, _ := procGetWindowThreadProcID.Call(fg, uintptr(unsafe.Pointer(&fgPID)))
	self, _, _ := procGetCurrentThreadIdUser.Call()
	if fgThread == 0 || fgThread == self {
		return false
	}
	if ok, _, _ := procAttachThreadInput.Call(self, fgThread, 1); ok == 0 {
		return false
	}
	defer procAttachThreadInput.Call(self, fgThread, 0)
	res, _, _ := procSetForegroundWindow.Call(hwnd)
	return res != 0
}
