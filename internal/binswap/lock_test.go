package binswap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const testStale = 2 * time.Minute

// TestAcquireLock_SecondCallerRefused: the point of the lock — two swappers
// of one target, and one would delete the other's in-flight backup.
func TestAcquireLock_SecondCallerRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")

	release, ok := AcquireLock(path, testStale)
	if !ok {
		t.Fatal("first acquire failed, want the lock taken")
	}
	if _, ok := AcquireLock(path, testStale); ok {
		t.Error("second acquire succeeded while the lock was held")
	}
	if !LockHeld(path, testStale) {
		t.Error("LockHeld = false while the lock was held")
	}
	release()
	if LockHeld(path, testStale) {
		t.Error("LockHeld = true after release")
	}
	if _, ok := AcquireLock(path, testStale); !ok {
		t.Error("acquire after release failed, want the lock reusable")
	}
}

// TestAcquireLock_ReleaseOnlyRemovesOwnLock: a release must not delete a
// lock it no longer owns, or a takeover hands a third process the lock
// while the second is mid-swap.
func TestAcquireLock_ReleaseOnlyRemovesOwnLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")
	release, ok := AcquireLock(path, testStale)
	if !ok {
		t.Fatal("acquire failed")
	}
	if err := os.WriteFile(path, []byte("424242\n"), 0600); err != nil {
		t.Fatalf("simulate takeover: %v", err)
	}
	release()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("release deleted a lock it did not own: %v", err)
	}
	if strings.TrimSpace(string(data)) != "424242" {
		t.Errorf("lock content = %q, want the new holder's pid intact", data)
	}
}

// TestAcquireLock_DeadHolderReclaimedImmediately: a crashed holder's lock is
// reclaimable at once — the recorded pid makes that a fact, not a guess.
func TestAcquireLock_DeadHolderReclaimedImmediately(t *testing.T) {
	const deadPID = 0x7fffffff
	if processAlive(deadPID) {
		t.Skipf("pid %d unexpectedly alive; cannot exercise the dead-holder path", deadPID)
	}
	path := filepath.Join(t.TempDir(), "tool.lock")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", deadPID)), 0600); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	if LockHeld(path, testStale) {
		t.Error("LockHeld = true for a dead holder's fresh lock")
	}
	release, ok := AcquireLock(path, testStale)
	if !ok {
		t.Fatal("could not reclaim a dead holder's lock")
	}
	release()
}

// TestAcquireLock_StaleLockTakenOver: the age backstop for a pid since
// recycled by an unrelated process, which liveness alone would read as held
// forever.
func TestAcquireLock_StaleLockTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")
	if err := os.WriteFile(path, []byte("999999\n"), 0600); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	stale := time.Now().Add(-testStale - time.Minute)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("age lock: %v", err)
	}
	if LockHeld(path, testStale) {
		t.Error("LockHeld = true for a stale lock, want it ignored")
	}
	release, ok := AcquireLock(path, testStale)
	if !ok {
		t.Fatal("could not take over a stale lock — a crashed holder would wedge updates forever")
	}
	release()
}

// TestWaitLock_WaitsForTheHolder: a second updater waits for the first and
// then proceeds; the wait is announced once and bounded.
func TestWaitLock_WaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")
	release, ok := AcquireLock(path, testStale)
	if !ok {
		t.Fatal("acquire failed")
	}

	var waits int
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		rel, err := WaitLock(context.Background(), path, testStale, 5*time.Second, func() {
			mu.Lock()
			waits++
			mu.Unlock()
		})
		if err == nil {
			rel()
		}
		done <- err
	}()
	time.Sleep(3 * lockPollInterval)
	select {
	case err := <-done:
		t.Fatalf("WaitLock returned %v while the lock was held", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("WaitLock after the holder released: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if waits != 1 {
		t.Errorf("onWait ran %d times, want once", waits)
	}

	// Bounded: a holder that never lets go is an error naming the path, and
	// a cancelled context ends the wait at once.
	release, _ = AcquireLock(path, testStale)
	defer release()
	if _, err := WaitLock(context.Background(), path, testStale, 2*lockPollInterval, nil); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("WaitLock past maxWait = %v, want an error naming the lock", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WaitLock(ctx, path, testStale, time.Minute, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitLock with a cancelled ctx = %v, want context.Canceled", err)
	}
}

// TestTryLock_UnwritableDirFailsFast: a directory the lock cannot be created
// in is an error at once, never "held" — O_EXCL fails with EACCES, no file
// exists for LockAbandoned to judge, and a waiter polling that would report
// a holder that is not there after the whole wait.
func TestTryLock_UnwritableDirFailsFast(t *testing.T) {
	dir := unwritableDir(t)
	path := filepath.Join(dir, "tool.lock")
	if _, err := TryLock(path, testStale); err == nil || errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryLock in an unwritable dir = %v, want the create error, not held", err)
	}
	started := time.Now()
	_, err := WaitLock(context.Background(), path, testStale, time.Minute, func() { t.Error("WaitLock waited on an unwritable dir") })
	if err == nil || errors.Is(err, ErrLockHeld) {
		t.Fatalf("WaitLock = %v, want the create error", err)
	}
	if spent := time.Since(started); spent > 2*lockPollInterval {
		t.Errorf("WaitLock took %v to fail on an unwritable dir", spent)
	}
}

// unwritableDir returns a directory nothing can be created in, or skips
// where that cannot be arranged: Windows ignores the mode bits, and root
// ignores them everywhere.
func unwritableDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory mode bits do not deny creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root is not denied by directory mode bits")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("the filesystem lets a 0500 directory take files")
	}
	return dir
}

// The Win32 mapping builds everywhere; its two answers are pinned here.
func TestProcessAliveWin32Mapping(t *testing.T) {
	if openFailureMeansAlive(syscall.Errno(87)) {
		t.Error("ERROR_INVALID_PARAMETER read as alive")
	}
	if !openFailureMeansAlive(syscall.Errno(5)) {
		t.Error("ERROR_ACCESS_DENIED read as dead — a holder we cannot see is a holder")
	}
	if !exitCodeMeansAlive(259) || exitCodeMeansAlive(0) {
		t.Error("exit code mapping wrong")
	}
}
