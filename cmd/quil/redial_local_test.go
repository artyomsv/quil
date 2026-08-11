package main

import (
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// The local daemon reconnects like any other destination. It did not use to:
// redialFns had no entry for "", so canReconnect answered false and a lost
// local link was fatal — on the documented grounds that "its panes died with
// it". That is true of a dead daemon and false of a dropped SOCKET, which is
// what the 2026-08-11 incident actually was: the daemon was healthy, its
// snapshot loop never missed a tick, and the client sat behind "the local
// daemon is gone — its panes are lost" with ctrl+q as the only way out.

// localDaemonAt starts a minimal daemon on the session's socket: enough to
// answer the link probe, which is all a redial verifies.
func localDaemonAt(t *testing.T) *ipc.Server {
	t.Helper()
	srv := ipc.NewServer(config.SocketPath(), func(conn *ipc.Conn, msg *ipc.Message) {
		if msg.Type != ipc.MsgVersionReq {
			return
		}
		resp, err := ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: versionpkg.Current()})
		if err != nil {
			return
		}
		resp.ID = msg.ID
		conn.Send(resp)
	}, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("start local daemon: %v", err)
	}
	return srv
}

// TestRedialLocal_ReconnectsWhileTheDaemonIsStillListening is the incident in
// one assertion: the link dropped, the daemon is fine, so the redial must hand
// back a live connection rather than reporting the session over.
func TestRedialLocal_ReconnectsWhileTheDaemonIsStillListening(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	srv := localDaemonAt(t)
	defer srv.Stop()

	client, err := redialLocal()(nil)
	if err != nil {
		t.Fatalf("redial against a listening daemon failed: %v", err)
	}
	if client == nil {
		t.Fatal("redial reported success with no connection")
	}
	if c, ok := client.(*ipc.Client); ok {
		c.Close()
	}
}

// TestRedialLocal_ParksOnlyAfterRepeatedFailures pins BOTH halves of the park
// policy, and neither is arbitrary.
//
// Parking eventually is what keeps a genuinely dead daemon from trapping the
// user in a frozen client that retries forever: every attempt is a dial against
// a socket nobody is serving, and freezeInput drops input for as long as the
// ladder climbs. Not parking IMMEDIATELY is what covers `quil restart` and an
// update-restart, where the socket is legitimately absent for a second or two —
// the daemon comes back and the client reattaches with nothing on screen but a
// brief banner.
func TestRedialLocal_ParksOnlyAfterRepeatedFailures(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir()) // nothing is listening in here

	redial := redialLocal()
	for i := 1; i < localRedialAttempts; i++ {
		_, err := redial(nil)
		if err == nil {
			t.Fatalf("attempt %d reported success with no daemon listening", i)
		}
		if errors.Is(err, tui.ErrLinkPermanent) {
			t.Fatalf("attempt %d parked the ladder; a restarting daemon needs %d attempts of grace",
				i, localRedialAttempts)
		}
	}

	_, err := redial(nil)
	if !errors.Is(err, tui.ErrLinkPermanent) {
		t.Errorf("attempt %d returned %v, want a permanent failure so the ladder parks instead of "+
			"retrying a daemon that is not coming back", localRedialAttempts, err)
	}
}

// TestRedialLocal_ResetsItsFailureCountOnSuccess stops a reconnect that
// succeeded from carrying old failures into the next outage — otherwise a
// client that has flapped a few times over a long session parks on the first
// blip of the next one, having spent its grace hours earlier.
func TestRedialLocal_ResetsItsFailureCountOnSuccess(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	redial := redialLocal()

	for i := 1; i < localRedialAttempts; i++ {
		if _, err := redial(nil); errors.Is(err, tui.ErrLinkPermanent) {
			t.Fatalf("attempt %d parked early", i)
		}
	}

	srv := localDaemonAt(t)
	client, err := redial(nil)
	if err != nil {
		t.Fatalf("redial against a restarted daemon failed: %v", err)
	}
	if c, ok := client.(*ipc.Client); ok {
		c.Close()
	}
	srv.Stop()

	for i := 1; i < localRedialAttempts; i++ {
		if _, err := redial(nil); errors.Is(err, tui.ErrLinkPermanent) {
			t.Fatalf("attempt %d after a successful reconnect parked early — the count did not reset", i)
		}
	}
}

// TestRedialLocal_ReleasesTheDeadClient mirrors redialRemote: the TUI's Client
// is Send/Receive by design, so this layer is the only one that can close the
// connection it is replacing. Leaking it leaks a socket per reconnect.
func TestRedialLocal_ReleasesTheDeadClient(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	var order []string
	dead := clientRecordingClose(t, &order)

	if _, err := redialLocal()(dead); err == nil {
		t.Fatal("redial reported success with no daemon listening")
	}
	if len(order) == 0 || order[0] != "close" {
		t.Errorf("order = %v, want the dead client closed first", order)
	}
}

// TestRedialFor_LocalDestinationGetsTheLocalDialer tests the DISPATCH, not the
// dialer: a redialLocal that works but is never installed for "" leaves the
// incident exactly where it was, and a unit test of the function alone cannot
// tell the difference. Asserting behaviour rather than identity because a
// tui.RedialFunc is not comparable.
func TestRedialFor_LocalDestinationGetsTheLocalDialer(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	srv := localDaemonAt(t)
	defer srv.Stop()

	cfg := config.Config{}
	client, err := redialFor("", func() *config.Config { return &cfg })(nil)
	if err != nil {
		t.Fatalf("redialFor(\"\") failed against a listening local daemon: %v — "+
			"the local destination is being dialled over ssh", err)
	}
	if c, ok := client.(*ipc.Client); ok {
		c.Close()
	}
}
