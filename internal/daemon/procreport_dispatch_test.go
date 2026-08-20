package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// These drive the real wire messages through handleMessage rather than calling
// the handlers directly.
//
// A dispatch `case` present in the source is not proof that it is reachable.
// Every other test in this package calls the pure functions underneath
// (validateKillTarget, helloRegistry.describe, procCollector.collect), so all
// three arms could be disconnected — `d.handleXxx(conn, msg)` replaced with
// `_ = conn` — and the package stayed fully green. That is the same wiring gap
// that let the previous version of this feature ship a confirm branch nothing
// could reach.

func TestHandleMessage_ClientHelloIsWiredUp(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	conn := &ipc.Conn{}

	msg, err := ipc.NewMessage(ipc.MsgClientHello, ipc.ClientHelloPayload{
		Role: "bridge", PID: 4242, Version: "1.62.4", ExeName: "quil.exe.old.3", UptimeMS: 90_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(conn, msg)

	got, _ := d.hellos.describe([]*ipc.Conn{conn}, "1.62.6", time.Now())
	if len(got) != 1 {
		t.Fatalf("registry described %d processes, want 1 — the dispatch arm for "+
			"MsgClientHello is not reaching handleClientHello", len(got))
	}
	if got[0].PID != 4242 || got[0].Role != "bridge" {
		t.Errorf("described %+v, want the payload that was sent", got[0])
	}
	if !got[0].Stale {
		t.Error("a client on an older version was not marked stale")
	}
}

// The registry must reject a hello that names no role or no PID, rather than
// storing a row the dialog would render as a blank process.
func TestHandleMessage_IncompleteClientHelloIsIgnored(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	conn := &ipc.Conn{}

	for _, p := range []ipc.ClientHelloPayload{
		{Role: "", PID: 1},
		{Role: "tui", PID: 0},
		{Role: "tui", PID: -5},
	} {
		msg, err := ipc.NewMessage(ipc.MsgClientHello, p)
		if err != nil {
			t.Fatal(err)
		}
		d.handleMessage(conn, msg)
	}

	if got, _ := d.hellos.describe([]*ipc.Conn{conn}, "1.62.6", time.Now()); len(got) != 0 {
		t.Errorf("described %d processes from incomplete payloads, want 0", len(got))
	}
}

// Identity strings are retained per connection for its whole lifetime and
// copied into every tree-bearing response. Unbounded, one connection holding a
// multi-megabyte Role makes the response fail to marshal — which is logged and
// dropped, so the dialog sits on "Loading…" and the status-bar total freezes
// for every client, not just the hostile one.
func TestHandleMessage_ClientHelloFieldsAreBounded(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	conn := &ipc.Conn{}

	huge := make([]byte, 64*1024)
	for i := range huge {
		huge[i] = 'A'
	}
	msg, err := ipc.NewMessage(ipc.MsgClientHello, ipc.ClientHelloPayload{
		Role: string(huge), PID: 7, Version: string(huge), ExeName: string(huge),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(conn, msg)

	got, _ := d.hellos.describe([]*ipc.Conn{conn}, "1.62.6", time.Now())
	if len(got) != 1 {
		t.Fatalf("described %d, want 1", len(got))
	}
	for name, v := range map[string]string{
		"Role": got[0].Role, "Version": got[0].Version, "ExeName": got[0].ExeName,
	} {
		if len(v) > maxHelloField {
			t.Errorf("%s retained %d bytes, want <= %d", name, len(v), maxHelloField)
		}
	}
}

// A status-bar poll must NOT start the process collector. The flag gates both
// what is on the wire and whether the daemon enumerates at all, so a treeless
// request starting it would run a process scan for the life of the session with
// nobody looking at the result.
func TestHandleMessage_ResourceReportWithoutTreesDoesNotStartTheCollector(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())

	msg, err := ipc.NewMessage(ipc.MsgResourceReportReq, ipc.ResourceReportReqPayload{WithTrees: false})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	d.procReport.mu.Lock()
	running := d.procReport.running
	d.procReport.mu.Unlock()
	if running {
		t.Error("a status-bar poll started the process collector")
	}
}

// ...and a tree request MUST start it. This is the arm that proves
// MsgResourceReportReq reaches handleResourceReportReq at all.
func TestHandleMessage_ResourceReportWithTreesStartsTheCollector(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())

	msg, err := ipc.NewMessage(ipc.MsgResourceReportReq, ipc.ResourceReportReqPayload{WithTrees: true})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	d.procReport.mu.Lock()
	running := d.procReport.running
	deadline := d.procReport.deadline
	d.procReport.mu.Unlock()

	if !running {
		t.Fatal("a tree request did not start the collector — the dispatch arm " +
			"for MsgResourceReportReq is not reaching handleResourceReportReq")
	}
	if deadline.Before(time.Now()) {
		t.Error("the gate deadline was not renewed, so the collector stops on its next tick")
	}
}

// The kill arm, observed through its single-flight.
//
// Pre-claiming the flight makes the handler take its synchronous busy path, so
// there is no goroutine to race: if the arm is wired, the claim is still held
// afterwards and nothing was spawned.
func TestHandleMessage_KillProcessIsWiredUpAndSingleFlighted(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())

	msg, err := ipc.NewMessage(ipc.MsgKillProcessReq, ipc.KillProcessReqPayload{
		PaneID: "no-such-pane", PID: 999999, StartMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// With the flight already claimed, a wired handler refuses synchronously.
	d.killRunning.Store(true)
	d.handleMessage(nil, msg)
	if !d.killRunning.Load() {
		t.Error("the busy path released a flight it did not claim")
	}

	// Released, the same message must claim it and then give it back once the
	// worker goroutine finishes. Never claiming means the arm is disconnected.
	d.killRunning.Store(false)
	d.handleMessage(nil, msg)

	deadline := time.Now().Add(5 * time.Second)
	claimed := false
	for time.Now().Before(deadline) {
		if d.killRunning.Load() {
			claimed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !claimed {
		t.Fatal("the kill request never claimed the single-flight — the dispatch " +
			"arm for MsgKillProcessReq is not reaching handleKillProcessReq")
	}
	for time.Now().Before(deadline) {
		if !d.killRunning.Load() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("the single-flight was never released; every later kill is refused as busy")
}
