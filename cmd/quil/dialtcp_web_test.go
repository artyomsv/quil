package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// A browser tab logs in as kind "web" under its own leased id, so the
// daemon's client list shows it as a browser from the first frame instead of
// as a TUI with the gateway process's id.
func TestWebHelloPayload_KindAndClientID(t *testing.T) {
	p := webHelloPayload("web-abc-123")
	if p.Kind != "web" {
		t.Fatalf("kind = %q, want web", p.Kind)
	}
	if p.ClientID != "web-abc-123" {
		t.Fatalf("client id = %q", p.ClientID)
	}
	if p.Proto != ipc.ProtocolVersion {
		t.Fatalf("proto = %d", p.Proto)
	}
}

// dialTCPWith puts the hello it was given on the wire, not the TUI's.
func TestDialTCPWith_SendsTheGivenHello(t *testing.T) {
	tok := mustNewToken(t)
	seen := make(chan ipc.HelloPayload, 1)
	addr, _ := fakeListener(t, func(c net.Conn) error {
		hello, err := ipc.ReadMessage(c)
		if err != nil {
			return err
		}
		var hp ipc.HelloPayload
		_ = hello.DecodePayload(&hp)
		seen <- hp
		return nil // closing makes the login fail; only the hello matters here
	})
	_, _, _ = dialTCPWith(context.Background(), addr, tok, webHelloPayload("web-x-1"))
	select {
	case got := <-seen:
		if got.Kind != "web" || got.ClientID != "web-x-1" {
			t.Fatalf("login hello kind=%q client_id=%q, want web / web-x-1", got.Kind, got.ClientID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never received a login hello")
	}
}
