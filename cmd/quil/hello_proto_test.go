package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// captureClientFrames runs fn against a real client dialled at a real
// ipc.Server, and returns every frame the server received, in order.
//
// A real server rather than a fake sender, the same reasoning
// TestMCPBridge_DeclinePaneOutputSendsSubscribeOptOut and paneInputBridge use:
// the thing worth pinning here is what actually leaves the client on the
// wire, including the ID field a fake recorder could omit without failing.
func captureClientFrames(t *testing.T, fn func(c *ipc.Client)) []*ipc.Message {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "hello.sock")

	got := make(chan *ipc.Message, 8)
	srv := ipc.NewServer(sockPath, func(_ *ipc.Conn, m *ipc.Message) {
		got <- m
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	client, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	fn(client)

	var frames []*ipc.Message
	deadline := time.After(3 * time.Second)
	for len(frames) < 2 {
		select {
		case m := <-got:
			frames = append(frames, m)
		case <-deadline:
			return frames
		}
	}
	return frames
}

func typesOf(frames []*ipc.Message) []string {
	types := make([]string, len(frames))
	for i, f := range frames {
		types[i] = f.Type
	}
	return types
}

// sendClientHello must send hello BEFORE client_hello, with an ID — the
// daemon ignores an id-less hello (see internal/ipc/hello.go), and hello has
// to register this conn as protocol-1 before anything else rides the link.
func TestSendClientHello_SendsHelloWithIDThenClientHello(t *testing.T) {
	frames := captureClientFrames(t, func(c *ipc.Client) { sendClientHello(c, helloRoleBridge) })
	if len(frames) < 2 || frames[0].Type != ipc.MsgHello || frames[1].Type != ipc.MsgClientHello {
		t.Fatalf("frames = %v; want hello then client_hello", typesOf(frames))
	}
	if frames[0].ID == "" {
		t.Error("hello has no id; the daemon ignores an id-less hello")
	}
	var p ipc.HelloPayload
	if err := frames[0].DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.Kind != helloRoleBridge || p.Proto != ipc.ProtocolVersion || p.ClientID != processClientID {
		t.Errorf("hello = %+v", p)
	}
}
