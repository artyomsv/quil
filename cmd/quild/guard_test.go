package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestDaemonAlreadyHealthy_RespondsToVersionReq: a peer that answers the
// MsgVersionReq handshake is recognised as a live daemon.
func TestDaemonAlreadyHealthy_RespondsToVersionReq(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock")
	srv := ipc.NewServer(sock, func(conn *ipc.Conn, msg *ipc.Message) {
		if msg.Type == ipc.MsgVersionReq {
			resp, err := ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: "9.9.9"})
			if err != nil {
				return
			}
			resp.ID = msg.ID
			_ = conn.Send(resp)
		}
	}, func(*ipc.Conn) {})
	if err := srv.Start(); err != nil {
		t.Fatalf("start fake daemon: %v", err)
	}
	defer srv.Stop()

	if !daemonAlreadyHealthyWithin(sock, 2*time.Second) {
		t.Fatal("expected a version-responding peer to be treated as a healthy daemon")
	}
}

// TestDaemonAlreadyHealthy_WedgedAcceptsButNeverAnswers: a peer that accepts
// the connection but never answers the handshake (the wedged-daemon / foreign
// squatter case) must NOT be treated as a healthy daemon — otherwise the guard
// would refuse a legitimate startup. Must return false within ~timeout.
func TestDaemonAlreadyHealthy_WedgedAcceptsButNeverAnswers(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock")
	srv := ipc.NewServer(sock, func(conn *ipc.Conn, msg *ipc.Message) {
		// Deliberately never respond — simulate a wedged daemon.
	}, func(*ipc.Conn) {})
	if err := srv.Start(); err != nil {
		t.Fatalf("start wedged fake daemon: %v", err)
	}
	defer srv.Stop()

	timeout := 200 * time.Millisecond
	start := time.Now()
	if daemonAlreadyHealthyWithin(sock, timeout) {
		t.Fatal("a wedged peer must not count as a healthy daemon")
	}
	if elapsed := time.Since(start); elapsed < timeout {
		t.Errorf("returned before the handshake deadline: %v < %v", elapsed, timeout)
	}
}

// TestDaemonAlreadyHealthy_NothingListening: with no socket, the dial fails and
// the guard reports no daemon (so the caller proceeds to start one).
func TestDaemonAlreadyHealthy_NothingListening(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "quild.sock")
	if daemonAlreadyHealthyWithin(sock, 2*time.Second) {
		t.Fatal("expected no healthy daemon when nothing is listening")
	}
}
