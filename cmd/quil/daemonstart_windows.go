//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemonspawn"
	"github.com/artyomsv/quil/internal/winjob"
	"golang.org/x/sys/windows"
)

// spawnLowered starts the daemon outside the ssh session's job, with a Medium
// token and the SSH_* markers removed from its environment (spec §5.2 step 4).
func spawnLowered(quild, quilDir string) (int, error) {
	tok, err := winjob.LoweredToken()
	if err != nil {
		return 0, fmt.Errorf("lower the daemon's token: %w", err)
	}
	defer tok.Close()
	pid, err := daemonspawn.Start(daemonspawn.Spec{
		Quild: quild,
		Home:  quilDir,
		Env:   winjob.DaemonEnv(os.Environ(), quilDir, config.IsDefaultQuilDir(quilDir), true),
		SysProcAttr: &syscall.SysProcAttr{
			Token:         syscall.Token(tok),
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008 | windows.CREATE_BREAKAWAY_FROM_JOB,
		},
	})
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return 0, fmt.Errorf("%w: %v", winjob.ErrBreakawayDenied, err)
	}
	return pid, err
}
