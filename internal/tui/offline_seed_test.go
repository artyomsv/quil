package tui

import "testing"

// canReconnect is redialFns[dest] != nil, and the launch path now installs a
// dialer for every CONFIGURED destination rather than every connected one. That
// is what makes an offline row's retry reachable at all.
func TestSeededDest_IsReconnectableOnceItsDialerIsInstalled(t *testing.T) {
	m := Model{}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "no route to host", nil)

	if m.canReconnect("gpu01") {
		t.Fatal("reconnectable with no dialer installed")
	}
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, nil })
	if !m.canReconnect("gpu01") {
		t.Error("not reconnectable after SetRedialFunc; the panel's retry would be dead")
	}
}
