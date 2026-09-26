package remoteinstall

// Remedy is what a failed remote launch calls for.
type Remedy int

const (
	// RemedyNone means the failure is not about a missing or broken install.
	RemedyNone Remedy = iota

	// RemedyInstall means the remote shell could not find quil.
	RemedyInstall

	// RemedyReinstall means the remote shell found quil and could not execute
	// it — almost always a binary built for another architecture.
	RemedyReinstall

	// RemedyUpgrade means quil ran over there and reported a version this TUI
	// cannot attach to. Distinct from RemedyInstall because the two produce
	// opposite headlines, and saying "Quil is not installed" about a daemon
	// that just answered with its version is a visible contradiction — the
	// probe on the very next line reports the install it supposedly lacks.
	RemedyUpgrade

	// RemedyProbe means the far side exited 1 before any byte arrived. Both
	// cmd.exe and PowerShell use 1 for "command not found" (measured on
	// Windows 10, issue #236 — not 9009), but so does `quil --stdio` refusing
	// to start. The code cannot tell them apart, so the caller asks the host
	// with the probe: no quil ⇒ install; quil present ⇒ it exited on its own.
	RemedyProbe
)

func (r Remedy) String() string {
	switch r {
	case RemedyInstall:
		return "install"
	case RemedyReinstall:
		return "reinstall"
	case RemedyUpgrade:
		return "upgrade"
	case RemedyProbe:
		return "probe"
	default:
		return "none"
	}
}

// POSIX shells reserve these two codes for "I could not run the command you
// asked for", and ssh passes the remote command's status through untouched —
// only its own failures become 255. That is what makes them a usable signal
// about the far side's filesystem rather than about the connection.
const (
	exitCommandNotFound = 127
	exitNotExecutable   = 126
)

// exitGeneralFailure is what cmd.exe and PowerShell answer for a command they
// cannot find — and what quil itself answers when it refuses to start. See
// RemedyProbe.
const exitGeneralFailure = 1

// ClassifyExit maps an ssh exit status to a remedy.
//
// established is the override, not a refinement: a link that delivered even one
// byte ran quil successfully over there, so however it exited afterwards is a
// remote crash rather than a missing binary. Without that check a daemon
// crashing with status 127 would be misdiagnosed as "not installed" and the
// user offered an install that fixes nothing.
func ClassifyExit(exitCode int, established bool) Remedy {
	if established {
		return RemedyNone
	}
	switch exitCode {
	case exitCommandNotFound:
		return RemedyInstall
	case exitNotExecutable:
		return RemedyReinstall
	case exitGeneralFailure:
		return RemedyProbe
	default:
		return RemedyNone
	}
}
