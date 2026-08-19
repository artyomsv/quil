// Package procscan enumerates the process tree and classifies quil's own
// orphaned children.
//
// Split like internal/notify: every file with logic is platform-neutral, and the
// build-tagged files hold enumeration syscalls only. CI is Linux, so anything
// behind //go:build windows is never compiled by dev.sh test — putting the
// classification here is what makes it testable at all.
//
// Scoped as a DIAGNOSTIC. The measurement that motivated it found zero orphaned
// bridges: watchParentExit is working, and the runaway process in that session
// was an MCP server quil does not own. So this reports what is running and
// offers to kill only what it can positively identify as quil's own orphan.
package procscan

import (
	"strings"
	"time"
)

// Process is one entry from the OS process table.
//
// Start is zero when the platform could not read it — a process that refuses
// OpenProcess, most commonly. Callers must treat a zero Start as "unknown",
// never as "epoch": see hasLiveParent for why that direction is the safe one.
type Process struct {
	PID     int
	PPID    int
	Name    string
	Cmdline string
	Start   time.Time
}

// Kind classifies a process for display. The dialog groups by this, and only
// KindOrphanBridge is ever offered for killing.
type Kind int

const (
	// KindOther is anything not recognised as quil's own.
	KindOther Kind = iota
	// KindTUI is the quil client.
	KindTUI
	// KindDaemon is quild.
	KindDaemon
	// KindBridge is a `quil mcp` bridge with a live parent.
	KindBridge
	// KindOrphanBridge is a `quil mcp` bridge whose parent is gone.
	KindOrphanBridge
)

func (k Kind) String() string {
	switch k {
	case KindTUI:
		return "TUI"
	case KindDaemon:
		return "daemon"
	case KindBridge:
		return "bridge"
	case KindOrphanBridge:
		return "orphan bridge"
	default:
		return "other"
	}
}

// IsBridge reports whether a process is one of quil's MCP bridges.
//
// Matched on the SUBCOMMAND rather than the image name, because the update swap
// renames the binary aside: a bridge can legitimately be running as
// quil.exe.old.3 while its command line still says `quil mcp`. Observed exactly
// that in production — a bridge from two days earlier pinning an old binary.
func (p Process) IsBridge() bool {
	f := strings.Fields(p.Cmdline)
	if len(f) < 2 {
		return false
	}
	if !strings.Contains(strings.ToLower(f[0]), "quil") {
		return false
	}
	for _, a := range f[1:] {
		if a == "mcp" {
			return true
		}
	}
	return false
}

// IsDaemon reports whether a process is quild.
func (p Process) IsDaemon() bool {
	f := strings.Fields(p.Cmdline)
	if len(f) == 0 {
		return false
	}
	base := strings.ToLower(f[0])
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	return strings.HasPrefix(base, "quild")
}

// Classify assigns each process a Kind, given the whole snapshot and the PID of
// the caller.
//
// selfPID is never classified as an orphan, so the dialog can never offer to
// kill the process drawing it.
func Classify(procs []Process, selfPID int) map[int]Kind {
	byPID := make(map[int]Process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}

	out := make(map[int]Kind, len(procs))
	for _, p := range procs {
		switch {
		case p.IsDaemon():
			out[p.PID] = KindDaemon
		case p.IsBridge():
			if p.PID != selfPID && !hasLiveParent(p, byPID) {
				out[p.PID] = KindOrphanBridge
			} else {
				out[p.PID] = KindBridge
			}
		case isQuilTUI(p):
			out[p.PID] = KindTUI
		default:
			out[p.PID] = KindOther
		}
	}
	return out
}

// isQuilTUI reports a quil binary invoked with no subcommand, i.e. the client.
func isQuilTUI(p Process) bool {
	f := strings.Fields(p.Cmdline)
	if len(f) == 0 {
		return false
	}
	base := strings.ToLower(f[0])
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if !strings.HasPrefix(base, "quil") || strings.HasPrefix(base, "quild") {
		return false
	}
	for _, a := range f[1:] {
		if !strings.HasPrefix(a, "-") {
			return false // has a subcommand — mcp, activate, notify, …
		}
	}
	return true
}

// hasLiveParent reports whether p's parent is present in the snapshot AND
// plausibly its real parent.
//
// A parent that started AFTER its supposed child is treated as ABSENT: PIDs are
// reused, and an impostor wearing a dead process's number must not make a real
// orphan look adopted. Same rule as parentHandleTrustworthy in
// cmd/quil/parentwatch_windows.go.
//
// An UNKNOWN start time (zero on either side) counts as live, not dead. The
// consequence of being wrong differs by direction: a missed orphan is a process
// the dialog does not offer to kill, while a false orphan invites the user to
// kill something still in use.
func hasLiveParent(p Process, byPID map[int]Process) bool {
	parent, ok := byPID[p.PPID]
	if !ok {
		return false
	}
	if parent.Start.IsZero() || p.Start.IsZero() {
		return true
	}
	return !parent.Start.After(p.Start)
}

// Orphans returns the quil bridges whose parent is gone, which is the only set
// the dialog offers to kill.
//
// Deliberately narrow. A parentless process that is NOT ours — the npx MCP
// churn that prompted this feature, say — is real information and is reported,
// but it is not something quil may offer to terminate.
func Orphans(procs []Process, selfPID int) []Process {
	kinds := Classify(procs, selfPID)
	var out []Process
	for _, p := range procs {
		if kinds[p.PID] == KindOrphanBridge {
			out = append(out, p)
		}
	}
	return out
}
