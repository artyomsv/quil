//go:build windows

package pty

import (
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/pty/winconpty"
	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

// winConPTY is the common surface of the inbox (charmbracelet/x/conpty) and the
// bundled-OpenConsole (winconpty) ConPTY backends. Both expose identical methods.
type winConPTY interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(w, h int) error
	Close() error
	Spawn(name string, args []string, attr *syscall.ProcAttr) (pid int, handle uintptr, err error)
}

type winSession struct {
	cpty     winConPTY
	pid      int
	handle   windows.Handle
	cols     int
	rows     int
	env      []string
	cwd      string
	waitOnce sync.Once
	exitCode int
}

func New() Session {
	return &winSession{cols: 80, rows: 24, exitCode: -1}
}

func newWithSize(cols, rows int) Session {
	return &winSession{cols: cols, rows: rows, exitCode: -1}
}

func (s *winSession) SetEnv(env []string) {
	s.env = env
}

func (s *winSession) SetCWD(dir string) {
	s.cwd = dir
}

// newConPTY prefers the bundled-OpenConsole backend (which renders claude-code's
// input cleanly, unlike the Windows 10 inbox conhost) and falls back to the
// inbox ConPTY when the bundled conpty.dll is not present next to the binary.
func newConPTY(cols, rows int) (winConPTY, error) {
	if winconpty.Available() {
		cp, err := winconpty.New(cols, rows, 0)
		if err == nil {
			logger.Debug("conpty: using bundled OpenConsole")
			return cp, nil
		}
		logger.Debug("conpty: bundled backend failed (%v); using inbox conhost", err)
	}
	cp, err := conpty.New(cols, rows, 0)
	if err != nil {
		return nil, err
	}
	return cp, nil
}

func (s *winSession) Start(cmd string, args ...string) error {
	cp, err := newConPTY(s.cols, s.rows)
	if err != nil {
		return err
	}
	s.cpty = cp

	fullArgs := append([]string{cmd}, args...)
	attr := &syscall.ProcAttr{
		Dir: s.cwd,
	}
	if len(s.env) > 0 {
		attr.Env = append(os.Environ(), s.env...)
	}
	pid, handle, err := cp.Spawn(cmd, fullArgs, attr)
	if err != nil {
		cp.Close()
		return err
	}
	s.pid = pid
	s.handle = windows.Handle(handle)
	return nil
}

func (s *winSession) Read(buf []byte) (int, error) {
	if s.cpty == nil {
		return 0, io.EOF
	}
	return s.cpty.Read(buf)
}

func (s *winSession) Write(data []byte) (int, error) {
	if s.cpty == nil {
		return 0, io.ErrClosedPipe
	}
	return s.cpty.Write(data)
}

func (s *winSession) Resize(rows, cols uint16) error {
	if s.cpty == nil {
		return nil
	}
	return s.cpty.Resize(int(cols), int(rows))
}

func (s *winSession) Close() error {
	if s.cpty != nil {
		s.cpty.Close()
	}
	return nil
}

func (s *winSession) Pid() int {
	return s.pid
}

func (s *winSession) WaitExit() int {
	s.waitOnce.Do(func() {
		if s.handle == 0 {
			return
		}
		windows.WaitForSingleObject(s.handle, windows.INFINITE)
		var code uint32
		if err := windows.GetExitCodeProcess(s.handle, &code); err == nil {
			s.exitCode = int(code)
		}
		// The kernel keeps the process object alive while any handle is
		// open; without this Close the daemon retains one HANDLE per
		// destroyed/restarted pane for its whole lifetime. The error is
		// discarded: after a successful wait the handle is known-valid, and
		// a CloseHandle failure has no recovery path.
		_ = windows.CloseHandle(s.handle)
		s.handle = 0
	})
	return s.exitCode
}
