//go:build linux || darwin || freebsd

package pty

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	cpty "github.com/creack/pty/v2"
)

type unixSession struct {
	ptmx     *os.File
	cmd      *exec.Cmd
	cols     int
	rows     int
	env      []string
	cwd      string
	waitOnce sync.Once
	exitCode int
}

func New() Session {
	return &unixSession{cols: 80, rows: 24, exitCode: -1}
}

func newWithSize(cols, rows int) Session {
	return &unixSession{cols: cols, rows: rows, exitCode: -1}
}

func (s *unixSession) SetEnv(env []string) {
	s.env = env
}

func (s *unixSession) SetCWD(dir string) {
	s.cwd = dir
}

func (s *unixSession) Start(cmd string, args ...string) error {
	s.cmd = exec.Command(cmd, args...)
	if s.cwd != "" {
		s.cmd.Dir = s.cwd
	}
	s.cmd.Env = childEnv(s.cwd, s.env)
	ws := &cpty.Winsize{Cols: uint16(s.cols), Rows: uint16(s.rows)}
	ptmx, err := cpty.StartWithSize(s.cmd, ws)
	if err != nil {
		return err
	}
	s.ptmx = ptmx
	return nil
}

func (s *unixSession) Read(buf []byte) (int, error) {
	return s.ptmx.Read(buf)
}

func (s *unixSession) Write(data []byte) (int, error) {
	return s.ptmx.Write(data)
}

func (s *unixSession) Resize(rows, cols uint16) error {
	return cpty.Setsize(s.ptmx, &cpty.Winsize{Rows: rows, Cols: cols})
}

func (s *unixSession) Close() error {
	if s.ptmx != nil {
		s.ptmx.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.WaitExit() // reuses sync.Once — safe to call from Close and streamPTYOutput
	}
	return nil
}

func (s *unixSession) Pid() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

func (s *unixSession) WaitExit() int {
	if s.cmd == nil {
		return -1
	}
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		if err == nil {
			s.exitCode = 0
		} else if s.cmd.ProcessState != nil {
			s.exitCode = s.cmd.ProcessState.ExitCode()
		}
	})
	return s.exitCode
}

// defaultTERM is what Quil's own emulator answers to.
//
// internal/tui renders every pane cell through charmbracelet/x/vt, so this
// describes Quil rather than whatever terminal happens to be attached, and the
// entry is present in every terminfo database the supported platforms ship.
const defaultTERM = "xterm-256color"

// childEnv assembles the environment for a PTY child, supplying TERM when the
// daemon's own environment has none.
//
// That absence is the normal case under `quil --remote`: the daemon is started
// through `ssh -T`, which allocates no TTY and therefore exports no TERM, and
// every pane child inherits the gap. Tools built on tcell — k9s and lazysql
// among the shipped plugins — refuse to start without it and exit 1 within
// milliseconds, which presents as a pane that opens and instantly dies rather
// than as a missing variable. Locally the daemon is spawned from a real
// terminal and inherits one, so this only ever bites over a remote link.
//
// Set only when ABSENT, never overridden: an inherited value describes the
// attached terminal accurately, and replacing it would be a behaviour change
// for every local user to fix a problem they do not have.
//
// Unix only, deliberately. The Windows path leaves the environment untouched:
// ConPTY children drive the console through the Win32 API and VT processing
// rather than terminfo, so they neither need TERM nor currently receive one —
// introducing it there would change behaviour on a platform where nothing is
// broken.
// PWD is set here rather than left to os/exec, which sets it only when Cmd.Env
// is nil.
//
// That condition is the whole hazard. exec updates PWD to match Cmd.Dir, but
// deliberately only for a nil Env, so as not to override a value the caller
// meant to set (go.dev/issue/50599). Before TERM was introduced this function
// did not exist and Env was left nil for any pane whose plugin contributed no
// variables — so those panes, and only those, got the fixup. Assigning Env
// unconditionally silently took it away from them: they began inheriting the
// DAEMON's PWD, which under `quil --remote` is ssh's login directory rather
// than the directory the pane was opened in. A child that trusts $PWD over
// getcwd() would resolve relative paths against the wrong directory, with the
// pane's own shell reporting the right one.
//
// Appended before extra, like TERM, so a plugin that sets PWD itself still wins.
//
// That precedence is provided by os/exec, NOT by execve. Cmd.environ() runs
// dedupEnv over the slice and keeps the LAST occurrence of each name, so the
// child receives exactly one TERM and one PWD. execve itself would pass both
// through, and a first-wins consumer is entirely legal there — glibc and musl
// getenv() both scan forward and return the first match. The distinction is
// worth stating because it is what constrains this slice's future: handing it
// to syscall.Exec or posix_spawn instead of exec.Cmd would silently invert
// plugin-vs-default precedence with nothing failing.
func childEnv(cwd string, extra []string) []string {
	env := os.Environ()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM="+defaultTERM)
	}
	if cwd != "" {
		// Absolute, because PWD is defined as an absolute pathname; a relative
		// one is worse than none. A path that cannot be made absolute is left
		// alone rather than guessed at.
		if abs, err := filepath.Abs(cwd); err == nil {
			env = append(env, "PWD="+abs)
		}
	}
	return append(env, extra...)
}
