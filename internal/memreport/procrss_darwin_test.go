//go:build darwin

package memreport

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcRSSBatch_Darwin_SelfPID(t *testing.T) {
	self := os.Getpid()
	got := procRSSBatch([]int{self})
	rss, ok := got[self]
	if !ok {
		t.Fatalf("procRSSBatch did not return an entry for self pid %d", self)
	}
	if rss == 0 {
		t.Errorf("procRSSBatch(self) = 0, want > 0")
	}
}

func TestProcRSSBatch_Darwin_BatchMultiple(t *testing.T) {
	c1 := exec.Command("sleep", "2")
	c2 := exec.Command("sleep", "2")
	if err := c1.Start(); err != nil {
		t.Fatalf("start c1: %v", err)
	}
	if err := c2.Start(); err != nil {
		t.Fatalf("start c2: %v", err)
	}
	defer func() {
		_ = c1.Process.Kill()
		_ = c2.Process.Kill()
		_ = c1.Wait()
		_ = c2.Wait()
	}()
	time.Sleep(50 * time.Millisecond)

	got := procRSSBatch([]int{c1.Process.Pid, c2.Process.Pid})
	if got[c1.Process.Pid] == 0 || got[c2.Process.Pid] == 0 {
		t.Errorf("expected non-zero RSS for both children, got %v", got)
	}
}
