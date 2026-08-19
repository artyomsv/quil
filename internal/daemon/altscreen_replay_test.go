//go:build integration

package daemon

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Issue #172: claude-code ships ghost_buffer = true, measured when it wrote to
// the MAIN screen. Its fullscreen renderer — the default for installs first run
// on or after 2026-05-06 — draws on the alternate screen and transmits only the
// cells that changed between frames, so any suffix of its stream is a few
// deltas against a blank screen. Measured 2026-08-19 through the client
// emulator: an arbitrary cut of a real fullscreen buffer paints torn escape
// sequences as literal text (`[H`, `6;101m…`) with 25 of 30 rows blank.
//
// The plugin default stays; what the child is DOING overrides it. The
// main-screen pane in the same attach is the control: a shell reprints none of
// its scrollback, so dropping ITS replay would trade one silent loss for
// another.
func TestHandleAttach_SkipsReplayForAPaneOnTheAlternateScreen(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	mkPane := func(typ string, alt bool) *Pane {
		t.Helper()
		pane, err := d.session.CreatePane(tab.ID, "/tmp")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		pane.PluginMu.Lock()
		pane.Type = typ
		pane.MouseModes.altScreen = alt
		pane.PluginMu.Unlock()
		// OutputBuf, not GhostSnap: this is the reconnect path, where the child
		// is alive and the replay is the only history there is. The ghostsnap
		// path is already skipped for claude-code by restoresOwnHistory, so it
		// is not the one this fix is about.
		pane.OutputBuf.Write(bytes.Repeat([]byte{'g'}, 4096))
		return pane
	}

	// A pane carrying BOTH — a restore snapshot and an alt-screen child. Its
	// snapshot must be consumed by this attach even though nothing is replayed:
	// left behind, it would be replayed by a LATER attach once the child leaves
	// the alternate screen, painting a previous daemon session's screen into a
	// live pane. Reachable without claude-code at all: attach while `vim` is
	// up, quit vim, attach again.
	snapshotted := mkPane("terminal", true)
	snapshotted.PluginMu.Lock()
	snapshotted.GhostSnap = bytes.Repeat([]byte{'s'}, 2048)
	snapshotted.PluginMu.Unlock()
	fullscreen := mkPane("claude-code", true)
	shell := mkPane("terminal", false)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Guard: both plugins must resolve WITH ghost_buffer = true, or this test
	// proves nothing about an override — it would just be watching an opt-out.
	for _, typ := range []string{"claude-code", "terminal"} {
		p := d.registry.Get(typ)
		if p == nil || !p.Persistence.GhostBuffer {
			t.Fatalf("setup: %s must resolve with ghost_buffer = true", typ)
		}
	}

	conn := dialDaemon(t, filepath.Join(tmp, "quild.sock"))
	defer conn.Close()

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage attach: %v", err)
	}
	if err := ipc.WriteMessage(conn, attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}

	// Read until the shell's replay has fully arrived. The alt-screen pane's
	// frames, if the daemon wrongly sent any, precede it in the same pane loop.
	ghost := map[string]int{}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for ghost[shell.ID] < 4096 {
		msg, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read after %v: %v", ghost, err)
		}
		if msg.Type != ipc.MsgPaneOutput {
			continue
		}
		var p ipc.PaneOutputPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane output: %v", err)
		}
		if p.Ghost {
			ghost[p.PaneID] += len(p.Data)
		}
	}

	if n := ghost[fullscreen.ID]; n != 0 {
		t.Errorf("alt-screen pane received %d ghost bytes; that buffer holds frame "+
			"deltas aimed at a screen the replay does not own, so it paints a torn "+
			"frame that nothing then repairs", n)
	}
	if n := ghost[shell.ID]; n != 4096 {
		t.Errorf("main-screen pane received %d ghost bytes, want 4096 — a shell "+
			"reprints none of its scrollback, so this replay is its only history", n)
	}
	if n := ghost[snapshotted.ID]; n != 0 {
		t.Errorf("alt-screen pane with a restore snapshot received %d ghost bytes, want 0", n)
	}

	snapshotted.PluginMu.Lock()
	leftover := snapshotted.GhostSnap
	snapshotted.PluginMu.Unlock()
	if leftover != nil {
		t.Errorf("GhostSnap survived an attach that skipped the replay (%d bytes); a "+
			"later attach would paint a previous session's screen into this live pane",
			len(leftover))
	}
}
