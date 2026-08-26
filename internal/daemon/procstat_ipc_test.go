package daemon_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
	"github.com/artyomsv/quil/internal/ipc"
)

// TestDaemon_ClientStatRoundTrip drives the whole self-reporting path over a
// real socket: hello, stat, then a resource report that has to carry both back.
//
// The unit tests cover each half, but the halves are joined by a message type
// string, a dispatch case and a payload shape — three things that can each be
// wrong while every unit test stays green. This is the test that fails if the
// dispatch case is missing.
func TestDaemon_ClientStatRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	cfg := config.Default()
	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { d.Stop() })

	sockPath := filepath.Join(tmp, "quild.sock")

	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("socket %s never became connectable", sockPath)
	}
	defer conn.Close()

	send := func(typ string, v any) {
		t.Helper()
		payload, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", typ, err)
		}
		if err := ipc.WriteMessage(conn, &ipc.Message{Type: typ, Payload: payload}); err != nil {
			t.Fatalf("write %s: %v", typ, err)
		}
	}

	self := os.Getpid()
	send(ipc.MsgClientHello, ipc.ClientHelloPayload{
		Role: "tui", PID: self, Version: "test-version", ExeName: "quil-test",
		UptimeMS: 1234,
	})
	send(ipc.MsgClientStat, ipc.ClientStatPayload{
		CPUPct: 11.5, RSSBytes: 703 << 20,
	})

	// Both are fire-and-forget, so there is no ack to wait on. Give the
	// dispatch goroutine a moment before asking for the report that must
	// contain them.
	time.Sleep(300 * time.Millisecond)

	reqPayload, err := json.Marshal(ipc.ResourceReportReqPayload{WithTrees: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ipc.WriteMessage(conn, &ipc.Message{
		Type: ipc.MsgResourceReportReq, ID: "stat1", Payload: reqPayload,
	}); err != nil {
		t.Fatalf("write resource report req: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp *ipc.Message
	for {
		m, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// The daemon broadcasts state on this connection too; skip anything
		// that is not the answer to the request just sent.
		if m.ID == "stat1" {
			resp = m
			break
		}
	}

	if resp.Type != ipc.MsgResourceReportResp {
		t.Fatalf("resp type = %s, want %s", resp.Type, ipc.MsgResourceReportResp)
	}

	var out ipc.ResourceReportRespPayload
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatalf("decode resource report: %v", err)
	}

	var mine *ipc.QuilProcInfo
	for i := range out.Quil {
		if out.Quil[i].PID == self && out.Quil[i].Role == "tui" {
			mine = &out.Quil[i]
			break
		}
	}
	if mine == nil {
		t.Fatalf("no tui row for pid %d in %d quil rows", self, len(out.Quil))
	}

	if mine.CPUPct != 11.5 {
		t.Errorf("CPUPct = %v, want 11.5 — the stat never reached describe()", mine.CPUPct)
	}
	if mine.RSSBytes != 703<<20 {
		t.Errorf("RSSBytes = %d, want %d", mine.RSSBytes, uint64(703)<<20)
	}
	if mine.StatAgeMS <= 0 {
		t.Errorf("StatAgeMS = %d, want a positive age measured on the daemon clock",
			mine.StatAgeMS)
	}
	if mine.StatAgeMS > 10_000 {
		t.Errorf("StatAgeMS = %d, implausibly large for a stat sent moments ago",
			mine.StatAgeMS)
	}
}

// The daemon's own row is assembled differently from every client row: it is
// not a hello, so it never touches the registry, and handleResourceReportReq
// appends it from procReport.SelfStat() directly. That assembly step is the one
// place in "quil reports itself" that no unit test reaches — mutating the
// SelfStat() call away leaves the whole package green.
func TestDaemon_OwnRowCarriesItsSelfMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	cfg := config.Default()
	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { d.Stop() })

	sockPath := filepath.Join(tmp, "quild.sock")
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("socket %s never became connectable", sockPath)
	}
	defer conn.Close()

	reqPayload, _ := json.Marshal(ipc.ResourceReportReqPayload{WithTrees: true})

	// The FIRST tree request starts the collector, whose first pass has nothing
	// to delta against — so the daemon's own CPU is legitimately unknown until a
	// later pass. RSS needs no baseline and must be present immediately, so that
	// is what this asserts on; asserting a percentage would be a timing race
	// against procTickInterval.
	var own *ipc.QuilProcInfo
	for attempt := 0; attempt < 3 && own == nil; attempt++ {
		id := "own" + strconv.Itoa(attempt)
		if err := ipc.WriteMessage(conn, &ipc.Message{
			Type: ipc.MsgResourceReportReq, ID: id, Payload: reqPayload,
		}); err != nil {
			t.Fatalf("write req: %v", err)
		}

		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var out ipc.ResourceReportRespPayload
		for {
			m, err := ipc.ReadMessage(conn)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if m.ID != id {
				continue
			}
			if err := json.Unmarshal(m.Payload, &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			break
		}
		for i := range out.Quil {
			if out.Quil[i].Role == "daemon" {
				row := out.Quil[i]
				if row.RSSBytes > 0 {
					own = &row
				}
				break
			}
		}
		if own == nil {
			time.Sleep(300 * time.Millisecond)
		}
	}

	if own == nil {
		t.Fatal("the daemon's own row never carried an RSS reading — " +
			"handleResourceReportReq is not consulting procReport.SelfStat()")
	}
	if own.PID != os.Getpid() {
		t.Errorf("daemon row PID = %d, want this process %d", own.PID, os.Getpid())
	}
	if own.CPUPct == 0 {
		t.Error("daemon row CPUPct is exactly 0 — the unknown marker is negative, " +
			"and a literal zero renders as \"0%\" claiming the daemon is idle")
	}
}

// A client that says hello but never reports must come back as unknown rather
// than as a confident zero — the honesty rule, verified across the wire rather
// than against the registry directly.
func TestDaemon_ClientWithoutStatReportsUnknownOverTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	cfg := config.Default()
	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { d.Stop() })

	sockPath := filepath.Join(tmp, "quild.sock")
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("socket %s never became connectable", sockPath)
	}
	defer conn.Close()

	self := os.Getpid()
	hello, _ := json.Marshal(ipc.ClientHelloPayload{
		Role: "bridge", PID: self, Version: "test-version", ExeName: "quil-test",
	})
	if err := ipc.WriteMessage(conn, &ipc.Message{Type: ipc.MsgClientHello, Payload: hello}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	reqPayload, _ := json.Marshal(ipc.ResourceReportReqPayload{WithTrees: true})
	if err := ipc.WriteMessage(conn, &ipc.Message{
		Type: ipc.MsgResourceReportReq, ID: "stat2", Payload: reqPayload,
	}); err != nil {
		t.Fatalf("write req: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var out ipc.ResourceReportRespPayload
	for {
		m, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if m.ID != "stat2" {
			continue
		}
		if err := json.Unmarshal(m.Payload, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		break
	}

	for i := range out.Quil {
		if out.Quil[i].PID == self && out.Quil[i].Role == "bridge" {
			if out.Quil[i].CPUPct >= 0 {
				t.Errorf("CPUPct = %v for a client that never reported; want a "+
					"negative unknown marker, since 0 renders as \"0%%\"",
					out.Quil[i].CPUPct)
			}
			return
		}
	}
	t.Fatalf("no bridge row for pid %d", self)
}
