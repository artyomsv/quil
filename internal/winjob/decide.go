package winjob

import (
	"errors"
	"fmt"
)

// How StartDaemon produced a daemon.
const (
	ViaNormal         = "normal"           // today's spawn: no job, or a Medium token in an ambiguous one
	ViaTask           = "task"             // the logon task started it in the desktop session
	ViaWaited         = "waited"           // another daemon was already starting; we waited
	ViaLowered        = "lowered"          // breakaway + lowered token
	ViaLoweredInPlace = "lowered-in-place" // lowered token, still inside the ambiguous job
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
	InJob              func() (JobInfo, error)
	TaskExists         func() bool
	InteractiveSession func() bool
	RunTask            func() error
	WaitReady          func() bool
	LiveDaemonPID      func() bool
	AboveMedium        func() bool // answers true when it cannot tell
	SpawnNormal        func() (pid int, err error)
	SpawnLowered       func() (pid int, err error) // lowered token + breakaway
	// SpawnLoweredInPlace is SpawnLowered WITHOUT the breakaway flag, for a
	// job that allows no breakaway but does not visibly kill on close.
	SpawnLoweredInPlace func() (pid int, err error)
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
// Outside any job it is today's spawn, byte for byte. Inside one: the logon
// task when it can run, else wait for a daemon that is already starting, else
// a lowered breakaway spawn when the job allows breakaway, else a refusal when
// the job kills on close.
//
// The last case is an AMBIGUOUS job: it allows no breakaway and does not
// visibly kill on close, but only the innermost job is visible, so an outer
// kill-on-close job (sshd's) may sit behind it. Breaking away is impossible,
// so the daemon stays in the job either way; what matters is its token. An
// above-Medium token (an admin over ssh is High) is lowered in place; a
// Medium one is already not elevated, so the normal spawn is used and a
// desktop user in such a job keeps today's behaviour exactly.
//
// It NEVER spawns with an above-Medium token inside a job, and a job query
// that fails is treated as "inside a kill-on-close job with breakaway",
// because the lowered path is safe everywhere and the normal one is not.
func StartDaemon(d StartDeps) (StartResult, error) {
	job, err := d.InJob()
	if err != nil {
		job = JobInfo{InJob: true, KillOnClose: true, BreakawayOK: true}
	}
	if !job.InJob {
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

	if job.BreakawayOK {
		pid, err := d.SpawnLowered()
		if errors.Is(err, ErrBreakawayDenied) {
			return StartResult{}, fmt.Errorf("%w: %v", ErrNoBreakaway, err)
		}
		if err != nil {
			return StartResult{}, err
		}
		return StartResult{PID: pid, Via: ViaLowered}, nil
	}
	if job.KillOnClose {
		return StartResult{}, ErrNoBreakaway
	}

	if d.AboveMedium() {
		pid, err := d.SpawnLoweredInPlace()
		if err != nil {
			return StartResult{}, err
		}
		return StartResult{PID: pid, Via: ViaLoweredInPlace}, nil
	}
	pid, err := d.SpawnNormal()
	return StartResult{PID: pid, Via: ViaNormal}, err
}
