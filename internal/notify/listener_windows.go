//go:build windows

package notify

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// maxActivateFrame bounds one pipe read. A pane id is 13 bytes; anything
// meaningfully longer is not a caller we serve, and refusing rather than
// buffering keeps a hostile local process from growing our heap.
const maxActivateFrame = 64

// sendActivateTimeout bounds the busy-retry in SendActivate. Short: this runs
// in the throwaway process Windows spawns for a toast click, and the user is
// waiting for their cursor to move.
const sendActivateTimeout = 2 * time.Second

// readFrameTimeout bounds one client's write after it has connected. A real
// client writes 13 bytes immediately; anything slower is not a client we owe
// the whole session's click routing to.
const readFrameTimeout = 2 * time.Second

// PipeName is the per-PID activation endpoint.
//
// Keyed by PID because several TUIs can run against one workspace and the
// toast belongs to exactly the one that raised it — the same PID the launch URI
// carries.
func PipeName(pid int) string {
	return `\\.\pipe\quil-activate-` + strconv.Itoa(pid)
}

type pipeListener struct {
	name   string
	handle windows.Handle

	mu     sync.Mutex
	closed bool
}

// Listen serves activation requests until the returned Closer is closed.
//
// onActivate is called with a pane id that has already passed ValidPaneID.
// Validation happens HERE as well as in ParseActivateURI because the pipe is
// independently reachable by any same-user process — the URI parser is not its
// gatekeeper.
func Listen(pid int, onActivate func(paneID string)) (io.Closer, error) {
	sd, err := currentUserOnlySD()
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))

	l := &pipeListener{name: PipeName(pid)}
	h, err := l.create(&sa)
	if err != nil {
		return nil, err
	}
	l.handle = h

	go l.serve(onActivate)
	return l, nil
}

func (l *pipeListener) create(sa *windows.SecurityAttributes) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("notify: encoding pipe name: %w", err)
	}
	h, err := windows.CreateNamedPipe(
		namePtr,
		// FILE_FLAG_FIRST_PIPE_INSTANCE is a SECURITY control, not a tidiness
		// flag, and omitting it silently voided the DACL below.
		//
		// \\.\pipe\ is a machine-global namespace shared across users and
		// sessions, and this name is predictable (PIDs are small and
		// enumerable). Without this flag, a process that pre-created the name —
		// including one running as a DIFFERENT, lower-privileged user — makes
		// this call succeed by joining THEIR pipe object, whose security
		// descriptor then governs. The explicit current-user-only DACL would
		// simply not be in effect, and any user could write a pane id into it
		// to move the victim's active pane, redirecting where their next
		// keystrokes land.
		//
		// With the flag, that case returns ERROR_ACCESS_DENIED and the caller
		// skips the feature — refusing is correct here, since the alternative
		// is adopting a stranger's control surface.
		windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		// MESSAGE mode rather than BYTE: a byte-mode read can return a partial
		// frame, and while a 13-byte write makes that vanishingly unlikely,
		// message mode makes it impossible rather than improbable.
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT,
		// One instance is all this design ever creates; saying so lets Windows
		// reject a second rather than leaving the cap inert and misleading.
		1,
		0, maxActivateFrame,
		0,
		sa,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("notify: creating pipe %s: %w", l.name, err)
	}
	return h, nil
}

func (l *pipeListener) serve(onActivate func(paneID string)) {
	for {
		// ConnectNamedPipe blocks. Close unblocks it by dialling the pipe
		// itself, which is why the closed check comes immediately after.
		//
		// Three outcomes count as "a client is here":
		//   nil                   — connected normally
		//   ERROR_PIPE_CONNECTED  — connected before we got here
		//   ERROR_NO_DATA         — connected AND already closed
		//
		// The third is the common case rather than an oddity: quil activate
		// writes 13 bytes and exits, so it routinely finishes before this
		// goroutine is scheduled. Its data is still buffered and readable until
		// DisconnectNamedPipe, so treating it as fatal killed the listener on
		// the very first activation — which then presented as "all pipe
		// instances are busy" for every later one.
		err := windows.ConnectNamedPipe(l.handle, nil)
		if err != nil &&
			!errors.Is(err, windows.ERROR_PIPE_CONNECTED) &&
			!errors.Is(err, windows.ERROR_NO_DATA) {
			windows.CloseHandle(l.handle)
			return
		}
		if l.isClosed() {
			windows.DisconnectNamedPipe(l.handle)
			windows.CloseHandle(l.handle)
			return
		}

		l.readOne(onActivate)

		windows.DisconnectNamedPipe(l.handle)
		if l.isClosed() {
			windows.CloseHandle(l.handle)
			return
		}
	}
}

// readOne consumes exactly one client frame.
//
// A malformed frame closes that connection and the loop continues — it must
// never kill the listener, or one bad local caller would disable activation for
// the whole session.
func (l *pipeListener) readOne(onActivate func(paneID string)) {
	// Bounded by a watchdog, because this is a BLOCKING read on a PIPE_WAIT
	// pipe and the listener serves one connection at a time. Any same-user
	// process that connects and then neither writes nor closes would otherwise
	// park this goroutine forever — after which every toast click gets
	// ERROR_PIPE_BUSY and click routing is dead for the life of the TUI, with
	// no diagnostic anywhere. A crashed client is already fine (the OS closes
	// the handle), so this covers the hostile-or-buggy holder.
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(readFrameTimeout):
			// Aborts the pending ReadFile on this handle from another thread,
			// which is exactly what CancelIoEx (unlike CancelIo) is for.
			_ = windows.CancelIoEx(l.handle, nil)
		}
	}()
	defer close(done)

	var buf [maxActivateFrame]byte
	var n uint32
	if err := windows.ReadFile(l.handle, buf[:], &n, nil); err != nil {
		return
	}
	id := string(buf[:n])
	if !ValidPaneID(id) {
		return
	}
	onActivate(id)
}

func (l *pipeListener) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// Close stops the accept loop.
//
// CancelIoEx rather than the obvious "dial our own pipe to wake it": the dial
// only works if the loop happens to be parked in ConnectNamedPipe, and it races
// the window between DisconnectNamedPipe and the next ConnectNamedPipe — land
// there and the dial gets ERROR_PIPE_BUSY, Close returns having woken nothing,
// and serve parks forever still holding the handle. Cancelling pending I/O on
// the handle has no such window and also aborts a blocked ReadFile, so the
// method's contract ("stops the accept loop") is unconditionally true.
func (l *pipeListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	_ = windows.CancelIoEx(l.handle, nil)
	return nil
}

// SendActivate delivers a pane id to the TUI identified by pid.
//
// Returns an error when no listener exists — the caller translates that into a
// silent exit, because a stale toast whose TUI has since quit must never pop an
// error window.
func SendActivate(pid int, paneID string) error {
	if !ValidPaneID(paneID) {
		return errors.New("notify: refusing to send malformed pane id")
	}
	h, err := dialActivatePipe(pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	b := []byte(paneID)
	var n uint32
	if err := windows.WriteFile(h, b, &n, nil); err != nil {
		return fmt.Errorf("notify: writing activation: %w", err)
	}
	return nil
}

// dialActivatePipe opens the activation pipe, riding out the transient busy
// window.
//
// The listener serves one connection at a time, so a client arriving while
// another is being read — or in the instant between DisconnectNamedPipe and the
// next ConnectNamedPipe — gets ERROR_PIPE_BUSY. That is a wait, not a failure.
//
// Bounded, because "nobody is listening" must stay distinguishable from "busy":
// the caller turns the former into a silent exit, and blocking forever would
// hang a toast click. A plain retry rather than WaitNamedPipe, which
// x/sys/windows does not expose — at one activation per click, polling costs
// nothing and saves hand-declaring another syscall.
func dialActivatePipe(pid int) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(PipeName(pid))
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("notify: encoding pipe name: %w", err)
	}
	deadline := time.Now().Add(sendActivateTimeout)
	for {
		// SECURITY_SQOS_PRESENT|SECURITY_ANONYMOUS is load-bearing, not
		// boilerplate. Without it a named-pipe client's quality of service
		// defaults to SecurityImpersonation — the most permissive level,
		// chosen by omission — so whoever is on the other end may call
		// ImpersonateNamedPipeClient and act as this user.
		//
		// Every control in this file protects the SERVER: the DACL, and
		// FILE_FLAG_FIRST_PIPE_INSTANCE so a squatter cannot take the name
		// from us. Nothing protected the CLIENT, and the two interact
		// backwards — pipe names are predictable (PipeName is the pid), so a
		// second principal on the machine can pre-create the name across a pid
		// range; our own Listen then fails with ERROR_ACCESS_DENIED, which the
		// TUI logs at debug and continues past with toasts still firing, and
		// the next click dials straight into the squatter and hands over an
		// impersonation token. The pane id is worthless; the token is the prize.
		//
		// Anonymous is the right level rather than merely the safest: the
		// server never needs the client's identity — the DACL already decided
		// who may connect.
		h, err := windows.CreateFile(namePtr, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_ANONYMOUS, 0)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || time.Now().After(deadline) {
			return windows.InvalidHandle, fmt.Errorf("notify: no activation listener for pid %d: %w", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// currentUserOnlySD restricts the pipe to the account that created it.
//
// Explicit rather than relying on the default DACL: the pipe is a control
// surface, and "same machine" is not the same as "same user".
func currentUserOnlySD() (*windows.SECURITY_DESCRIPTOR, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("notify: reading process token: %w", err)
	}
	sid := user.User.Sid.String()
	// P = protected (no inherited ACEs), GA = generic all, for this SID only.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid + ")")
	if err != nil {
		return nil, fmt.Errorf("notify: building pipe security descriptor: %w", err)
	}
	return sd, nil
}
