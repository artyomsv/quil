//go:build windows

package main

import (
	"os"
	"syscall"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemonspawn"
	"github.com/artyomsv/quil/internal/notify"
	"github.com/artyomsv/quil/internal/winjob"
	"golang.org/x/sys/windows"
)

// runStartDaemon is the logon task's action. Windowless by construction (this
// binary is -H windowsgui) and it never exits non-zero: a task action's error
// is a silent line in Task Scheduler's history, and the daemon log is where a
// failure is useful. The breakaway flag is added when the scheduler runs the
// action inside a kill-on-close job, so the daemon's life does not depend on
// how the scheduler tears the action down.
func runStartDaemon(args []string) {
	quild, home, err := parseStartDaemonArgs(args)
	if err != nil {
		return
	}
	logf := notify.ActivationLogger(home)
	flags := uint32(syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008) // DETACHED_PROCESS
	if in, ok, _ := winjob.InKillOnCloseJob(); in && ok {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	_, err = daemonspawn.Start(daemonspawn.Spec{
		Quild:       quild,
		Home:        home,
		Env:         winjob.DaemonEnv(os.Environ(), home, config.IsDefaultQuilDir(home), false),
		SysProcAttr: &syscall.SysProcAttr{CreationFlags: flags},
	})
	if err != nil {
		logf("start-daemon: %v", err)
	}
}
