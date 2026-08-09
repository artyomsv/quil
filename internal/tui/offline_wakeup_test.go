package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

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

// Production calls wakeOfflineDests from exactly one place: the !m.sized
// branch of Update's tea.WindowSizeMsg arm, on a statement whose own comment
// warns that a value-receiver copy taken before the pointer-receiver calls
// makes the next resize repeat the work (`resize, attach, wake :=
// m.resizeAllPanes(), m.attachAllDests(), m.wakeOfflineDests()` BEFORE
// `return m, ...`, not `return m, tea.Batch(m.wakeOfflineDests(), ...)`,
// which would copy `m` for the first return value before the pointer
// receivers ever ran). A test calling wakeOfflineDests directly cannot catch
// a regression that moves or reorders that call — nothing drives
// Update(tea.WindowSizeMsg{...}) at all in that case, so the call site that
// makes the decision unreachable would still pass every test in this file.
func TestUpdate_WindowSizeMsg_WakesOfflineDestOnlyOnFirstResize(t *testing.T) {
	m := Model{client: NewRouter(map[string]Client{})}
	m.SeedOfflineDest("gpu01", "gpu01", offlineRetrying, "", nil)

	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m1, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if !m1.sized {
		t.Fatal("the first WindowSizeMsg did not mark the model sized")
	}
	if !m1.offlineWoken["gpu01"] {
		t.Fatal("the first WindowSizeMsg did not wake the offline destination")
	}

	// A second resize must not re-enter the !m.sized branch — wakeOfflineDests
	// is not called from anywhere else — so the only way this survives is if
	// the first call's write to offlineWoken made it into the Model Update
	// actually returned.
	next2, _ := m1.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m2, ok := next2.(Model)
	if !ok {
		t.Fatalf("second Update returned %T, want Model", next2)
	}
	if !m2.offlineWoken["gpu01"] {
		t.Error("offlineWoken did not survive to the second resize's returned Model")
	}
}
