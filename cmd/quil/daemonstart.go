package main

import (
	"log"

	"github.com/artyomsv/quil/internal/daemonspawn"
	"github.com/artyomsv/quil/internal/winjob"
)

// daemonName is the daemon binary's name without extension: "quild", or the
// variant's name baked in by -X main.daemonBinary ("quild-dev", "quild-debug").
func daemonName() string {
	if daemonBinary != "" {
		return daemonBinary
	}
	return "quild"
}

// startDepsFn builds startDaemon's effects; swappable for tests.
var startDepsFn = newStartDeps

func newStartDeps(quild, quilDir, sockPath string) winjob.StartDeps {
	task := logonTaskNameForThisBuild()
	return winjob.StartDeps{
		InJob:      winjob.JobState,
		TaskExists: func() bool { return winjob.TaskExists(task) },
		InteractiveSession: func() bool {
			ok, err := winjob.UserHasInteractiveSession()
			if err != nil {
				log.Printf("daemon start: session query: %v", err)
			}
			return ok
		},
		RunTask:   func() error { return winjob.RunTask(task) },
		WaitReady: func() bool { return waitForDaemonReady(sockPath, 0) },
		LiveDaemonPID: func() bool {
			pid := daemonPID()
			if pid == 0 {
				return false
			}
			alive, comm := processProbe(pid)
			return alive && isQuildName(comm)
		},
		// An unreadable token answers true: lowering a Medium token is
		// harmless, spawning an elevated one inside a job is the defect.
		AboveMedium: func() bool {
			above, err := winjob.AboveMedium()
			if err != nil {
				log.Printf("daemon start: token integrity query: %v", err)
				return true
			}
			return above
		},
		SpawnNormal: func() (int, error) {
			return daemonspawn.Start(daemonspawn.Spec{Quild: quild, Home: quilDir, SysProcAttr: daemonSysProcAttr()})
		},
		SpawnLowered:        func() (int, error) { return spawnLowered(quild, quilDir) },
		SpawnLoweredInPlace: func() (int, error) { return spawnLoweredInPlace(quild, quilDir) },
	}
}
