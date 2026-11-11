package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
)

// terminalQueries are replies the emulator writes into its input pipe: DA1,
// DSR cursor report, DECRQM, kitty keyboard query, XTVERSION. Claude Code
// sends several of them on every start, so a pane's ring buffer holds them.
const terminalQueries = "\x1b[c\x1b[6n\x1b[?2026$p\x1b[?u\x1b[>0q"

// A buffer holding terminal queries renders instead of blocking. Before the
// drain, em.Write parked on the first query's reply and never returned.
func TestRenderScreenshot_TerminalQueriesDoNotBlock(t *testing.T) {
	type shot struct {
		text string
		x, y int
	}
	got := make(chan shot, 1)
	go func() {
		text, x, y := renderScreenshot([]byte("hello"+terminalQueries+"\r\nworld"), 40, 5)
		got <- shot{text, x, y}
	}()
	select {
	case s := <-got:
		if s.text != "hello\nworld" {
			t.Errorf("text = %q, want %q", s.text, "hello\nworld")
		}
		if s.x != 5 || s.y != 1 {
			t.Errorf("cursor = (%d,%d), want (5,1)", s.x, s.y)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renderScreenshot blocked on a terminal query")
	}
}

// Many replies, more than any pipe or channel buffer would absorb.
func TestRenderScreenshot_ManyQueriesDoNotBlock(t *testing.T) {
	raw := []byte{}
	for i := 0; i < 2000; i++ {
		raw = append(raw, terminalQueries...)
	}
	raw = append(raw, "done"...)
	got := make(chan string, 1)
	go func() {
		text, _, _ := renderScreenshot(raw, 20, 3)
		got <- text
	}()
	select {
	case text := <-got:
		if text != "done" {
			t.Errorf("text = %q, want %q", text, "done")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("renderScreenshot blocked on a run of terminal queries")
	}
}

// Through the real dispatch path: the screenshot of a pane whose buffer holds
// a query is answered, and the SAME connection keeps being served. The
// production failure was the second half — every later request from that
// bridge timed out, because its dispatch goroutine was parked in em.Write.
func TestScreenshotPane_QueryInBufferKeepsTheConnServing(t *testing.T) {
	h := newAuthHarness(t)
	tab := h.d.session.CreateTab("shot")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.PluginMu.Lock()
	pane.Type = "terminal"
	pane.PluginMu.Unlock()
	pane.OutputBuf.Write([]byte("prompt>" + terminalQueries))

	full, _ := h.login(t, h.mint(t, "agent", clientauth.LevelFull, nil))
	shot := decodeInto[ipc.ScreenshotPaneRespPayload](t, roundTrip(t, full, ipc.MsgScreenshotPaneReq,
		ipc.MsgScreenshotPaneResp, ipc.ScreenshotPaneReqPayload{PaneID: pane.ID, Width: 40, Height: 5}))
	if shot.Text != "prompt>" {
		t.Errorf("screenshot text = %q, want %q", shot.Text, "prompt>")
	}
	status := decodeInto[ipc.PaneStatusRespPayload](t, roundTrip(t, full, ipc.MsgPaneStatusReq,
		ipc.MsgPaneStatusResp, ipc.PaneStatusReqPayload{PaneID: pane.ID}))
	if status.PaneID != pane.ID {
		t.Errorf("pane status after the screenshot = %+v, want pane %s", status, pane.ID)
	}
}
