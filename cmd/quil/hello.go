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
//
// A var rather than a const so tests can drive the loop without spending five
// seconds per tick — the same reason clientSendTimeout is one.
var statPushInterval = 5 * time.Second

// statSender is the narrow slice of *ipc.Client this file needs.
//
// Declared at the consumer, and deliberately exposing only the DROPPABLE send:
// a stat must never be able to reach the must-deliver path, and a one-method
// interface makes that structural rather than a rule someone has to remember.
type statSender interface {
	SendDroppable(*ipc.Message) error
}

// sampleSelfStat reads this process's own cpu and rss.
//
// Unknown unless proven otherwise: zero CPU renders as "0%" and claims the
// process is idle, which is the wrong claim in a dialog opened to find
// something that spins.
func sampleSelfStat(sampler *proctree.SelfSampler, now time.Time) ipc.ClientStatPayload {
	p := ipc.ClientStatPayload{CPUPct: proctree.UnknownCPU}
	if pct, ok := sampler.Percent(now); ok {
		p.CPUPct = pct
	}
	self := os.Getpid()
	if m := memreport.ProcRSSBatch([]int{self}); m != nil {
		p.RSSBytes = m[self]
	}
	return p
}

// startClientStatReports pushes this process's own cpu and rss on a tick.
//
// Best effort, like the hello: an older daemon drops the unknown message type,
// and a send failure ends the loop rather than retrying.
//
// The send is DROPPABLE, and that is load-bearing rather than tidy. Client.Send
// closes the connection when the must-deliver queue stays full past
// clientSendTimeout — survivable for the TUI, which redials, but the MCP bridge
// dials once in connectToDaemon and never redials, so a background push on that
// path would close the bridge's conn and fail every later tool call for the
// life of the process, with nothing in flight to surface the error. A dropped
// stat costs an em dash for one tick instead.
//
// The error exit is still the reconnect story: SendDroppable reports
// ErrSendOverflow for a conn that is genuinely closed (as opposed to merely
// full), and a redial calls sendClientHello again and starts a fresh loop.
// Retrying here would leave one goroutine per reconnect pushing at a dead
// socket.
//
// One loop per CLIENT, not per process: every caller is handed a freshly
// dialled client, so calling this twice on the same one would double the push
// rate with nothing to notice it.
func startClientStatReports(client statSender) {
	if client == nil {
		return
	}
	go func() {
		sampler := proctree.NewSelfSampler()
		// Primed here so the FIRST tick carries a real percentage. A rate needs
		// two readings, and without this the reading at +5 s is the sampler's
		// first, leaving cpu unknown until +10 s.
		sampler.Percent(time.Now())

		t := time.NewTicker(statPushInterval)
		defer t.Stop()

		for range t.C {
			msg, err := ipc.NewMessage(ipc.MsgClientStat, sampleSelfStat(sampler, time.Now()))
			if err != nil {
				continue
			}
			if err := client.SendDroppable(msg); err != nil {
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
