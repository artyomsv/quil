package main

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// stubLogonSeams replaces every Task Scheduler seam with a fake that records
// calls, and restores each on cleanup.
func stubLogonSeams(t *testing.T) (calls *[][]string, lowered *[]bool) {
	t.Helper()
	var c [][]string
	var l []bool
	prevSchtasks := schtasksFn
	prevTaskExists := taskExistsFn
	prevCurrentUserSID := currentUserSIDFn
	prevAboveMedium := aboveMediumFn
	prevExecutable := executableFn
	prevStat := statFn
	prevFindDaemonBinary := findDaemonBinaryFn
	schtasksFn = func(args []string, low bool) (string, error) {
		c = append(c, args)
		l = append(l, low)
		return "SUCCESS", nil
	}
	taskExistsFn = func(string) bool { return true }
	currentUserSIDFn = func() (string, error) { return "S-1-5-21-1-2-3-1001", nil }
	aboveMediumFn = func() (bool, error) { return true, nil }
	// Forward slashes so filepath.Dir/Join split it correctly under the
	// Linux test runner too — path/filepath only treats '\' as a separator
	// on GOOS=windows, and the real production exe path always is native.
	executableFn = func() (string, error) { return `C:/t/quil.exe`, nil }
	statFn = func(string) (fs.FileInfo, error) { return nil, nil }
	findDaemonBinaryFn = func() string { return `C:\t\quild.exe` }
	t.Cleanup(func() {
		schtasksFn = prevSchtasks
		taskExistsFn = prevTaskExists
		currentUserSIDFn = prevCurrentUserSID
		aboveMediumFn = prevAboveMedium
		executableFn = prevExecutable
		statFn = prevStat
		findDaemonBinaryFn = prevFindDaemonBinary
	})
	return &c, &l
}

func TestInstallLogon_MissingLauncher_Refuses(t *testing.T) {
	calls, _ := stubLogonSeams(t)
	statFn = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	if code := runInstallLogon(nil); code == 0 {
		t.Fatal("install-logon succeeded without quil-activate.exe")
	}
	if len(*calls) != 0 {
		t.Errorf("schtasks ran: %v", *calls)
	}
}

func TestInstallLogon_Registers_LoweredWhenAboveMedium(t *testing.T) {
	t.Setenv("QUIL_HOME", `C:\q\.quil`)
	calls, lowered := stubLogonSeams(t)

	if code := runInstallLogon(nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*calls) != 1 || (*calls)[0][0] != "/Create" || !slices.Contains((*calls)[0], "/F") {
		t.Fatalf("schtasks calls = %v", *calls)
	}
	if !(*lowered)[0] {
		t.Error("registration from a High token did not run lowered")
	}
	if !strings.HasPrefix((*calls)[0][2], "Quil daemon") {
		t.Errorf("task name %q", (*calls)[0][2])
	}
}

func TestInstallLogon_Remove_NoTask_IsNotAnError(t *testing.T) {
	calls, _ := stubLogonSeams(t)
	taskExistsFn = func(string) bool { return false }
	if code := runInstallLogon([]string{"--remove"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*calls) != 0 {
		t.Errorf("schtasks ran for a missing task: %v", *calls)
	}
}

func TestInstallLogon_UnknownArg_Refuses(t *testing.T) {
	stubLogonSeams(t)
	if code := runInstallLogon([]string{"--bogus"}); code == 0 {
		t.Fatal("accepted --bogus")
	}
}
