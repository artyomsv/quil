package binswap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// TestLockFile_ExcludesWithinProcess: two LockFile calls in one process are
// two open file descriptions (flock) or two handles (LockFileEx), and they
// exclude each other — the second gets ErrLockHeld until the first releases.
// The file itself stays after release; nothing unlinks a lock.
func TestLockFile_ExcludesWithinProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")
	release, err := LockFile(path)
	if err != nil {
		t.Fatalf("first LockFile: %v", err)
	}
	if _, err := LockFile(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second LockFile in the same process = %v, want ErrLockHeld", err)
	}
	release()
	release2, err := LockFile(path)
	if err != nil {
		t.Fatalf("LockFile after release: %v", err)
	}
	release2()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the lock file was removed on release: %v", err)
	}
}

// TestLockFileHelper is the second PROCESS of
// TestLockFile_ExcludesAnotherProcess: re-executed with the lock path in the
// environment, it takes the lock, says so on stdout and parks until killed.
// Without the variable it is nothing.
func TestLockFileHelper(t *testing.T) {
	path := os.Getenv("QUIL_BINSWAP_LOCK_PATH")
	if path == "" {
		return
	}
	if _, err := LockFile(path); err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println("locked")
	select {}
}

// TestLockFile_ExcludesAnotherProcess: the point of the OS lock. A second
// process holding the lock makes LockFile answer ErrLockHeld — no pid to
// judge, no file to steal — and the kernel drops the lock when that process
// dies, so the survivor acquires it with no reclaim step.
func TestLockFile_ExcludesAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")
	helper := exec.Command(os.Args[0], "-test.run=^TestLockFileHelper$")
	helper.Env = append(os.Environ(), "QUIL_BINSWAP_LOCK_PATH="+path)
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		helper.Process.Kill()
		helper.Wait()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case line := <-lines:
		if line != "locked" {
			t.Fatalf("helper said %q, want locked", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the helper process never took the lock")
	}

	if _, err := LockFile(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("LockFile while another process holds it = %v, want ErrLockHeld", err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	helper.Wait()
	release, err := LockFile(path)
	if err != nil {
		t.Fatalf("LockFile after the holder died = %v, want it free", err)
	}
	release()
}

// TestWaitLock_WaitsForTheHolder: a second updater waits for the first and
// then proceeds; the wait is announced once and bounded.
func TestWaitLock_WaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.lock")
	release, err := LockFile(path)
	if err != nil {
		t.Fatalf("LockFile: %v", err)
	}

	var waits int
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		rel, err := WaitLock(context.Background(), path, 5*time.Second, func() {
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
	release, err = LockFile(path)
	if err != nil {
		t.Fatalf("LockFile: %v", err)
	}
	defer release()
	if _, err := WaitLock(context.Background(), path, 2*lockPollInterval, nil); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("WaitLock past maxWait = %v, want an error naming the lock", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WaitLock(ctx, path, time.Minute, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitLock with a cancelled ctx = %v, want context.Canceled", err)
	}
}

// TestLockFile_UnwritableDirFailsFast: a directory the lock cannot be created
// in is an error at once, never "held", and WaitLock does not wait on it (a
// waiter polling an EACCES would report a holder that is not there after the
// whole wait). TryLock, the pid-file lock, answers the same way.
func TestLockFile_UnwritableDirFailsFast(t *testing.T) {
	dir := unwritableDir(t)
	path := filepath.Join(dir, "tool.lock")
	if _, err := LockFile(path); err == nil || errors.Is(err, ErrLockHeld) {
		t.Fatalf("LockFile in an unwritable dir = %v, want the create error, not held", err)
	}
	if _, err := TryLock(path, testStale); err == nil || errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryLock in an unwritable dir = %v, want the create error, not held", err)
	}
	started := time.Now()
	_, err := WaitLock(context.Background(), path, time.Minute, func() { t.Error("WaitLock waited on an unwritable dir") })
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
