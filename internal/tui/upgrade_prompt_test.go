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

// THE ORDER THAT SHIPS. cmd/quil/main.go seeds the offline rows (:597) and
// wires the provisioner (:765) — 168 lines apart, seeding first — so at seed
// time installDestFn is still nil. upgradeModel wires it FIRST, which is the
// reverse, and that inversion is why every test above passed while the launch
// path offered nothing: enqueueUpgradePrompt's nil-provisioner guard rejected
// the ask before it was ever queued, and the drain point found an empty queue.
//
// The symptom reached a user as a sidebar row with a ⚡ and a pane area reading
// "No tabs in cluster-management@… — Ctrl+T opens one", against a remote daemon
// that was up the whole time with two tabs in it.
func TestUpgradePrompt_SeedBeforeInstallFuncStillOffers(t *testing.T) {
	m := *newSplitDragTestModel(t)
	m.width, m.height = 120, 44
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

	var installed []string
	m.SetInstallFunc(func(dest string) error {
		installed = append(installed, dest)
		return nil
	})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	got := updated.(Model)
	if got.dialog != dialogConfirm || got.confirmKind != confirmKindUpgradeDest {
		t.Fatalf("a host seeded before the provisioner was wired raised no offer: "+
			"dialog=%v kind=%q queue=%+v", got.dialog, got.confirmKind, got.upgradeQueue)
	}
	if got.confirmID != "artyom@host" {
		t.Errorf("confirmID = %q, want the destination", got.confirmID)
	}
}

// The guard that MOVED, pinned on the side the existing test cannot see.
//
// TestUpgradePrompt_NoInstallFuncRaisesNothing asserts only that no dialog
// opens, which was true BEFORE this change for the opposite reason — the ask
// was refused at the door and the queue was empty. Both readings satisfy it, so
// on its own it cannot tell "held until a provisioner arrives" from "dropped".
// That distinction is the entire fix: cmd/quil/main.go wires the provisioner
// 168 lines after it seeds these rows.
func TestUpgradePrompt_NoProvisionerHoldsTheAskRatherThanDroppingIt(t *testing.T) {
	m := *newSplitDragTestModel(t)
	m.width, m.height = 120, 44
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	got := updated.(Model)

	if got.dialog == dialogConfirm {
		t.Fatal("an upgrade was offered with no provisioner wired")
	}
	if len(got.upgradeQueue) != 1 {
		t.Fatalf("the ask was dropped instead of held: %+v — a provisioner wired "+
			"later would then have nothing to surface", got.upgradeQueue)
	}

	// And wiring one later surfaces it, without a second seed.
	//
	// The second size must DIFFER: an unchanged one is swallowed by the
	// poll-echo guard well above the drain point, so re-sending 120x44 here
	// asserts nothing about the queue and fails for a reason that has nothing
	// to do with provisioners. Production never depends on this arm — main.go
	// wires the provisioner before tea.NewProgram, so the first WindowSizeMsg
	// already has one — but "held" is only meaningful if something can still
	// collect it.
	got.SetInstallFunc(func(string) error { return nil })
	updated, _ = got.Update(tea.WindowSizeMsg{Width: 121, Height: 45})
	if final := updated.(Model); final.dialog != dialogConfirm || final.confirmID != "artyom@host" {
		t.Errorf("a held ask did not surface once a provisioner was wired: dialog=%v id=%q",
			final.dialog, final.confirmID)
	}
}

// THE FLAGSHIP PATH, and the one the first version of this fix still lost.
//
// promptNextUpgrade's doc promised "every dialog dismissal calls this again",
// but only the upgrade confirm's own Esc did. handleWhatsNewKey and
// handleDisclaimerKey return to dialogNone and drained nothing — so on the ONE
// launch that matters, the queue filled and was never opened: a client
// auto-update is exactly when gateExtraVersion refuses every configured host
// AND when ResolveWhatsNew puts a dialog on screen. The first WindowSizeMsg
// no-ops on `dialog != dialogNone`, the 1 s size poll is echo-guarded so no
// second one arrives by itself, and dismissing what's-new dropped the ask on
// the floor. Symptom: the ⚡ and no offer — what the PR exists to remove.
func TestUpgradePrompt_SurvivesTheWhatsNewDialogAtLaunch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialog  dialogScreen
		dismiss tea.KeyPressMsg
	}{
		{"what's new", dialogWhatsNew, tea.KeyPressMsg{Code: tea.KeyEscape, Text: "esc"}},
		{"disclaimer", dialogDisclaimer, tea.KeyPressMsg{Code: tea.KeyEnter, Text: "enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := upgradeModel(t)
			m.dialog = tc.dialog
			m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)

			updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
			held := updated.(Model)
			if held.dialog != tc.dialog {
				t.Fatalf("the offer displaced the startup dialog: %v", held.dialog)
			}
			if len(held.upgradeQueue) != 1 {
				t.Fatalf("the ask was dropped before the dialog was even answered: %+v", held.upgradeQueue)
			}

			// Dismissing the startup dialog must surface it — with no resize,
			// because none arrives on its own.
			updated, _ = held.Update(tc.dismiss)
			got := updated.(Model)
			if got.dialog != dialogConfirm || got.confirmKind != confirmKindUpgradeDest {
				t.Fatalf("dismissing %v dropped the queued offer: dialog=%v kind=%q queue=%+v",
					tc.dialog, got.dialog, got.confirmKind, got.upgradeQueue)
			}
			if got.confirmID != "artyom@host" {
				t.Errorf("confirmID = %q, want the destination", got.confirmID)
			}
		})
	}
}

// A host that came BACK must not be offered an upgrade it no longer needs.
//
// applyWorkspaceState clears Offline on reconnect and adoptDest never touches
// installedDests, so the queued ask still resolved to a live project. Accepting
// it runs the same runRemoteSetup the CLI does, which STOPS that daemon — the
// cost the confirm's own body warns about, charged against a host that works.
func TestUpgradePrompt_HostThatCameBackIsNotOffered(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@host", "artyom@host", OfflineNeedsUpgrade, mismatchDetail, nil)
	if len(m.upgradeQueue) != 1 {
		t.Fatalf("fixture did not queue the ask: %+v", m.upgradeQueue)
	}

	// The host reconnects before the queue is drained.
	for _, p := range m.projects {
		if p.Dest == "artyom@host" {
			p.Offline = nil
		}
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	if got := updated.(Model); got.dialog == dialogConfirm {
		t.Errorf("a reconnected host was offered a daemon-restarting upgrade: %q", got.confirmID)
	}
}

// needsInstall queues the same confirm as needsUpgrade, and the body was written
// for the upgrade alone. A host that has never run quil has no daemon to restart
// and no panes to lose; telling the user otherwise invents a cost and buys a
// decline for something that was free.
func TestUpgradePrompt_FirstInstallDoesNotThreatenPanes(t *testing.T) {
	m, _ := upgradeModel(t)
	m.SeedOfflineDest("artyom@fresh", "artyom@fresh", OfflineNeedsInstall, "quil: command not found", nil)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	got := updated.(Model)
	if got.dialog != dialogConfirm {
		t.Fatalf("a host with no quil raised no offer: %v", got.dialog)
	}
	out := got.renderConfirmDialog()

	if !strings.Contains(out, "Install quil on") {
		t.Errorf("a first install is titled as an upgrade:\n%s", out)
	}
	if strings.Contains(out, "RESTARTS") || strings.Contains(out, "is killed") {
		t.Errorf("a host with no daemon was warned its panes would be killed:\n%s", out)
	}
	if !strings.Contains(out, "y install") {
		t.Errorf("the footer still offers an upgrade:\n%s", out)
	}

	// The upgrade wording must survive for the kind it was written for.
	m2, _ := upgradeModel(t)
	m2.SeedOfflineDest("artyom@old", "artyom@old", OfflineNeedsUpgrade, mismatchDetail, nil)
	updated2, _ := m2.Update(tea.WindowSizeMsg{Width: 120, Height: 44})
	up := updated2.(Model).renderConfirmDialog()
	if !strings.Contains(up, "Upgrade quil on") || !strings.Contains(up, "RESTARTS") {
		t.Errorf("the upgrade confirm lost its own warning:\n%s", up)
	}
}
