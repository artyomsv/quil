package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/winjob"
)

// Seams for the Task Scheduler effects, so the command's decisions test on
// Linux. The real calls are exercised natively (plan Task 2 / Task 6).
var (
	schtasksFn       = winjob.Schtasks
	taskExistsFn     = winjob.TaskExists
	currentUserSIDFn = winjob.CurrentUserSID
	aboveMediumFn    = winjob.AboveMedium
	executableFn     = os.Executable
	statFn           = os.Stat
)

// logonTaskNameForThisBuild names this variant's task for the current
// QUIL_HOME. startDaemon computes the same name to find the task.
func logonTaskNameForThisBuild() string {
	dir := config.QuilDir()
	return winjob.LogonTaskName(daemonName(), dir, config.IsDefaultQuilDir(dir))
}

// runInstallLogon implements `quil daemon install-logon [--remove]` and returns
// the process exit code.
func runInstallLogon(args []string) int {
	remove := false
	for _, a := range args {
		if a != "--remove" {
			fmt.Fprintf(os.Stderr, "usage: quil daemon install-logon [--remove]\n")
			return 1
		}
		remove = true
	}
	name := logonTaskNameForThisBuild()
	lowered, err := aboveMediumFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: %v\n", err)
		return 1
	}

	if remove {
		if !taskExistsFn(name) {
			fmt.Println("no logon task registered")
			return 0
		}
		if out, err := schtasksFn([]string{"/Delete", "/TN", name, "/F"}, lowered); err != nil {
			fmt.Fprintf(os.Stderr, "remove logon task: %v\n%s\n%s", err, out, accessHint)
			return 1
		}
		fmt.Printf("removed logon task %q\n", name)
		return 0
	}

	exe, err := executableFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: locate this executable: %v\n", err)
		return 1
	}
	launcher := filepath.Join(filepath.Dir(exe), "quil-activate.exe")
	if _, err := statFn(launcher); err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: %s is missing; it starts the daemon without a console window\n", launcher)
		return 1
	}
	quild := logonDaemonBinary(exe)
	sid, err := currentUserSIDFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: %v\n", err)
		return 1
	}
	xml, err := winjob.LogonTaskXML(winjob.TaskSpec{UserSID: sid, LauncherPath: launcher, QuildPath: quild, Home: config.QuilDir()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: %v\n", err)
		return 1
	}
	f, err := os.CreateTemp("", "quil-logon-*.xml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: %v\n", err)
		return 1
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(xml)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		fmt.Fprintf(os.Stderr, "install-logon: write task definition: %v\n", err)
		return 1
	}
	if out, err := schtasksFn([]string{"/Create", "/TN", name, "/XML", f.Name(), "/F"}, lowered); err != nil {
		fmt.Fprintf(os.Stderr, "register logon task: %v\n%s\n%s", err, out, accessHint)
		return 1
	}
	fmt.Printf("registered logon task %q: the daemon now starts when you log on.\n", name)
	fmt.Println("Start it now with: quil daemon start")
	return 0
}

// logonDaemonBinary picks the daemon the logon task will start: the one beside
// exe first, then whatever findDaemonBinary finds. The order is the reverse of
// findDaemonBinary's for the reason findDaemonBinaryForUpgrade gives: the
// sibling shipped in the same archive as this quil, while a quild found on PATH
// can be an older install. The task pins the path it is given, so a PATH-first
// pick here would start the old daemon at every logon and fail the version gate
// every time.
func logonDaemonBinary(exe string) string {
	sibling := filepath.Join(filepath.Dir(exe), daemonName()+".exe")
	if _, err := statFn(sibling); err == nil {
		return sibling
	}
	return findDaemonBinaryFn()
}

// accessHint explains the one failure schtasks reports without a useful
// code: a task registered from an ssh or elevated session by an older build is
// owned by Administrators.
const accessHint = "If this says access is denied, the task was registered from an ssh session\n" +
	"or an elevated terminal. Run this command there.\n"
