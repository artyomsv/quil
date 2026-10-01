package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func stubTokenRequest(t *testing.T, fn func(msgType string, payload any) (*ipc.Message, error)) {
	t.Helper()
	prev := tokenRequestFn
	tokenRequestFn = fn
	t.Cleanup(func() { tokenRequestFn = prev })
}

func reply(t *testing.T, typ string, payload any) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The token goes to stdout exactly once, and nowhere else: not stderr, and
// not the log (which becomes quil.log once a logger is installed).
func TestTokenCreate_PrintsTokenOnce(t *testing.T) {
	const tok = "qtk_0a1b2c3d_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	var logged bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prevLog) })
	var sent ipc.TokenCreateReqPayload
	stubTokenRequest(t, func(typ string, p any) (*ipc.Message, error) {
		sent = p.(ipc.TokenCreateReqPayload)
		return reply(t, ipc.MsgTokenCreateResp, ipc.TokenCreateRespPayload{Token: tok, ID: "0a1b2c3d", Name: "laptop", Rights: "read-only", Expires: "2026-12-30T00:00:00Z"}), nil
	})
	var out, errOut bytes.Buffer
	if code := runTokenCreate([]string{"--name", "laptop", "--rights", "read-only", "--expires", "90d"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Count(out.String(), tok) != 1 {
		t.Fatalf("token printed %d times:\n%s", strings.Count(out.String(), tok), out.String())
	}
	if strings.Contains(errOut.String(), tok) || strings.Contains(logged.String(), tok) {
		t.Fatalf("token leaked to stderr or the log:\nstderr: %s\nlog: %s", errOut.String(), logged.String())
	}
	if sent.Name != "laptop" || sent.Rights != "read-only" || sent.Expires != "90d" {
		t.Fatalf("request = %+v", sent)
	}
}

func TestTokenCreate_BadFlagsRefusedBeforeDial(t *testing.T) {
	stubTokenRequest(t, func(string, any) (*ipc.Message, error) {
		t.Fatal("dialled the daemon for an invalid request")
		return nil, nil
	})
	for _, args := range [][]string{
		{"--name", "x", "--expires", "5"},
		{"--name", "x", "--rights", "admin"},
		{"--name", ""},
	} {
		var out, errOut bytes.Buffer
		if code := runTokenCreate(args, &out, &errOut); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestTokenList_NeverPrintsSecrets(t *testing.T) {
	stubTokenRequest(t, func(string, any) (*ipc.Message, error) {
		return reply(t, ipc.MsgTokenListResp, ipc.TokenListRespPayload{Tokens: []ipc.TokenInfo{
			{ID: "0a1b2c3d", Name: "laptop", Rights: "standard", Created: "2026-10-01T10:00:00Z"},
		}}), nil
	})
	var out, errOut bytes.Buffer
	if code := runTokenList(nil, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "0a1b2c3d") || !strings.Contains(out.String(), "never") {
		t.Fatalf("list output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "qtk_") {
		t.Fatal("list printed a token")
	}
}

// The REAL sendTokenRequest and ReceiveByID against a listener that accepts,
// reads, and never answers — what an older daemon does for an unknown type on
// a conn that never said hello. Only the dial (tokenDialFn) and the timeout
// are swapped; a stubbed sender could not fail this.
func TestTokenRequest_OldDaemonTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(io.Discard, c) }() // silent
		}
	}()
	prevDial, prevTimeout := tokenDialFn, tokenRequestTimeout
	tokenDialFn = func() (*ipc.Client, error) {
		return ipc.NewClientWithDialer(context.Background(), func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", ln.Addr().String())
		})
	}
	tokenRequestTimeout = 200 * time.Millisecond
	t.Cleanup(func() { tokenDialFn, tokenRequestTimeout = prevDial, prevTimeout })

	var out, errOut bytes.Buffer
	start := time.Now()
	if code := runTokenRevoke([]string{"laptop"}, &out, &errOut); code == 0 {
		t.Fatal("revoke succeeded without an answer")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("waited %v for a silent daemon; the timeout is not applied", elapsed)
	}
	if !strings.Contains(errOut.String(), "did not answer") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestClients_RefusedUnderRemote(t *testing.T) {
	prev, prevExit := remoteDest, exitFn
	remoteDest = "tcp:127.0.0.1:7878"
	code := -1
	exitFn = func(c int) { code = c; panic("exit") }
	t.Cleanup(func() { remoteDest, exitFn = prev, prevExit })
	stubTokenRequest(t, func(string, any) (*ipc.Message, error) {
		t.Fatal("asked a daemon for a token request under --remote")
		return nil, nil
	})
	defer func() {
		_ = recover()
		if code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
	}()
	handleClientsArgs([]string{"quil", "clients", "token", "list"})
}
