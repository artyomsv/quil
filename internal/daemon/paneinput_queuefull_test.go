package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// The browser's paste flow tells a full queue (wait and resend) from every
// other refusal (stop) by this text. web/src/lib/paste.ts keeps the prefix
// "pane input queue is full"; the two must not drift.
func TestPaneInputQueueFull_TextIsTheBrowsersPrefix(t *testing.T) {
	if !strings.HasPrefix(ipc.PaneInputQueueFull, "pane input queue is full") {
		t.Fatalf("PaneInputQueueFull = %q; web/src/lib/paste.ts matches on the prefix %q", ipc.PaneInputQueueFull, "pane input queue is full")
	}
}

// A full queue answers with exactly that text.
func TestPaneInputOutcome_FullQueueUsesTheConstant(t *testing.T) {
	d := newTestDaemon(t)
	p := wedgedInputPane(t, d)
	var out ipc.PaneInputRespPayload
	for i := 0; i < inputQueueSize+8; i++ {
		out = d.paneInputOutcome(ipc.PaneInputPayload{PaneID: p.ID, Data: []byte("x")})
		if !out.Delivered {
			break
		}
	}
	if out.Delivered || out.Error != ipc.PaneInputQueueFull {
		t.Fatalf("full queue answered %+v, want error %q", out, ipc.PaneInputQueueFull)
	}
}

// wedgedInputPane publishes a pane whose PTY never accepts a write, so its
// writer goroutine parks on the first item and the input queue fills.
func wedgedInputPane(t *testing.T, d *Daemon) *Pane {
	t.Helper()
	tab := d.session.CreateTab("wedge")
	p, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := newWedgedSession()
	p.PluginMu.Lock()
	p.PTY = w
	p.PluginMu.Unlock()
	t.Cleanup(func() {
		close(w.release)
		p.StopInput()
	})
	return p
}
