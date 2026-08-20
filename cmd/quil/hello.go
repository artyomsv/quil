package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// processStart is when this process began, for the uptime it reports.
//
// A package var set at init rather than a value threaded through: every dial
// site needs it and none of them has a natural place to carry it.
var processStart = time.Now()

// Roles a quil process can report itself as.
const (
	helloRoleTUI    = "tui"
	helloRoleBridge = "bridge"
)

// sendClientHello tells the daemon what this process is.
//
// EVERY durable dial goes through this one helper, and that is the whole point.
// A client that forgets to identify itself does not fail loudly — it silently
// disappears from the process dialog, or worse, is counted as an unidentified
// client from a build predating the feature. Funnelling it here means a new
// durable dial site cannot quietly acquire that bug, the same discipline
// enqueueInput uses for ordered pane input.
//
// NOT called from ipc.NewClient. Most dial sites in this binary are short-lived
// probes — `quil status`, the version gate, the daemonctl dials — that ask one
// thing and close; registering those would populate the dialog with processes
// that no longer exist. This mirrors the daemon's own distinction between an
// ATTACHED client and a merely CONNECTED conn.
//
// Best effort by design: the return value is deliberately ignored at every call
// site. An older daemon drops the unknown message, and a send failure here must
// never block a client from working — the only consequence is a missing row in
// a diagnostic dialog.
func sendClientHello(client *ipc.Client, role string) {
	if client == nil {
		return
	}
	msg, err := ipc.NewMessage(ipc.MsgClientHello, ipc.ClientHelloPayload{
		Role:    role,
		PID:     os.Getpid(),
		Version: version,
		ExeName: currentExeName(),
		// A DURATION, never a start timestamp: in remote mode the daemon's
		// clock is a different machine's, so a timestamp would be skewed by
		// whatever those two clocks disagree about.
		UptimeMS: time.Since(processStart).Milliseconds(),
	})
	if err != nil {
		return
	}
	_ = client.Send(msg)
}

// currentExeName is the basename of the running binary.
//
// The basename specifically, so a bridge still executing quil.exe.old.3 after
// an in-place update swap renamed the binary aside is visible as exactly that.
// A two-day-old bridge pinning an old binary was the observation that motivated
// this section in the first place.
func currentExeName() string {
	exe, err := os.Executable()
	if err != nil {
		return "quil"
	}
	return filepath.Base(exe)
}
