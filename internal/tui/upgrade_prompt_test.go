package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The agreement these tests pin: a remote running an OLDER quil than this
// client must be OFFERED an upgrade inside the tool. The machinery existed
// (installOffer, installDest, the once-per-host guard) but only the New Project
// dialog reached it, so the two paths a restart goes through — the launch dial
// and the reconnect ladder — left the user a parked row and a shell command.

// upgradeModel returns a model wired with an install seam that records the
// hosts it was asked to provision.
func upgradeModel(t *testing.T) (Model, *[]string) {
	t.Helper()
	m := *newSplitDragTestModel(t)
	m.width, m.height = 120, 44
	var installed []string
	m.SetInstallFunc(func(dest string) error {
		installed = append(installed, dest)
		return nil
	})
	return m, &installed
}

const mismatchDetail = "artyom@host runs 1.54.0, this client runs 1.54.1; run `quil remote setup artyom@host`"

// The launch path. SeedOfflineDest runs before the program starts, so the ask
// has to survive until Update can show it — a WindowSizeMsg always arrives.
func TestUpgradePrompt_LaunchSeedRaisesTheConfirm(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

	if m.dialog == dialogConfirm {
		t.Fatal("the confirm opened at seed time — the program has not started yet")
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	got := updated.(Model)

	if got.dialog != dialogConfirm || got.confirmKind != confirmKindUpgradeDest {
		t.Fatalf("no upgrade confirm after launch: dialog=%v kind=%q", got.dialog, got.confirmKind)
	}
	if got.confirmID != "artyom@host" {
		t.Errorf("confirmID = %q, want the destination", got.confirmID)
	}
	if !strings.Contains(got.confirmDetail, "1.54.0") {
		t.Errorf("the confirm does not carry the version pair: %q", got.confirmDetail)
	}
}

// The rendered dialog has to say what is wrong and what it costs — the whole
// complaint was a lightning bolt with no explanation.
func TestUpgradePrompt_ConfirmExplainsItself(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	out := updated.(Model).renderConfirmDialog()

	for _, want := range []string{"Upgrade quil on", "artyom@host", "1.54.0", "RESTARTS", "y upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirm does not mention %q:\n%s", want, out)
		}
	}
}

// `y` runs the same push `quil remote setup` does — no shell required.
func TestUpgradePrompt_YRunsTheInstall(t *testing.T) {
	m, installed := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	got := updated.(Model)
	if cmd == nil {
		t.Fatal("accepting the upgrade produced no command")
	}
	runCmd(cmd)

	if len(*installed) != 1 || (*installed)[0] != "artyom@host" {
		t.Errorf("install ran for %v, want [artyom@host]", *installed)
	}
	if got.dialog != dialogNone {
		t.Error("the confirm stayed open after accepting")
	}
	if !got.installedDests["artyom@host"] {
		t.Error("the once-per-host guard was not armed — a daemon that fails to restart would loop")
	}
}

// Enter must NOT accept. This dialog opens by itself at launch, so a reflexive
// Enter would restart a remote daemon and kill whatever its panes were running.
func TestUpgradePrompt_EnterDoesNotAccept(t *testing.T) {
	m, installed := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Text: "enter"})
	if cmd != nil {
		runCmd(cmd)
	}
	if len(*installed) != 0 {
		t.Errorf("Enter provisioned %v — only `y` may accept", *installed)
	}
	if updated.(Model).dialog != dialogConfirm {
		t.Error("Enter closed the confirm")
	}
}

// A client update leaves EVERY configured host stale at once. Dismissing the
// first must not hide the rest.
func TestUpgradePrompt_QueueDrainsAcrossHosts(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("a@one", "a@one", OfflineNeedsUpgrade, mismatchDetail, nil)
	m.SeedOfflineDest("b@two", "b@two", OfflineNeedsUpgrade, mismatchDetail, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = updated.(Model)
	if m.confirmID != "a@one" {
		t.Fatalf("first prompt is for %q, want a@one", m.confirmID)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape, Text: "esc"})
	got := updated.(Model)
	if got.dialog != dialogConfirm || got.confirmID != "b@two" {
		t.Errorf("after declining the first, dialog=%v id=%q — want the second host offered", got.dialog, got.confirmID)
	}
}

// Declining must not mark the host installed: the user said "not now", not
// "never", and a later reconnect is entitled to ask again.
func TestUpgradePrompt_DecliningDoesNotArmTheGuard(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape, Text: "esc"})
	if updated.(Model).installedDests["artyom@host"] {
		t.Error("declining armed the once-per-host guard — the host could never be offered again")
	}
}

// The reconnect ladder's own comment promised this and delivered nothing: a
// link that drifts out of version mid-session must raise the same offer.
func TestUpgradePrompt_ReconnectMismatchRaisesTheConfirm(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineRetrying, "unreachable", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = updated.(Model)
	if m.dialog != dialogNone {
		t.Fatalf("fixture opened a dialog for a merely-unreachable host: %v", m.dialog)
	}

	// The arm drops a result for a superseded generation or an inactive link,
	// so the ladder has to be genuinely mid-attempt for this to be reached —
	// which is the only state a real mismatch can arrive in.
	ls := m.linkFor("artyom@host")
	ls.active = true
	updated, _ = m.Update(redialResultMsg{
		dest: "artyom@host",
		gen:  ls.gen,
		err:  fmt.Errorf("dial: %w", ErrRemoteVersionMismatch),
	})
	got := updated.(Model)

	if got.dialog != dialogConfirm || got.confirmKind != confirmKindUpgradeDest {
		t.Errorf("a mid-session version drift raised no offer: dialog=%v kind=%q", got.dialog, got.confirmKind)
	}
}

// A merely-unreachable host must NOT be offered an upgrade — it enters the
// reconnect ladder and heals itself. Offering to reinstall over a flaky link
// would be the confidently-wrong answer this feature exists to remove.
func TestUpgradePrompt_RetryingHostIsNotOffered(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineRetrying, "ssh: connect: no route to host", nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	if got := updated.(Model); got.dialog == dialogConfirm {
		t.Errorf("an unreachable host was offered an upgrade: %q", got.confirmID)
	}
}

// Without a provisioner there is nothing to offer, and a dialog whose only
// action cannot run is worse than the parked row.
func TestUpgradePrompt_NoInstallFuncRaisesNothing(t *testing.T) {
	m := *newSplitDragTestModel(t)
	m.width, m.height = 120, 44
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	if updated.(Model).dialog == dialogConfirm {
		t.Error("an upgrade was offered with no provisioner wired")
	}
}

// The startup disclaimer and the plugin migration are dialogs the user is
// already answering; the migration deliberately blocks until resolved. The
// offer waits rather than displacing them.
func TestUpgradePrompt_WaitsForAStartupDialog(t *testing.T) {
	m, _ := upgradeModel(t)
	m.dialog = dialogDisclaimer
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	m = updated.(Model)
	if m.dialog != dialogDisclaimer {
		t.Fatalf("the upgrade offer displaced the disclaimer: %v", m.dialog)
	}
	// And it is not lost — closing the disclaimer surfaces it.
	if len(m.upgradeQueue) != 1 {
		t.Errorf("the queued offer was dropped: %+v", m.upgradeQueue)
	}
}

// A host already provisioned this session that still reports a mismatch did not
// restart its daemon; pushing the same archive again cannot change that. The
// guard is shared with the New Project dialog's offer for exactly this loop.
func TestUpgradePrompt_AlreadyInstalledHostIsNotOfferedAgain(t *testing.T) {
	m, _ := upgradeModel(t)
	m.installedDests = map[string]bool{"artyom@host": true}
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	if got := updated.(Model); got.dialog == dialogConfirm {
		t.Error("a host provisioned this session was offered again — install, retry, same error, install")
	}
}
