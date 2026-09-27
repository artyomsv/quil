// Package daemonspawn starts quild in the background. Shared by the quil CLI's
// startDaemon and by quil-activate's windowless `start-daemon`, so the two
// ways a daemon is born cannot drift apart.
package daemonspawn

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Spec describes one spawn. Env nil inherits this process's environment
// (exec.Cmd's own rule), which keeps startDaemon's normal path byte-identical.
type Spec struct {
	Quild       string
	Home        string
	Env         []string
	SysProcAttr *syscall.SysProcAttr
}

// Start creates Home, points the daemon's stderr at Home/quild.stderr.log
// (panics and goroutine dumps must survive — the 2026-06-11 wedge lost its
// post-mortem to a discarded stderr), starts `quild --background` there and
// releases it.
func Start(s Spec) (int, error) {
	if err := os.MkdirAll(s.Home, 0o700); err != nil {
		return 0, fmt.Errorf("create data dir %q: %w", s.Home, err)
	}
	cmd := exec.Command(s.Quild, "--background")
	cmd.Dir = s.Home
	cmd.Env = s.Env
	if f, err := os.OpenFile(filepath.Join(s.Home, "quild.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stderr = f
		defer f.Close()
	}
	cmd.SysProcAttr = s.SysProcAttr
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", s.Quild, err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release() // the daemon is detached; nothing to reap
	return pid, nil
}
