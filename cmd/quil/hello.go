package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/proctree"
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

	// Identity is sent once; cpu and rss have to keep arriving. Started HERE
	// rather than at the four call sites for the reason the hello itself is
	// funnelled through this helper: a new durable dial that forgets it would
	// not fail loudly, it would just show an em dash forever in a diagnostic
	// dialog nobody opens until something is already wrong.
	startClientStatReports(client)
}

// statPushInterval matches the daemon's proc-collector tick, so a client's
// report and the daemon's own enumeration describe roughly the same window.
const statPushInterval = 5 * time.Second

// startClientStatReports pushes this process's own cpu and rss on a tick.
//
// Best effort, like the hello: an older daemon drops the unknown message type,
// and a send failure ends the loop rather than retrying. That exit is the
// reconnect story — a redial calls sendClientHello again and starts a fresh
// loop, while this one's next Send fails on the dead conn and returns. Retrying
// here instead would leave one goroutine per reconnect pushing at a socket
// nobody reads.
//
// The first tick always reports an unknown CPU: a rate needs two readings. RSS
// is valid immediately, so a fresh process shows its memory and an em dash for
// cpu until the tick after.
func startClientStatReports(client *ipc.Client) {
	if client == nil {
		return
	}
	go func() {
		sampler := proctree.NewSelfSampler()
		t := time.NewTicker(statPushInterval)
		defer t.Stop()

		for range t.C {
			// Unknown unless proven otherwise. Zero would render as "0%" and
			// claim this process is idle.
			p := ipc.ClientStatPayload{CPUPct: proctree.UnknownCPU}
			if pct, ok := sampler.Percent(time.Now()); ok {
				p.CPUPct = pct
			}
			self := os.Getpid()
			if m := memreport.ProcRSSBatch([]int{self}); m != nil {
				p.RSSBytes = m[self]
			}

			msg, err := ipc.NewMessage(ipc.MsgClientStat, p)
			if err != nil {
				continue
			}
			if err := client.Send(msg); err != nil {
				return
			}
		}
	}()
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
