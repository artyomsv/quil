package daemon

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/webgw"
)

// Lifecycle of browser tabs against a real daemon: resync, revoke, master
// eligibility and the gateway's own shutdown.

// A token revoked while its tab is open: the daemon refuses, then closes the
// connection, and the page reads 4004 with the daemon's reason, not 4003.
func TestWeb_TokenRevokedMidSessionClosesWith4004(t *testing.T) {
	h := webHarness(t)
	tok := h.mint(t, "web-tab", clientauth.LevelFull, nil)
	r := newWebRigWith(t, tokenWebDial(h, tok))
	h.d.session.CreateTab("T")
	w := r.open(t)
	w.attach()

	if _, n, err := h.d.revokeToken("web-tab"); err != nil || n != 1 {
		t.Fatalf("revoke closed %d conns (err %v), want 1", n, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code, reason := w.closeOf(ctx); code != webgw.CloseTokenRefused || reason != "token revoked" {
		t.Fatalf("the page closed %d %q, want 4004 \"token revoked\"", code, reason)
	}
}

// After a 4001 the page comes back on the same daemon connection: the tab
// keeps the size master slot, gets the replay again, and the daemon holds one
// record for it, not two.
func TestWeb_ResyncReattachKeepsMasterAndReplays(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	pane := newTerminalPane(t, h)
	tui := attachTUIAs(t, h.sock, "TUI", 120, 40, false)
	drainClient(tui)
	waitUntil(t, "the TUI is master", func() bool { return h.d.masterID() == "TUI" })

	w := r.open(t)
	w.attach()
	w.send(ipc.MsgTakeControl, "", struct{}{})
	waitUntil(t, "the web tab is master", func() bool { return h.d.masterID() == w.id })

	if code := floodUntilClosed(t, h, pane.ID, w); code != webgw.CloseResync {
		t.Fatalf("the page closed with %d, want 4001", code)
	}

	w2 := r.openAs(t, w.id)
	if w2.id != w.id {
		t.Fatalf("came back as %s, want %s", w2.id, w.id)
	}
	var replayed bool
	for _, f := range w2.attach() {
		if f.binary && f.ghost && f.pane == pane.ID {
			replayed = true
		}
	}
	if !replayed {
		t.Fatal("no replay after the re-attach")
	}
	if id := h.d.masterID(); id != w.id {
		t.Fatalf("master is %q after the re-attach, want %s", id, w.id)
	}

	w2.send(ipc.MsgListClientsReq, "lc", struct{}{})
	frames := w2.until("list_clients_resp", func(f webFrame) bool {
		return f.msg != nil && f.msg.Type == ipc.MsgListClientsResp && f.msg.ID == "lc"
	})
	var list ipc.ListClientsRespPayload
	if err := frames[len(frames)-1].msg.DecodePayload(&list); err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, c := range list.Clients {
		if c.Client == w.id {
			mine++
		}
	}
	if mine != 1 || len(list.Clients) != 2 {
		t.Fatalf("list_clients = %+v, want the TUI and one record for %s", list.Clients, w.id)
	}
}

// One tab resyncing does not stop another: the second tab keeps receiving
// live output throughout and after.
func TestWeb_OtherTabKeepsStreamingWhileOneResyncs(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	pane := newTerminalPane(t, h)
	slow := r.open(t)
	slow.attach()
	fast := r.open(t)
	fast.attach()

	// The slow tab reads but never acknowledges.
	slowCode := make(chan int, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code, _, err := slow.readToClose(ctx)
		if err != nil {
			code = -1
		}
		slowCode <- code
	}()

	// The fast tab acknowledges every frame, and the next chunk is printed
	// only once it has the previous one, so it is never more than a chunk
	// behind.
	readMarker := func(marker []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			f, err := fast.next(ctx)
			if err != nil {
				t.Fatalf("the fast tab, waiting for %q: %v", marker, err)
			}
			fast.ack(f)
			if f.binary && !f.ghost && bytes.HasSuffix(f.data, marker) {
				return
			}
		}
	}
	chunk := bytes.Repeat([]byte("y"), 32<<10)
	code := 0
	for i := 0; code == 0; i++ {
		if i == 400 {
			t.Fatal("the slow tab was never resynced")
		}
		marker := []byte(fmt.Sprintf("|%d|", i))
		h.d.flushPaneOutput(pane.ID, append(append([]byte(nil), chunk...), marker...))
		readMarker(marker)
		select {
		case code = <-slowCode:
		default:
		}
	}
	if code != webgw.CloseResync {
		t.Fatalf("the slow tab closed with %d, want 4001", code)
	}
	h.d.flushPaneOutput(pane.ID, []byte("|after|"))
	readMarker([]byte("|after|"))
}

// The window size in the attach decides eligibility for size master; no
// client_geometry is needed first.
func TestWeb_AttachWindowMakesTheTabEligible(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	h.d.session.CreateTab("T")

	small := r.open(t)
	small.attachSized(30, 8)
	if id := h.d.masterID(); id != "" {
		t.Fatalf("a 30x8 tab made %q master, want none", id)
	}
	big := r.open(t)
	big.attachSized(120, 40)
	if id := h.d.masterID(); id != big.id {
		t.Fatalf("master is %q after a 120x40 attach, want %s", id, big.id)
	}
}

// Ctrl+C: every tab is detached before its connection closes, so no slot is
// kept for a tab that is gone, and every page reads 1001.
func TestWeb_ShutdownDetachesEveryTab(t *testing.T) {
	h := webHarness(t)
	r := newWebRig(t, h)
	h.d.session.CreateTab("T")
	tabs := []*webTab{r.open(t), r.open(t)}
	codes := make([]chan int, len(tabs))
	for i, w := range tabs {
		w.attach()
		codes[i] = make(chan int, 1)
	}
	waitUntil(t, "both tabs attached", func() bool { return h.d.clientCount() == 2 })
	if h.d.masterID() != tabs[0].id {
		t.Fatalf("master is %q, want the first tab", h.d.masterID())
	}

	// The pages read while the gateway stops, as a browser does.
	for i, w := range tabs {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			code, _, err := w.readToClose(ctx)
			if err != nil {
				code = -1
			}
			codes[i] <- code
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.srv.Shutdown(ctx)

	waitUntil(t, "every tab detached", func() bool { return h.d.clientCount() == 0 })
	if id := h.d.masterID(); id != "" {
		t.Fatalf("the slot is kept for %q after the gateway stopped, want none", id)
	}
	for i := range codes {
		if code := <-codes[i]; code != webgw.CloseGoingAway {
			t.Fatalf("page %d closed with %d, want 1001", i, code)
		}
	}
}
