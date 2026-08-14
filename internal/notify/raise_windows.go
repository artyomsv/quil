//go:build windows

package notify

import (
	"fmt"
	"runtime"
	"sync"
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
	procSetActiveWindow        = user32.NewProc("SetActiveWindow")
	procSwitchToThisWindow     = user32.NewProc("SwitchToThisWindow")
	procKeybdEvent             = user32.NewProc("keybd_event")
	procGetCurrentThreadIdUser = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

const (
	swRestore = 9

	// VK_MENU is ALT. See the "inputtap" rung for why it is the only safe key
	// to synthesise into whatever the user is looking at.
	vkMenu           = 0x12
	keyEventExtended = 0x0001
	keyEventKeyUp    = 0x0002
)

// RaiseWindowFor brings the terminal window hosting pid to the foreground.
//
// Best-effort in EFFECT — a failure means the user alt-tabs, which is where
// they were before — but it reports what happened, because a raise that half
// worked is not the same as one that did nothing and the difference is exactly
// what the user feels. See the limbo case in forceForeground.
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
// raiseAttempt is one way of asking Windows to foreground a window, paired with
// the name that goes in the log when it is the one that worked.
//
// A LADDER rather than a single call, because "may this process set the
// foreground window" is a policy decision Windows makes from state we cannot
// see — whether the click's foreground rights reached us, what the foreground
// lock timeout is, which process owns the desktop right now. Measured on the
// reporting machine: a toast launched through protocol activation arrives with
// NO foreground rights, so the two documented approaches were both refused.
//
// Every rung is VERIFIED against GetForegroundWindow, which is what makes
// adding rungs safe: a technique that does nothing costs a call and reports
// failure, rather than being believed because it returned a non-zero value.
type raiseAttempt struct {
	name string
	try  func(hwnd uintptr) bool
}

// raiseLadder is ordered least- to most-intrusive. Nothing below the first rung
// runs unless the one above it was refused.
func raiseLadder() []raiseAttempt {
	return []raiseAttempt{
		// The ordinary activation. When the caller holds foreground rights this
		// is all that is needed, and it is the only rung that lets the window
		// manager run its normal path end to end.
		{"plain", func(hwnd uintptr) bool {
			procSetForegroundWindow.Call(hwnd)
			return isForeground(hwnd)
		}},
		// Borrow the input queues. Enough to be PERMITTED where the refusal is
		// about queue ownership rather than rights.
		{"attached", forceForeground},
		// What Alt+Tab uses. Undocumented but exported and stable since XP, and
		// it takes a different path through the window manager than
		// SetForegroundWindow — which is the only reason it is worth a rung.
		{"switch", func(hwnd uintptr) bool {
			procSwitchToThisWindow.Call(hwnd, 1)
			return isForeground(hwnd)
		}},
		// Last resort. One of the documented conditions for setting the
		// foreground window is having received the last input event, so a
		// synthetic key tap satisfies it where nothing else did.
		//
		// ALT is deliberate: it is the only key that is a no-op on its own in
		// essentially every application — a bare press-and-release focuses a
		// menu bar at most, and the window it lands on is the one about to lose
		// focus anyway. A printable key would be TYPED into the user's document.
		{"inputtap", func(hwnd uintptr) bool {
			procKeybdEvent.Call(vkMenu, 0, keyEventExtended, 0)
			procKeybdEvent.Call(vkMenu, 0, keyEventExtended|keyEventKeyUp, 0)
			return forceForeground(hwnd)
		}},
	}
}

func RaiseWindowFor(pid int) (string, error) {
	// EVERY call below is per-OS-THREAD: AttachThreadInput binds a specific
	// thread's input queue, GetCurrentThreadId reports the thread it runs on,
	// and the detach must name the same pair the attach did. A goroutine may be
	// rescheduled onto a different OS thread at any call boundary, so without
	// this lock the attach binds one thread, the activation runs on another and
	// the detach unbinds a pair that was never attached — see forceForeground,
	// where that produced a window in the foreground with the keyboard in
	// limbo. internal/notify already pins a thread for COM apartment state, and
	// this is the same class of requirement.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var firstErr error
	for _, cand := range ancestorPIDs(uint32(pid)) {
		hwnd := visibleTopLevelWindow(cand)
		if hwnd == 0 {
			continue
		}
		// Windows only grants SetForegroundWindow to a process that currently
		// has foreground rights. A process launched by the user clicking a
		// toast usually does — but it must hand that right to the target,
		// which is what AllowSetForegroundWindow is for.
		procAllowSetForegroundWin.Call(uintptr(cand))
		if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
			procShowWindow.Call(hwnd, swRestore)
		}
		for _, attempt := range raiseLadder() {
			if attempt.try(hwnd) {
				return attempt.name, nil
			}
		}
		// Keep walking rather than giving up here. In practice the first
		// ancestor owning a titled window IS the terminal, but the walk is what
		// makes that a finding rather than an assumption, and the failure is
		// reported against the whole chain.
		if firstErr == nil {
			firstErr = fmt.Errorf("every rung refused for owner pid %d", cand)
		}
	}
	if firstErr != nil {
		return "", fmt.Errorf("notify: raising for pid %d: %w", pid, firstErr)
	}
	return "", fmt.Errorf("notify: no visible window found for pid %d or its ancestors", pid)
}

// isForeground asks the SYSTEM whether the raise landed, rather than trusting
// the return value of the call that attempted it.
//
// SetForegroundWindow reports whether the request was ACCEPTED, which is not
// the same as the window ending up foreground with the keyboard behind it —
// the difference is invisible from inside the call and is the whole reason
// this feature was debugged three times from the outside.
func isForeground(hwnd uintptr) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	return fg == hwnd
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

// forceForeground borrows the input queues so SetForegroundWindow is permitted.
//
// MUST be called on a locked OS thread — RaiseWindowFor holds the lock for
// exactly this. Every call here is per-thread, and a goroutine that migrates
// mid-sequence leaves the attach and the detach naming different threads.
//
// BOTH queues are attached, and the target's is the one that was missing. The
// earlier version attached only the foreground thread, which is enough to be
// PERMITTED to call SetForegroundWindow but not enough for the activation to
// land in the target's own queue — so Windows Terminal came forward while the
// keyboard belonged to nobody: text typed after a toast click appeared neither
// in the raised pane nor in the app the user had been in. SetActiveWindow is
// what completes it, and it can only reach a window in an attached queue.
//
// The result is VERIFIED against the system rather than read from a return
// value, and the attachments are undone before asking, so the answer describes
// the state the user is left in.
func forceForeground(hwnd uintptr) bool {
	self, _, _ := procGetCurrentThreadIdUser.Call()

	fg, _, _ := procGetForegroundWindow.Call()
	var fgThread uintptr
	if fg != 0 {
		fgThread, _, _ = procGetWindowThreadProcID.Call(fg, 0)
	}
	// The window's own thread. NULL for the pid is legal and documented — it is
	// the thread id this needs, not the process.
	targetThread, _, _ := procGetWindowThreadProcID.Call(hwnd, 0)

	detachFg := attachInput(self, fgThread)
	defer detachFg()
	detachTarget := attachInput(self, targetThread)
	defer detachTarget()

	// Deliberately NO BringWindowToTop. It changes z-order WITHOUT activating,
	// so when the foreground call that follows is refused the window is left on
	// top of everything with the keyboard still elsewhere — which is precisely
	// the state reported as "I see the pane, I type, nothing happens anywhere".
	// A raise either takes focus or leaves the desktop as it found it; a
	// half-raise is worse than none, because it looks like it worked.
	procSetForegroundWindow.Call(hwnd)
	procSetActiveWindow.Call(hwnd)

	// Detached BEFORE the check: while queues are attached the answer describes
	// a borrowed state that ends a microsecond later, not the one the user gets.
	detachTarget()
	detachFg()
	return isForeground(hwnd)
}

// attachInput joins self to other's input queue and returns the undo, which is
// safe to call more than once. A no-op undo is returned when there is nothing
// to attach, so callers never branch on whether the attach happened.
func attachInput(self, other uintptr) func() {
	if other == 0 || other == self {
		return func() {}
	}
	if ok, _, _ := procAttachThreadInput.Call(self, other, 1); ok == 0 {
		return func() {}
	}
	var once sync.Once
	return func() {
		once.Do(func() { procAttachThreadInput.Call(self, other, 0) })
	}
}
