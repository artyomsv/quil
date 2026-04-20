//go:build windows

package memreport

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcRSSBatch_Windows_SelfPID(t *testing.T) {
	self := os.Getpid()
	got := procRSSBatch([]int{self})
	rss := got[self]
	if rss == 0 {
		t.Errorf("procRSSBatch(self) = 0, want > 0")
	}
}

func TestProcRSSBatch_Windows_Child(t *testing.T) {
	// `timeout /t 3 /nobreak` is a Windows sleep equivalent.
	cmd := exec.Command("cmd", "/c", "timeout", "/t", "3", "/nobreak")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	time.Sleep(100 * time.Millisecond)

	got := procRSSBatch([]int{cmd.Process.Pid})
	if got[cmd.Process.Pid] == 0 {
		t.Errorf("procRSSBatch(child) = 0, want > 0")
	}
}

func TestProcRSSBatch_Windows_InvalidPID(t *testing.T) {
	got := procRSSBatch([]int{2_147_483_647})
	if rss := got[2_147_483_647]; rss != 0 {
		t.Errorf("invalid PID got rss=%d, want 0 (missing)", rss)
	}
}
