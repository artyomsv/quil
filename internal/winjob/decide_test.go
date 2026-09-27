package winjob

import (
	"errors"
	"slices"
	"testing"
)

// sshJob is Win32-OpenSSH's session job as the query sees it.
var sshJob = JobInfo{InJob: true, KillOnClose: true, BreakawayOK: true}

// ambiguousJob allows no breakaway and does not visibly kill on close; an
// unseen outer kill-on-close job may still sit behind it.
var ambiguousJob = JobInfo{InJob: true}

type fakeDeps struct {
	job         JobInfo
	jobErr      error
	taskExists  bool
	interactive bool
	runTaskErr  error
	readyAfter  []bool // successive WaitReady answers
	livePID     bool
	aboveMedium bool
	lowerErr    error
	inPlaceErr  error
	calls       []string
}

func (f *fakeDeps) deps() StartDeps {
	return StartDeps{
		InJob: func() (JobInfo, error) {
			f.calls = append(f.calls, "injob")
			return f.job, f.jobErr
		},
		TaskExists:         func() bool { f.calls = append(f.calls, "taskexists"); return f.taskExists },
		InteractiveSession: func() bool { f.calls = append(f.calls, "interactive"); return f.interactive },
		RunTask:            func() error { f.calls = append(f.calls, "runtask"); return f.runTaskErr },
		WaitReady: func() bool {
			f.calls = append(f.calls, "wait")
			if len(f.readyAfter) == 0 {
				return false
			}
			r := f.readyAfter[0]
			f.readyAfter = f.readyAfter[1:]
			return r
		},
		LiveDaemonPID: func() bool { f.calls = append(f.calls, "livepid"); return f.livePID },
		AboveMedium:   func() bool { f.calls = append(f.calls, "abovemedium"); return f.aboveMedium },
		SpawnNormal:   func() (int, error) { f.calls = append(f.calls, "normal"); return 11, nil },
		SpawnLowered: func() (int, error) {
			f.calls = append(f.calls, "lowered")
			if f.lowerErr != nil {
				return 0, f.lowerErr
			}
			return 22, nil
		},
		SpawnLoweredInPlace: func() (int, error) {
			f.calls = append(f.calls, "inplace")
			if f.inPlaceErr != nil {
				return 0, f.inPlaceErr
			}
			return 33, nil
		},
	}
}

func TestStartDaemon_NotInJob_SpawnsNormallyAndNothingElse(t *testing.T) {
	f := &fakeDeps{}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaNormal || res.PID != 11 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !slices.Equal(f.calls, []string{"injob", "normal"}) {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestStartDaemon_TaskAndSession_UsesTask(t *testing.T) {
	f := &fakeDeps{job: sshJob, taskExists: true, interactive: true, readyAfter: []bool{true}}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaTask || res.PID != 0 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "lowered") || slices.Contains(f.calls, "normal") {
		t.Errorf("spawned although the task produced a daemon: %v", f.calls)
	}
}

func TestStartDaemon_NoTask_SpawnsLowered(t *testing.T) {
	f := &fakeDeps{job: sshJob}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLowered || res.PID != 22 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "normal") {
		t.Errorf("unlowered spawn inside a job: %v", f.calls)
	}
}

func TestStartDaemon_TaskButNoSession_SkipsTask(t *testing.T) {
	f := &fakeDeps{job: sshJob, taskExists: true, interactive: false}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLowered {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "runtask") {
		t.Errorf("ran the task with no interactive session: %v", f.calls)
	}
}

func TestStartDaemon_TaskSlow_LivePID_WaitsAgainInsteadOfSpawning(t *testing.T) {
	f := &fakeDeps{job: sshJob, taskExists: true, interactive: true,
		readyAfter: []bool{false, true}, livePID: true}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaWaited {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "lowered") {
		t.Errorf("spawned a second daemon beside a live one: %v", f.calls)
	}
}

func TestStartDaemon_LivePIDNeverReady_StillNoSecondSpawn(t *testing.T) {
	f := &fakeDeps{job: sshJob, livePID: true}
	_, err := StartDaemon(f.deps())
	if !errors.Is(err, ErrDaemonNotReady) {
		t.Fatalf("err = %v, want ErrDaemonNotReady", err)
	}
	if slices.Contains(f.calls, "lowered") || slices.Contains(f.calls, "normal") {
		t.Errorf("spawned beside a live pid: %v", f.calls)
	}
}

func TestStartDaemon_KillOnCloseWithoutBreakaway_Refuses(t *testing.T) {
	f := &fakeDeps{job: JobInfo{InJob: true, KillOnClose: true}, aboveMedium: true}
	if _, err := StartDaemon(f.deps()); !errors.Is(err, ErrNoBreakaway) {
		t.Fatalf("err = %v, want ErrNoBreakaway", err)
	}
	for _, spawn := range []string{"lowered", "inplace", "normal"} {
		if slices.Contains(f.calls, spawn) {
			t.Errorf("spawned (%s) in a job that kills it on close: %v", spawn, f.calls)
		}
	}
}

// An innermost job that allows breakaway but does not kill on close may hide
// an outer kill-on-close one; breaking away is safe whether or not it exists.
func TestStartDaemon_BreakawayWithoutKillOnClose_SpawnsLowered(t *testing.T) {
	f := &fakeDeps{job: JobInfo{InJob: true, BreakawayOK: true}}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLowered || res.PID != 22 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "normal") {
		t.Errorf("unlowered spawn inside a job: %v", f.calls)
	}
}

// The H-1 defect: an ambiguous job used to read as "not in a job", so an
// admin's High ssh token started the daemon elevated inside sshd's job.
func TestStartDaemon_AmbiguousJobAboveMedium_LowersInPlaceNeverNormal(t *testing.T) {
	f := &fakeDeps{job: ambiguousJob, aboveMedium: true}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLoweredInPlace || res.PID != 33 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "normal") || slices.Contains(f.calls, "lowered") {
		t.Errorf("want only the in-place lowered spawn: %v", f.calls)
	}
}

func TestStartDaemon_AmbiguousJobAboveMedium_InPlaceFails_NeverFallsBackToNormal(t *testing.T) {
	boom := errors.New("CreateRestrictedToken: boom")
	f := &fakeDeps{job: ambiguousJob, aboveMedium: true, inPlaceErr: boom}
	if _, err := StartDaemon(f.deps()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if slices.Contains(f.calls, "normal") {
		t.Errorf("fell back to the unlowered spawn: %v", f.calls)
	}
}

// A Medium token is already not elevated: a desktop user in such a job keeps
// today's spawn exactly.
func TestStartDaemon_AmbiguousJobMedium_SpawnsNormally(t *testing.T) {
	f := &fakeDeps{job: ambiguousJob}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaNormal || res.PID != 11 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "inplace") || slices.Contains(f.calls, "lowered") {
		t.Errorf("lowered a Medium token: %v", f.calls)
	}
}

func TestStartDaemon_AmbiguousJobTaskAndSession_UsesTask(t *testing.T) {
	f := &fakeDeps{job: ambiguousJob, aboveMedium: true, taskExists: true, interactive: true,
		readyAfter: []bool{true}}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaTask {
		t.Fatalf("got %+v, %v", res, err)
	}
	for _, spawn := range []string{"lowered", "inplace", "normal"} {
		if slices.Contains(f.calls, spawn) {
			t.Errorf("spawned (%s) although the task produced a daemon: %v", spawn, f.calls)
		}
	}
}

func TestStartDaemon_AmbiguousJobLivePID_WaitsInsteadOfSpawning(t *testing.T) {
	f := &fakeDeps{job: ambiguousJob, aboveMedium: true, livePID: true, readyAfter: []bool{true}}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaWaited {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "inplace") || slices.Contains(f.calls, "normal") {
		t.Errorf("spawned beside a live pid: %v", f.calls)
	}
}

func TestStartDaemon_BreakawayDeniedByOuterJob_MapsToNoBreakaway(t *testing.T) {
	f := &fakeDeps{job: sshJob, lowerErr: ErrBreakawayDenied}
	if _, err := StartDaemon(f.deps()); !errors.Is(err, ErrNoBreakaway) {
		t.Fatalf("err = %v, want ErrNoBreakaway", err)
	}
}

func TestStartDaemon_LoweringFails_NeverFallsBackToNormal(t *testing.T) {
	boom := errors.New("CreateRestrictedToken: boom")
	f := &fakeDeps{job: sshJob, lowerErr: boom}
	if _, err := StartDaemon(f.deps()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if slices.Contains(f.calls, "normal") {
		t.Errorf("fell back to the unlowered spawn: %v", f.calls)
	}
}

func TestStartDaemon_JobQueryFails_TreatedAsInJob(t *testing.T) {
	f := &fakeDeps{jobErr: errors.New("query failed")}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLowered {
		t.Fatalf("got %+v, %v — an unanswerable job query must take the safe path", res, err)
	}
}
