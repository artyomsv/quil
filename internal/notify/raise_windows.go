//go:build windows

package notify

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procEnumWindows           = user32.NewProc("EnumWindows")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procAllowSetForegroundWin = user32.NewProc("AllowSetForegroundWindow")
	procGetWindowTextLengthW  = user32.NewProc("GetWindowTextLengthW")
	procAttachThreadInput     = user32.NewProc("AttachThreadInput")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	procSetActiveWindow       = user32.NewProc("SetActiveWindow")
	procSwitchToThisWindow    = user32.NewProc("SwitchToThisWindow")
	procSystemParametersInfo  = user32.NewProc("SystemParametersInfoW")
	procRegisterHotKey        = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey      = user32.NewProc("UnregisterHotKey")
	procSendInput             = user32.NewProc("SendInput")
	procPeekMessageW          = user32.NewProc("PeekMessageW")
	procGetClassNameW         = user32.NewProc("GetClassNameW")

	kernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procGetCurrentThreadIdUser = kernel32.NewProc("GetCurrentThreadId")
	procGetConsoleWindow       = kernel32.NewProc("GetConsoleWindow")
	procFreeConsole            = kernel32.NewProc("FreeConsole")
)

const (
	swRestore = 9
	swHide    = 0

	// SPI_GETFOREGROUNDLOCKTIMEOUT. See ForegroundLockTimeout.
	spiGetForegroundLockTimeout = 0x2000

	// F22 exists on no keyboard anyone owns and no application binds it, which
	// is precisely why it is the key to synthesise. See foregroundViaHotkey.
	vkF22 = 0x85

	inputKeyboard = 1
	keyEventKeyUp = 0x0002

	wmHotkey = 0x0312
	pmRemove = 0x0001

	// hotkeyID is scoped to this thread — RegisterHotKey with a NULL window
	// registers against the calling thread's queue, so it cannot collide with
	// another process's id.
	hotkeyID = 0x4051

	// hotkeyWait bounds the wait for our own injected key to come back. It
	// returns in microseconds when it works; this exists so a swallowed
	// injection cannot hang a click.
	hotkeyWait = 400 * time.Millisecond
	hotkeyPoll = 5 * time.Millisecond
)

// keyboardInput is Win32 INPUT holding a KEYBDINPUT.
//
// Laid out by hand because the C type is a union sized to its LARGEST member
// (MOUSEINPUT), not to the member being used. On amd64 that makes INPUT 40
// bytes, where the fields this needs occupy 32 — and SendInput validates the
// cb argument against its own idea of the size, sending NOTHING and reporting
// success-with-zero-events when they disagree. A silently unsent keystroke here
// would look exactly like the refusal this whole mechanism exists to defeat.
type keyboardInput struct {
	typ     uint32
	_       uint32 // union alignment
	wVk     uint16
	wScan   uint16
	dwFlags uint32
	time    uint32
	_       uint32 // alignment before the pointer-sized field
	dwExtra uintptr
	_       [8]byte // pad out to the size of the MOUSEINPUT arm
}

// Compile-time size assertion. A wrong layout is otherwise invisible: the call
// succeeds, no key is sent, and the raise simply never works. This is checked by
// the GOOS=windows build in CI, which is the only place these files compile.
var _ = [1]struct{}{}[unsafe.Sizeof(keyboardInput{})-40]

// msgW is Win32 MSG (48 bytes on amd64).
type msgW struct {
	hwnd    uintptr
	message uint32
	_       uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
	_       uint32
}

var _ = [1]struct{}{}[unsafe.Sizeof(msgW{})-48]

// raiseAttempt is one way of asking Windows to foreground a window, paired with
// the name that goes in the log when it is the one that worked.
//
// A LADDER rather than a single call, because whether a process MAY set the
// foreground window depends on state it cannot read. SetForegroundWindow's
// documented rules are two groups that must BOTH hold: a set of hard gates
// (no menus active, and the foreground process has not called
// LockSetForegroundWindow) and a set of ways to qualify (be the foreground
// process, be started by it, have received the last input event, or have the
// foreground lock timeout expired).
//
// Getting that structure wrong cost this feature several rounds. The timeout is
// only ever about the SECOND group, so setting it to zero — measured, on the
// reporting machine — changes nothing while the shell holds the lock, which it
// does at exactly the moment a toast is clicked. Retrying cannot help either:
// the lock is released by user INPUT, not by elapsed time.
//
// Every rung is VERIFIED against GetForegroundWindow, which is what makes
// adding rungs safe: a technique that does nothing costs a call and reports
// failure, rather than being believed because it returned a non-zero value.
// try reports success AND why not, because the four ways this can fail need
// four different fixes and are indistinguishable from the outside. A single
// "refused" is what turned each of the last several diagnoses into guesswork.
type raiseAttempt struct {
	name string
	try  func(hwnd uintptr) (bool, string)
}

const (
	// shellHandoverWait is how long to wait for the notification UI to give the
	// foreground back before trying to take it. Generous, because the cost of
	// waiting is invisible (this process has no window) while the cost of
	// giving up early is the whole feature.
	shellHandoverWait = 2500 * time.Millisecond
	shellHandoverPoll = 60 * time.Millisecond
)

// isModernShellSurface reports whether hwnd is one of the UWP surfaces the
// shell uses for notifications.
//
// This is the thing that was refusing us, and it took six rounds to see because
// it is not a permission at all. SetForegroundWindow cannot take the foreground
// FROM a modern application, full stop — no lock to clear, no right to acquire,
// no input to generate. Measured at the moment of a real toast click: the
// foreground was hwnd=5505836 class="Windows.UI.Core.CoreWindow", and it stayed
// that way through every rung, including one where the injected hotkey was
// confirmed delivered.
//
// So the question was never "how do we win the foreground", it was "when is it
// ours to take". ApplicationFrameWindow is included because it hosts UWP content
// in a Win32 frame and behaves the same way.
func isModernShellSurface(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	var buf [64]uint16
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	switch windows.UTF16ToString(buf[:]) {
	case "Windows.UI.Core.CoreWindow", "ApplicationFrameWindow":
		return true
	}
	return false
}

// waitForForegroundHandover blocks until the notification UI stops holding the
// foreground, and reports how long that took.
//
// Polling rather than an event, because there is no notification for "the shell
// finished dismissing"; and bounded, because the user may genuinely have been
// working IN a UWP application when the toast arrived, in which case the
// foreground never becomes takeable and the attempt below simply fails as it
// did before. Waiting costs nothing visible either way — this process has no
// window and the pane routing has already been delivered.
func waitForForegroundHandover() time.Duration {
	start := time.Now()
	for time.Since(start) < shellHandoverWait {
		if !isModernShellSurface(currentForeground()) {
			break
		}
		time.Sleep(shellHandoverPoll)
	}
	return time.Since(start).Truncate(time.Millisecond)
}

// describeWindow names a window well enough to identify who is holding the
// foreground when a click is delivered — the one fact that is invisible from
// inside a process being refused, and the one that says whether the refusal is
// coming from the shell, from the terminal itself, or from something else.
func describeWindow(hwnd uintptr) string {
	if hwnd == 0 {
		return "none"
	}
	var pid uint32
	procGetWindowThreadProcID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	buf := make([]uint16, 128)
	procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return fmt.Sprintf("hwnd=%d pid=%d class=%q", hwnd, pid, windows.UTF16ToString(buf))
}

// foregroundViaHotkey satisfies both groups at once by generating real input.
//
// It registers a hotkey for a key nobody has, injects that key with SendInput,
// waits for the WM_HOTKEY it just caused, and only then calls
// SetForegroundWindow. The injected keystroke is genuine user input as far as
// the window manager is concerned, so it RELEASES the foreground lock (first
// group) and makes this thread the one that received the last input event
// (second group). Chromium does the same thing in
// ui/base/win/foreground_helper.cc, and this is the technique behind most
// desktop applications that manage to raise themselves from a notification.
//
// F22 is the key because no keyboard has one and nothing binds it. An earlier
// version of this file injected ALT instead, which failed for two independent
// reasons: it opens the MENU BAR of whatever window has focus — swallowing the
// user's next keystrokes — and an active menu violates the first group outright,
// so the rung guaranteed its own refusal. That failure was recorded here as
// "synthetic input does not work", which was wrong and pointed the next four
// attempts in the wrong direction. Only ALT did not work.
//
// Waiting for the hotkey to come BACK is what makes this ordered rather than
// hopeful: SendInput is asynchronous, so calling SetForegroundWindow straight
// after it races the input it depends on.
//
// Must run on the locked OS thread RaiseWindowFor pins — RegisterHotKey and the
// message queue both belong to the calling thread.
func foregroundViaHotkey(hwnd uintptr) (bool, string) {
	if ok, _, err := procRegisterHotKey.Call(0, hotkeyID, 0, vkF22); ok == 0 {
		return false, fmt.Sprintf("RegisterHotKey failed (%v)", err)
	}
	defer procUnregisterHotKey.Call(0, hotkeyID)

	inputs := [2]keyboardInput{
		{typ: inputKeyboard, wVk: vkF22},
		{typ: inputKeyboard, wVk: vkF22, dwFlags: keyEventKeyUp},
	}
	// A blocked injection is the one failure that says the technique itself is
	// unavailable rather than merely unlucky: SendInput refuses when the
	// foreground window belongs to a process at a higher integrity level.
	sent, _, err := procSendInput.Call(2, uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	if sent != 2 {
		return false, fmt.Sprintf("SendInput sent %d of 2 (%v)", sent, err)
	}

	// PeekMessage on a deadline rather than a blocking GetMessage: this runs in
	// a process spawned by a click, where a wait that cannot end is
	// indistinguishable from the click doing nothing.
	deadline := time.Now().Add(hotkeyWait)
	for time.Now().Before(deadline) {
		var m msgW
		if got, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove); got != 0 {
			if m.message != wmHotkey {
				continue
			}
			// The injection landed. Whether the raise is now PERMITTED is the
			// separate question, and distinguishing the two is the whole point
			// of reporting this far.
			r, _, sfwErr := procSetForegroundWindow.Call(hwnd)
			if isForeground(hwnd) {
				return true, ""
			}
			return false, fmt.Sprintf("hotkey delivered but SetForegroundWindow returned %d (%v), foreground is %s",
				r, sfwErr, describeWindow(currentForeground()))
		}
		time.Sleep(hotkeyPoll)
	}
	return false, fmt.Sprintf("injected F22 never came back within %s", hotkeyWait)
}

// raiseLadder is ordered least- to most-intrusive. Nothing below the first rung
// runs unless the one above it was refused.
func raiseLadder() []raiseAttempt {
	return []raiseAttempt{
		// The ordinary activation. When the caller holds foreground rights this
		// is all that is needed, and it is the only rung that lets the window
		// manager run its normal path end to end.
		{"plain", func(hwnd uintptr) (bool, string) {
			r, _, err := procSetForegroundWindow.Call(hwnd)
			if isForeground(hwnd) {
				return true, ""
			}
			return false, fmt.Sprintf("returned %d (%v), foreground is %s", r, err, describeWindow(currentForeground()))
		}},
		// Generate real input so the foreground lock is released and this thread
		// owns the last input event. The rung that actually works at toast-click
		// time; everything below it is a fallback for when RegisterHotKey fails.
		{"hotkey", foregroundViaHotkey},
		// Borrow the input queues. Enough to be PERMITTED where the refusal is
		// about queue ownership rather than rights.
		{"attached", forceForeground},
		// What Alt+Tab uses. Undocumented but exported and stable since XP, and
		// it takes a different path through the window manager than
		// SetForegroundWindow — which is the only reason it is worth a rung.
		{"switch", func(hwnd uintptr) (bool, string) {
			procSwitchToThisWindow.Call(hwnd, 1)
			if isForeground(hwnd) {
				return true, ""
			}
			return false, "refused"
		}},
		// A synthetic ALT tap was tried here and REMOVED. The theory was sound —
		// one documented condition for setting the foreground window is having
		// received the last input event — but measured on the reporting machine
		// it was refused like every other rung, and it is not side-effect free:
		// ALT alone activates the MENU BAR of whatever window has focus, which
		// then swallows the keystrokes that follow. So a user who clicked the
		// toast and started typing got nothing, from a rung that was supposed
		// to be the one that finally worked.
		//
		// "Harmless if it fails" is a property to verify, not assume, and it is
		// the property that decides whether a speculative rung may exist at all.
	}
}

const (
	// raiseRounds is how many times the WHOLE ladder is re-run if it fails.
	//
	// Small on purpose. An earlier version retried the plain call alone for
	// 1.2 s, which was built on a wrong model of the barrier: the foreground
	// lock is cleared by user INPUT, not by elapsed time, so retrying an
	// identical call generated nothing that could change the answer — and,
	// worse, it delayed the one rung that does work behind more than a second
	// of calls that cannot. What the rounds cover now is narrow and real: the
	// shell may take the foreground back as the notification dismisses, after
	// we have already won it.
	raiseRounds = 3

	// raiseRetryInterval spaces the rounds. Long enough for a dismissal to
	// finish, short enough that a user who clicked cannot perceive it.
	raiseRetryInterval = 150 * time.Millisecond
)

// raiseWithSettle runs the whole ladder, re-running it a few times if the
// window does not stay in front. It returns the rung that worked, or "".
//
// The WHOLE ladder each round, not the first rung repeatedly. The rungs are
// ordered cheapest-and-most-normal first, and the plain call costs nothing and
// has no side effects when refused — but the rung that actually works at
// toast-click time is the hotkey one, and an earlier version could not reach it
// until it had spent 1.2 s repeating a call that was structurally incapable of
// succeeding.
//
// The rounds exist for one narrow reason: the shell may take the foreground
// back while the notification finishes dismissing, after we have already won
// it. Re-running the hotkey rung is safe — F22 is inert, and the injection is
// what makes the next attempt permitted at all.
//
// The round count is reported when more than one was needed, because "worked
// immediately" and "worked on the third round" are different findings about
// what the shell is doing after a click.
func raiseWithSettle(hwnd uintptr) (string, string) {
	start := time.Now()
	// Notes from the FIRST round only. Later rounds repeat the same rungs
	// against the same state, so keeping them all would bury the answer in
	// duplicates.
	var notes []string
	for round := 1; round <= raiseRounds; round++ {
		for _, attempt := range raiseLadder() {
			ok, why := attempt.try(hwnd)
			if ok {
				if round == 1 {
					return attempt.name, ""
				}
				return fmt.Sprintf("%s (round %d, %s)", attempt.name, round, time.Since(start).Truncate(time.Millisecond)), ""
			}
			if round == 1 {
				notes = append(notes, attempt.name+": "+why)
			}
		}
		if round < raiseRounds {
			time.Sleep(raiseRetryInterval)
		}
	}
	return "", strings.Join(notes, "; ")
}

// RaiseWindowFor brings the terminal window hosting pid to the foreground and
// reports which rung of the ladder managed it.
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

	entryForeground := currentForeground()

	// BEFORE anything is attempted: a click is delivered while the notification
	// UI still owns the foreground, and the foreground cannot be taken from a
	// modern application at all. Every rung run inside that window is refused
	// identically no matter how well it is implemented — which is exactly what
	// the logs showed for six rounds, including a rung whose injected keystroke
	// was confirmed delivered.
	handover := waitForForegroundHandover()

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
		how, why := raiseWithSettle(hwnd)
		if how != "" {
			// The wait is reported on SUCCESS too. It is the difference between
			// "the shell had already let go" and "we waited 400 ms for it",
			// and only the second confirms why this works at all.
			return fmt.Sprintf("%s after %s handover", how, handover), nil
		}
		// Keep walking rather than giving up here. In practice the first
		// ancestor owning a titled window IS the terminal, but the walk is what
		// makes that a finding rather than an assumption, and the failure is
		// reported against the whole chain.
		if firstErr == nil {
			// The foreground at ENTRY is recorded because it names who is
			// refusing. Every previous round of this diagnosis was spent
			// guessing at that from the outside.
			firstErr = fmt.Errorf("owner pid %d, foreground on entry was %s, waited %s for handover, foreground then was %s — %s",
				cand, describeWindow(entryForeground), handover, describeWindow(currentForeground()), why)
		}
	}
	if firstErr != nil {
		return "", fmt.Errorf("notify: raising for pid %d: %w", pid, firstErr)
	}
	return "", fmt.Errorf("notify: no visible window found for pid %d or its ancestors", pid)
}

// DetachOwnConsole hides and releases the console window Windows allocates to
// this process, and must be the FIRST thing the activation handler does.
//
// `quil activate` is a console binary, so launching it for a toast click
// allocates it a console WINDOW — a real, visible, focusable window that
// appears in front of the user and takes the foreground. The handler was then
// competing with itself: it asked for the terminal to be raised, its own
// console took the foreground back, the verification read that as a refusal,
// and when the process exited the console vanished and left focus nowhere. That
// is the "I clicked the toast and my typing went to neither window" report, and
// it is also why every technique in the ladder looked refused.
//
// Invisible for its whole life is the only correct behaviour for a process
// spawned by a click: it has no output, no prompt and nothing to show. Hidden
// BEFORE being freed so the window cannot flash — freeing alone destroys it,
// but not before it has been painted.
//
// After this the process has no stdout or stderr. Nothing in the activation
// path prints; it reports through notify-activate.log precisely because it has
// no terminal to report to.
func DetachOwnConsole() {
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
	procFreeConsole.Call()
}

// ForegroundLockTimeout reports how long after user input Windows refuses to
// let an application force itself into the foreground.
//
// This is the policy behind every refusal the raise ladder can hit, and it is
// per-USER rather than anything Quil can see from its own state — which is why
// a raise can be impossible on one machine and instant on another with the same
// build. Non-zero means an application that does not already own the user's
// most recent interaction cannot take focus, full stop, and a toast click
// handled by the shell's notification host leaves Quil in exactly that position.
//
// Read through SystemParametersInfo rather than the registry key that backs it:
// the key may be absent while the system still applies a default, so the
// registry answers "unset" where this answers what is actually enforced.
//
// Reported, never changed. It is a system-wide behaviour the user chose (or
// their default), other applications depend on it, and silently rewriting it to
// make one feature work is exactly the kind of side effect `quil notify setup`
// exists to avoid.
func ForegroundLockTimeout() (time.Duration, error) {
	var ms uint32
	r, _, err := procSystemParametersInfo.Call(
		spiGetForegroundLockTimeout,
		0,
		uintptr(unsafe.Pointer(&ms)),
		0,
	)
	if r == 0 {
		return 0, fmt.Errorf("notify: reading foreground lock timeout: %w", err)
	}
	return time.Duration(ms) * time.Millisecond, nil
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
func forceForeground(hwnd uintptr) (bool, string) {
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
	if isForeground(hwnd) {
		return true, ""
	}
	return false, fmt.Sprintf("refused with queues attached (fgThread=%d targetThread=%d)", fgThread, targetThread)
}

// currentForeground is GetForegroundWindow, named so the call sites read as
// questions about state rather than as raw syscalls.
func currentForeground() uintptr {
	fg, _, _ := procGetForegroundWindow.Call()
	return fg
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
