package ipc

import (
	"net"
	"testing"
)

// Broadcast fans every frame to every connection with no notion of who wants
// what. That includes MsgPaneOutput — the live PTY stream of every pane — and
// an MCP bridge's read loop decodes each one only to discard it
// (`if msg.ID == "" { continue }`).
//
// Measured 2026-08-18 that cost little, because the workspace was producing
// ~1.5 KB/s. It is a standing amplifier rather than a current problem: 17
// bridges were attached, so one pane running a verbose build multiplies its
// output by 18 socket writes and 17 processes' worth of frame decoding.
//
// The default is SUBSCRIBED, so a client that says nothing — every existing
// TUI, and any older build — is unaffected.

func TestConn_ReceivesPaneOutputByDefault(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	c := newConn(local)
	if !c.wantsPaneOutput() {
		t.Error("a conn that has not opted out must receive pane output — silence " +
			"from an older client must not cost it the live stream")
	}
}

func TestConn_OptOutStopsPaneOutputOnly(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	c := newConn(local)
	c.setPaneOutputWanted(false)

	if c.wantsPaneOutput() {
		t.Fatal("opt-out did not take effect")
	}

	// Must-deliver traffic is unaffected: a bridge still needs workspace state,
	// its own request responses, and lifecycle frames.
	if !c.wantsFrame(MsgWorkspaceState) {
		t.Error("opting out of pane output must not silence workspace state")
	}
	if !c.wantsFrame(MsgShutdown) {
		t.Error("opting out of pane output must not silence lifecycle frames")
	}
	if c.wantsFrame(MsgPaneOutput) {
		t.Error("pane output must be filtered for a conn that opted out")
	}
}

func TestConn_SubscribedConnWantsEverything(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	c := newConn(local)
	for _, typ := range []string{MsgPaneOutput, MsgWorkspaceState, MsgShutdown} {
		if !c.wantsFrame(typ) {
			t.Errorf("subscribed conn must want %q", typ)
		}
	}
}
