//go:build windows

package winjob

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The untagged files mirror these so Linux CI can test the decisions.
func TestJobConstants_MatchXSys(t *testing.T) {
	if jobLimitBreakawayOK != windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK {
		t.Errorf("jobLimitBreakawayOK = %#x, want %#x", jobLimitBreakawayOK, windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK)
	}
	if jobLimitKillOnJobClose != windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE {
		t.Errorf("jobLimitKillOnJobClose = %#x, want %#x", jobLimitKillOnJobClose, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	}
	if wtsStateActive != windows.WTSActive {
		t.Errorf("wtsStateActive = %d, want %d", wtsStateActive, windows.WTSActive)
	}
	if wtsStateDisconnected != windows.WTSDisconnected {
		t.Errorf("wtsStateDisconnected = %d, want %d", wtsStateDisconnected, windows.WTSDisconnected)
	}
}

// Logged, not asserted: the answer depends on the host.
func TestUserHasInteractiveSession_DesktopHost_Answers(t *testing.T) {
	has, err := UserHasInteractiveSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("hasInteractiveSession=%v", has)
}

// Logged, not asserted: the answer depends on the host.
func TestAboveMedium_Answers(t *testing.T) {
	above, err := AboveMedium()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("aboveMedium=%v", above)
}

// Logged, not asserted: whether the test host itself sits in a job, and which
// one, depends on the terminal it runs from.
func TestJobState_Answers(t *testing.T) {
	info, err := JobState()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("job=%+v", info)
}

// A child started with LoweredToken runs at Medium with Administrators
// deny-only. From a Medium desktop this proves the "already Medium" branch
// only; the "above Medium" branch is proven over ssh (plan Task 6).
func TestLoweredToken_ChildIsMediumAndNotAdmin(t *testing.T) {
	tok, err := LoweredToken()
	if err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe"), "/groups")
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok), HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("whoami under lowered token: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "Medium Mandatory Level") {
		t.Errorf("child is not Medium:\n%s", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "S-1-5-32-544") && !strings.Contains(line, "Group used for deny only") {
			t.Errorf("Administrators is not deny-only: %s", line)
		}
	}
}

// schtasks accepts LogonTaskXML's output. Registers a uniquely named task
// that points at a harmless launcher, then deletes it.
func TestLogonTaskXML_AcceptedBySchtasks(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("quil-winjob-test-%d", time.Now().UnixNano())
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	raw, err := LogonTaskXML(TaskSpec{
		UserSID: sid, LauncherPath: filepath.Join(sys, "cmd.exe"),
		QuildPath: filepath.Join(sys, "cmd.exe"), Home: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	xmlPath := filepath.Join(t.TempDir(), "task.xml")
	if err := os.WriteFile(xmlPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := Schtasks([]string{"/Create", "/TN", name, "/XML", xmlPath, "/F"}, false); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = Schtasks([]string{"/Delete", "/TN", name, "/F"}, false) })
	if !TaskExists(name) {
		t.Fatalf("task %q not found after create", name)
	}
}
