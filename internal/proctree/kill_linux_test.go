//go:build linux

package proctree

import (
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Linux is the ONE platform whose kill primitives CI can actually exercise
// against real processes — Windows and Darwin genuinely cannot be reached from
// here. Everything else about the sweep is verified through KillOps fakes,
// which by construction cannot tell whether pidfd_open, the start-time check or
// the fallback path do what they claim.

// spawnSleeper starts a real child and returns its PID and start time.
func spawnSleeper(t *testing.T) (int, time.Time) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child here: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	start, ok := processStart(pid)
	if !ok {
		t.Fatalf("could not read the start time of a live child (pid %d)", pid)
	}
	if start.IsZero() {
		t.Fatal("processStart reported success with a zero time")
	}
	return pid, start
}

func TestProcessStart_ReadsALiveChild(t *testing.T) {
	pid, start := spawnSleeper(t)

	// Two reads of the same live process must agree exactly — the kill path
	// compares them with sameProcess, which requires equality, not closeness.
	again, ok := processStart(pid)
	if !ok {
		t.Fatal("second read failed")
	}
	if !again.Equal(start) {
		t.Errorf("start time moved between reads: %v then %v", start, again)
	}
}

func TestIdentityMatches_RealChild(t *testing.T) {
	pid, start := spawnSleeper(t)

	if !identityMatches(pid, start) {
		t.Error("a live child did not match its own start time")
	}
	// A different start time is a different process wearing the PID.
	if identityMatches(pid, start.Add(time.Hour)) {
		t.Error("matched on a start time that is not this process's — this is " +
			"the check that stops a recycled PID being signalled")
	}
	// An unknown start time can never be confirmed.
	if identityMatches(pid, time.Time{}) {
		t.Error("matched on a zero start time; identity cannot be confirmed, so " +
			"the answer must be no")
	}
}

func TestAliveWithStart_FalseAfterExit(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child here: %v", err)
	}
	pid := cmd.Process.Pid
	start, ok := processStart(pid)
	if !ok {
		t.Fatal("could not read the child's start time")
	}
	if !aliveWithStart(pid, start) {
		t.Fatal("a live child reported not alive")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // reap, so the PID is not merely a zombie

	if aliveWithStart(pid, start) {
		t.Error("a reaped process still reports alive; Escalate would SIGKILL it")
	}
}

// The real signalling path, through pidfd where the kernel supports it.
func TestSignalPinned_StopsARealChild(t *testing.T) {
	pid, start := spawnSleeper(t)

	if err := signalPinned(pid, start, unix.SIGKILL); err != nil {
		t.Fatalf("signalPinned: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !aliveWithStart(pid, start) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the child survived a SIGKILL through signalPinned")
}

// The identity guard, against a real process. A mismatched start time must
// refuse rather than signal — this is what stands between the sweep and a
// recycled PID belonging to something else.
func TestSignalPinned_RefusesOnStartMismatch(t *testing.T) {
	pid, start := spawnSleeper(t)

	err := signalPinned(pid, start.Add(time.Hour), unix.SIGKILL)
	if err == nil {
		t.Fatal("signalled a process whose start time did not match")
	}
	if !aliveWithStart(pid, start) {
		t.Error("the child died despite the refusal — the signal was sent anyway")
	}

	if err := signalPinned(pid, time.Time{}, unix.SIGKILL); err == nil {
		t.Error("signalled on an unknown start time")
	}
}

// The pre-5.3 fallback, exercised directly rather than assumed dead. It is
// reachable on any kernel without pidfd_open, and an untested fallback is one
// that works until the day it is needed.
func TestSignalUnpinned_FallbackPath(t *testing.T) {
	pid, start := spawnSleeper(t)

	if err := signalUnpinned(pid, start.Add(time.Hour), unix.SIGKILL); err == nil {
		t.Error("the fallback signalled on a mismatched start time")
	}
	if !aliveWithStart(pid, start) {
		t.Fatal("the child died despite the fallback refusing")
	}

	if err := signalUnpinned(pid, start, unix.SIGKILL); err != nil {
		t.Fatalf("signalUnpinned on a valid target: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !aliveWithStart(pid, start) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the child survived the fallback SIGKILL")
}

func TestSignalPinned_RefusesNonsensePIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -12345} {
		if err := signalPinned(pid, time.Now(), unix.SIGKILL); err == nil {
			t.Errorf("signalPinned accepted pid %d — 0 and negatives address "+
				"process GROUPS, which would signal far more than the target", pid)
		}
	}
}

// End to end: DefaultKillOps driving a real subtree through the same Sweep the
// daemon calls. The fakes cannot show that the wiring between Sweep and the
// platform primitives is right.
func TestDefaultKillOps_SweepStopsARealProcess(t *testing.T) {
	pid, start := spawnSleeper(t)

	node := &Node{PID: pid, Start: start, Depth: 2, Name: "sleep"}
	res := Sweep(node, 200*time.Millisecond, DefaultKillOps())

	if res.Signalled != 1 {
		t.Fatalf("signalled %d, want 1 (skipped=%d)", res.Signalled, res.Skipped)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !aliveWithStart(pid, start) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the process survived a full sweep with the real platform primitives")
}

// A zombie is not alive.
//
// /proc/<pid>/stat survives a process's death until its parent reaps it, with
// the original start time intact, so an identity check alone answers "alive"
// for something that has already exited. A subtree sweep manufactures exactly
// this state: it kills parents and children together, and a parent that exits
// first cannot reap anything. This test spawns a child, kills it, and
// deliberately does NOT reap it.
func TestAliveWithStart_ZombieIsNotAlive(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child here: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() })

	start, ok := processStart(pid)
	if !ok {
		t.Fatal("could not read the child's start time")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	// Unreaped: the PID and its start time both still resolve.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if isZombie(pid) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !isZombie(pid) {
		t.Skip("the child was reaped before it could be observed as a zombie")
	}
	if !identityMatches(pid, start) {
		t.Fatal("fixture: a zombie should still match on identity — that is the " +
			"whole reason the state check is needed")
	}

	if aliveWithStart(pid, start) {
		t.Error("a zombie reported alive; Escalate would SIGKILL a corpse and " +
			"report it as forced, and the dialog would offer to stop it")
	}
}
