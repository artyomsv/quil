package main

import (
	"errors"
	"testing"
	"time"
)

func TestAcquireStartupLock_SecondHolderWaitsThenFails_ReleaseFreesIt(t *testing.T) {
	dir := t.TempDir()
	release1, err := acquireStartupLock(dir, 200*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	start := time.Now()
	if _, err := acquireStartupLock(dir, 200*time.Millisecond, 20*time.Millisecond); !errors.Is(err, errLockHeld) {
		t.Fatalf("second acquire err = %v, want errLockHeld", err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("second acquire gave up after %v, want it to retry for the wait budget", waited)
	}
	release1()
	release3, err := acquireStartupLock(dir, 200*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release3()
}

func TestAcquireStartupLock_HolderReleasingDuringWait_Succeeds(t *testing.T) {
	dir := t.TempDir()
	release1, err := acquireStartupLock(dir, time.Second, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		release1()
	}()
	release2, err := acquireStartupLock(dir, 2*time.Second, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire while the holder releases: %v", err)
	}
	release2()
}

func TestAcquireStartupLock_MissingDir_IsCreated(t *testing.T) {
	dir := t.TempDir() + "/nested/home"
	release, err := acquireStartupLock(dir, 100*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire in a missing dir: %v", err)
	}
	release()
}
