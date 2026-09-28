package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// mcpTestDaemon is a daemon with a LIVE IPC server, live fake PTYs and the
// claude-code plugin registered — the shape every MCP request/response test
// needs, because respondTo answers nothing without a conn.
func mcpTestDaemon(t *testing.T) (*Daemon, *ipc.Client) {
	t.Helper()
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	registerShippedPlugins(t, d)
	client, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	// Match the MCP bridge's connection: attaching as a TUI can create a
	// default shell tab concurrently with the test's workspace setup.
	sendNoID(t, client, ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "bridge", PID: os.Getpid()})
	return d, client
}

// registerShippedPlugins loads the SHIPPED plugin TOMLs — the ones with the
// real toggle declarations — rather than registerClaudePlugin's minimal
// stand-in, because the create path resolves toggles by the names those files
// declare.
func registerShippedPlugins(t *testing.T, d *Daemon) {
	t.Helper()
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("write default plugins: %v", err)
	}
	if err := d.registry.LoadFromDir(dir); err != nil {
		t.Fatalf("load default plugins: %v", err)
	}
}

// roundTrip sends an ID-bearing request and returns the first response of
// respType carrying the same ID, skipping broadcasts.
//
// The read deadline is what actually bounds this: Receive blocks with no
// timeout of its own, so a bare time.After checked only BETWEEN successful
// reads never interrupts one that never arrives — exactly the failure this
// helper exists to catch. SetReadDeadline unparks a Receive already in
// progress, which is why it is set before the loop rather than raced against
// it.
func roundTrip(t *testing.T, client *ipc.Client, msgType, respType string, payload any) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(msgType, payload)
	if err != nil {
		t.Fatalf("NewMessage(%s): %v", msgType, err)
	}
	msg.ID = "rt-" + msgType + "-" + time.Now().Format("150405.000000")
	if err := client.Send(msg); err != nil {
		t.Fatalf("send %s: %v", msgType, err)
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		resp, err := client.Receive()
		if err != nil {
			t.Fatalf("no %s arrived for %s: %v", respType, msgType, err)
		}
		if resp.Type == respType && resp.ID == msg.ID {
			return resp
		}
	}
}

func decodeInto[T any](t *testing.T, msg *ipc.Message) T {
	t.Helper()
	var out T
	if err := msg.DecodePayload(&out); err != nil {
		t.Fatalf("decode %s: %v", msg.Type, err)
	}
	return out
}

// sendNoID sends a fire-and-forget message, then proves it produced no answer
// by round-tripping an unrelated request: one conn's messages dispatch in
// order, so the second answer arriving means the first was handled silently.
func sendNoID(t *testing.T, client *ipc.Client, msgType string, payload any) {
	t.Helper()
	msg, err := ipc.NewMessage(msgType, payload)
	if err != nil {
		t.Fatalf("NewMessage(%s): %v", msgType, err)
	}
	if err := client.Send(msg); err != nil {
		t.Fatalf("send %s: %v", msgType, err)
	}
}
