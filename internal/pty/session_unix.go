//go:build linux || darwin || freebsd

package pty

import (
	"os"
	"os/exec"

	cpty "github.com/creack/pty/v2"
)

type unixSession struct {
	ptmx *os.File
	cmd  *exec.Cmd
	cols int
	rows int
	env  []string
	cwd  string
}

func New() Session {
	return &unixSession{cols: 80, rows: 24}
}

func newWithSize(cols, rows int) Session {
	return &unixSession{cols: cols, rows: rows}
}

func (s *unixSession) SetEnv(env []string) {
	s.env = env
}

func (s *unixSession) SetCWD(dir string) {
	s.cwd = dir
}

func (s *unixSession) Start(cmd string, args ...string) error {
	s.cmd = exec.Command(cmd, args...)
	if len(s.env) > 0 {
		s.cmd.Env = append(os.Environ(), s.env...)
	}
	if s.cwd != "" {
		s.cmd.Dir = s.cwd
	}
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
		s.cmd.Wait()
	}
	return nil
}

func (s *unixSession) Pid() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}
