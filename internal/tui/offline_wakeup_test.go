package tui

import "testing"

// A ladder against a version-drifted daemon re-authenticates and re-fails
// forever: it cannot succeed until the user upgrades the far side.
func TestOfflineDestMsg_LaddersOnlyRetryableKinds(t *testing.T) {
	for _, tt := range []struct {
		name string
		kind OfflineKind
		want bool
	}{
		{"retrying", offlineRetrying, true},
		{"needs upgrade", offlineNeedsUpgrade, false},
		{"needs install", offlineNeedsInstall, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{client: NewRouter(map[string]Client{})}
			m.SeedOfflineDest("gpu01", "gpu01", tt.kind, "", nil)
			m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, nil })

			next, _ := m.Update(offlineDestMsg{dest: "gpu01"})
			got := next.(Model).linkOf("gpu01").active

			if got != tt.want {
				t.Errorf("ladder active = %v, want %v", got, tt.want)
			}
		})
	}
}

// The linkLostMsg arm re-arms listenForMessages because the listen loop STOPPED
// to deliver it. A synthesised one stops nothing, so reusing that arm installs a
// second permanent reader of r.in — two readers reorder pane output and
// workspace_state, with no error anywhere.
func TestOfflineDestMsg_DoesNotArmASecondListener(t *testing.T) {
	listens := 0
	m := Model{client: NewRouter(map[string]Client{})}
	m.listenCountFn = func() { listens++ }
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", nil)
	m.SetRedialFunc("gpu01", func(Client) (Client, error) { return nil, nil })

	m.Update(offlineDestMsg{dest: "gpu01"})

	if listens != 0 {
		t.Errorf("listenForMessages armed %d times, want 0", listens)
	}
}

// One wake-up per offline destination, emitted once — a second round would
// re-enter beginReconnect, which early-returns, but the Cmd churn is pointless.
func TestWakeOfflineDests_OneCmdPerLadderedDest(t *testing.T) {
	m := Model{client: NewRouter(map[string]Client{})}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", nil)
	m.SeedOfflineDest("build02", "build02", offlineNeedsUpgrade, "", nil)

	cmd := m.wakeOfflineDests()
	if cmd == nil {
		t.Fatal("no wake-up command for a laddered destination")
	}
	if m.offlineWoken["build02"] {
		t.Error("a non-laddered destination was woken")
	}
	if !m.offlineWoken["gpu01"] {
		t.Error("the laddered destination was not marked woken")
	}
	if second := m.wakeOfflineDests(); second != nil {
		t.Error("a second call produced more commands; the wake-up must fire once")
	}
	_ = cmd
}
