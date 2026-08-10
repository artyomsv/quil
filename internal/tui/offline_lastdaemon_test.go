package tui

import "testing"

// knownDests is the router's CONN table, and an offline destination has no conn
// until finishReconnect adds one. So a local daemon dying while the only remote
// is offline made lastDaemon iterate [""] alone, answer true, and quit the
// client — killing the ladder and the repair panel mid-repair.
func TestLastDaemon_OfflineRemoteKeepsTheSessionAlive(t *testing.T) {
	m := Model{client: NewRouter(map[string]Client{"": newFakeConn()})}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", nil)
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, nil })

	if m.lastDaemon("") {
		t.Error("quitting on the local daemon while an offline remote is still laddering")
	}
}

// An offline destination with no dialer cannot come back on its own, so it does
// not hold the session open.
func TestLastDaemon_OfflineRemoteWithNoDialerDoesNotHold(t *testing.T) {
	m := Model{client: NewRouter(map[string]Client{"": newFakeConn()})}
	m.SeedOfflineDest("gpu01", "gpu01", offlineNeedsUpgrade, "", nil)

	if !m.lastDaemon("") {
		t.Error("an unrepairable offline host held the session open")
	}
}
