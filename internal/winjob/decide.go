package winjob

import (
	"errors"
	"fmt"
)

// How StartDaemon produced a daemon.
const (
	ViaNormal  = "normal"  // not in a kill-on-close job: today's spawn
	ViaTask    = "task"    // the logon task started it in the desktop session
	ViaWaited  = "waited"  // another daemon was already starting; we waited
	ViaLowered = "lowered" // breakaway + lowered token
)

var (
	// ErrNoBreakaway: the job forbids breakaway, so no daemon started here can
	// outlive the session. The caller prints NoBreakawayMessage.
	ErrNoBreakaway = errors.New("cannot start a daemon that outlives this session")
	// ErrBreakawayDenied is what SpawnLowered wraps when CreateProcess refuses
	// CREATE_BREAKAWAY_FROM_JOB (an OUTER job forbids it; the query sees only
	// the innermost one).
	ErrBreakawayDenied = errors.New("breakaway from the job was denied")
	// ErrDaemonNotReady: a daemon process exists but never opened its socket.
	ErrDaemonNotReady = errors.New("a daemon is starting but did not become ready")
)

// NoBreakawayMessage is the user-facing text for ErrNoBreakaway.
const NoBreakawayMessage = "quil: this session cannot start a daemon that survives it.\n" +
	"  Run `quil daemon install-logon` on that machine once, or log on at the\n" +
	"  machine so the daemon starts in the desktop session."

// StartDeps are StartDaemon's effects, injected so the decision is testable on
// any OS. WaitReady waits the full daemon-ready budget for the socket.
type StartDeps struct {
	InJob              func() (inJob, breakawayOK bool, err error)
	TaskExists         func() bool
	InteractiveSession func() bool
	RunTask            func() error
	WaitReady          func() bool
	LiveDaemonPID      func() bool
	SpawnNormal        func() (pid int, err error)
	SpawnLowered       func() (pid int, err error)
}

// StartResult reports the path taken. PID is 0 when the daemon was started by
// something other than this process (task, or one already starting), which is
// startDaemon's existing "socket is already up" convention.
type StartResult struct {
	PID int
	Via string
}

// StartDaemon decides how to start the daemon (spec §5.2, decision D1).
//
// Outside a kill-on-close job it is today's spawn, byte for byte. Inside one:
// the logon task when it can run, else wait for a daemon that is already
// starting, else a lowered breakaway spawn. It NEVER spawns with the unlowered
// token inside a job — that token is High for an admin over ssh — and a job
// query that fails is treated as "inside a job", because the lowered path is
// safe everywhere and the normal one is not.
func StartDaemon(d StartDeps) (StartResult, error) {
	inJob, breakawayOK, err := d.InJob()
	if err != nil {
		inJob, breakawayOK = true, true
	}
	if !inJob {
		pid, err := d.SpawnNormal()
		return StartResult{PID: pid, Via: ViaNormal}, err
	}

	if d.TaskExists() && d.InteractiveSession() {
		if err := d.RunTask(); err == nil && d.WaitReady() {
			return StartResult{Via: ViaTask}, nil
		}
	}

	// A daemon process that exists but is not listening yet — the task's, still
	// restoring panes serially before it listens — must not get a sibling: the
	// second one's listener would remove the first one's live socket.
	if d.LiveDaemonPID() {
		if d.WaitReady() {
			return StartResult{Via: ViaWaited}, nil
		}
		return StartResult{}, ErrDaemonNotReady
	}

	if !breakawayOK {
		return StartResult{}, ErrNoBreakaway
	}
	pid, err := d.SpawnLowered()
	if errors.Is(err, ErrBreakawayDenied) {
		return StartResult{}, fmt.Errorf("%w: %v", ErrNoBreakaway, err)
	}
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{PID: pid, Via: ViaLowered}, nil
}
