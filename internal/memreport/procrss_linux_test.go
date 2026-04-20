//go:build linux

package memreport

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcRSSBatch_Linux_SelfPID(t *testing.T) {
	self := os.Getpid()
	got := procRSSBatch([]int{self})
	rss, ok := got[self]
	if !ok {
		t.Fatalf("procRSSBatch did not return an entry for self pid %d", self)
	}
	if rss == 0 {
		t.Errorf("procRSSBatch(self) = 0, want > 0")
	}
	if rss > 5*1024*1024*1024 { // 5 GB ceiling sanity
		t.Errorf("procRSSBatch(self) = %d, unexpectedly large", rss)
	}
}

func TestProcRSSBatch_Linux_Child(t *testing.T) {
	cmd := exec.Command("sleep", "2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Give /proc/<pid>/status a moment to populate VmRSS.
	time.Sleep(50 * time.Millisecond)

	got := procRSSBatch([]int{cmd.Process.Pid})
	rss := got[cmd.Process.Pid]
	if rss == 0 {
		t.Errorf("procRSSBatch(child) = 0, want > 0")
	}
}

func TestProcRSSBatch_Linux_NonexistentPID(t *testing.T) {
	got := procRSSBatch([]int{2_147_483_647}) // very high PID unlikely to exist
	if rss, ok := got[2_147_483_647]; ok && rss != 0 {
		t.Errorf("nonexistent PID got rss=%d, want missing or 0", rss)
	}
}
