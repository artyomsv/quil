package winjob

import (
	"errors"
	"slices"
	"testing"
)

type fakeDeps struct {
	inJob, breakawayOK bool
	jobErr             error
	taskExists         bool
	interactive        bool
	runTaskErr         error
	readyAfter         []bool // successive WaitReady answers
	livePID            bool
	lowerErr           error
	calls              []string
}

func (f *fakeDeps) deps() StartDeps {
	return StartDeps{
		InJob: func() (bool, bool, error) {
			f.calls = append(f.calls, "injob")
			return f.inJob, f.breakawayOK, f.jobErr
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
		SpawnNormal:   func() (int, error) { f.calls = append(f.calls, "normal"); return 11, nil },
		SpawnLowered: func() (int, error) {
			f.calls = append(f.calls, "lowered")
			if f.lowerErr != nil {
				return 0, f.lowerErr
			}
			return 22, nil
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
	f := &fakeDeps{inJob: true, breakawayOK: true, taskExists: true, interactive: true, readyAfter: []bool{true}}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaTask || res.PID != 0 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "lowered") || slices.Contains(f.calls, "normal") {
		t.Errorf("spawned although the task produced a daemon: %v", f.calls)
	}
}

func TestStartDaemon_NoTask_SpawnsLowered(t *testing.T) {
	f := &fakeDeps{inJob: true, breakawayOK: true}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLowered || res.PID != 22 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "normal") {
		t.Errorf("unlowered spawn inside a job: %v", f.calls)
	}
}

func TestStartDaemon_TaskButNoSession_SkipsTask(t *testing.T) {
	f := &fakeDeps{inJob: true, breakawayOK: true, taskExists: true, interactive: false}
	res, err := StartDaemon(f.deps())
	if err != nil || res.Via != ViaLowered {
		t.Fatalf("got %+v, %v", res, err)
	}
	if slices.Contains(f.calls, "runtask") {
		t.Errorf("ran the task with no interactive session: %v", f.calls)
	}
}

func TestStartDaemon_TaskSlow_LivePID_WaitsAgainInsteadOfSpawning(t *testing.T) {
	f := &fakeDeps{inJob: true, breakawayOK: true, taskExists: true, interactive: true,
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
	f := &fakeDeps{inJob: true, breakawayOK: true, livePID: true}
	_, err := StartDaemon(f.deps())
	if !errors.Is(err, ErrDaemonNotReady) {
		t.Fatalf("err = %v, want ErrDaemonNotReady", err)
	}
	if slices.Contains(f.calls, "lowered") || slices.Contains(f.calls, "normal") {
		t.Errorf("spawned beside a live pid: %v", f.calls)
	}
}

func TestStartDaemon_NoBreakaway_Refuses(t *testing.T) {
	f := &fakeDeps{inJob: true, breakawayOK: false}
	if _, err := StartDaemon(f.deps()); !errors.Is(err, ErrNoBreakaway) {
		t.Fatalf("err = %v, want ErrNoBreakaway", err)
	}
	if slices.Contains(f.calls, "lowered") || slices.Contains(f.calls, "normal") {
		t.Errorf("spawned without breakaway: %v", f.calls)
	}
}

func TestStartDaemon_BreakawayDeniedByOuterJob_MapsToNoBreakaway(t *testing.T) {
	f := &fakeDeps{inJob: true, breakawayOK: true, lowerErr: ErrBreakawayDenied}
	if _, err := StartDaemon(f.deps()); !errors.Is(err, ErrNoBreakaway) {
		t.Fatalf("err = %v, want ErrNoBreakaway", err)
	}
}

func TestStartDaemon_LoweringFails_NeverFallsBackToNormal(t *testing.T) {
	boom := errors.New("CreateRestrictedToken: boom")
	f := &fakeDeps{inJob: true, breakawayOK: true, lowerErr: boom}
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
