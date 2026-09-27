package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/winjob"
)

func shortHome(t *testing.T) string {
	t.Helper()
	// AF_UNIX paths are capped near 108 bytes; t.TempDir() can exceed it.
	d, err := os.MkdirTemp("", "qd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// A nil effect panics only when the decision first reaches it — for the
// ambiguous-job row, only on a Windows host inside such a job.
func TestNewStartDeps_EveryEffectIsWired(t *testing.T) {
	t.Setenv("QUIL_HOME", shortHome(t))
	v := reflect.ValueOf(newStartDeps("quild", "home", "sock"))
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			t.Errorf("StartDeps.%s is nil", v.Type().Field(i).Name)
		}
	}
}

func TestStartDaemon_UsesDecision_NormalPathReturnsItsPID(t *testing.T) {
	t.Setenv("QUIL_HOME", shortHome(t))
	prev := startDepsFn
	t.Cleanup(func() { startDepsFn = prev })
	var got []string
	startDepsFn = func(quild, quilDir, sock string) winjob.StartDeps {
		return winjob.StartDeps{
			InJob:       func() (winjob.JobInfo, error) { return winjob.JobInfo{}, nil },
			SpawnNormal: func() (int, error) { got = append(got, "normal:"+filepath.Base(quilDir)); return 4242, nil },
		}
	}
	if pid := startDaemon(true); pid != 4242 {
		t.Fatalf("pid = %d, want 4242", pid)
	}
	if len(got) != 1 {
		t.Errorf("spawns = %v", got)
	}
}

func TestStartDaemon_NoBreakaway_ExitsWithMessage(t *testing.T) {
	t.Setenv("QUIL_HOME", shortHome(t))
	prev, prevExit := startDepsFn, exitFn
	t.Cleanup(func() { startDepsFn, exitFn = prev, prevExit })
	startDepsFn = func(string, string, string) winjob.StartDeps {
		return winjob.StartDeps{
			InJob:              func() (winjob.JobInfo, error) { return winjob.JobInfo{InJob: true, KillOnClose: true}, nil },
			TaskExists:         func() bool { return false },
			InteractiveSession: func() bool { return false },
			LiveDaemonPID:      func() bool { return false },
		}
	}
	code := -1
	exitFn = func(c int) { code = c }
	if pid := startDaemon(true); pid != -1 {
		t.Errorf("pid = %d, want -1", pid)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
