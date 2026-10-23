package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// The boot indicator around a plugin fix: "Running <fix>…" from the press
// (or the daemon's FixLabel) until the fix child paints, "Starting <plugin>…"
// from the fix's clean exit until the restarted command paints. Driven the
// way it happens: dialog keys, workspace_state edges and pane_output frames
// through Update.

// fixBootWorkspace is pluginDialogWorkspace with p1 as a stripe pane, its
// fix label, exited flag and current run generation as given.
func fixBootWorkspace(fixLabel string, exited bool, runGen uint64) WorkspaceStateMsg {
	ws := pluginDialogWorkspace(fixLabel)
	ws.Panes[0].Type = "stripe"
	ws.Panes[0].Exited = exited
	ws.Panes[0].RunGen = runGen
	return ws
}

func p1Output(gen uint64, text string) PaneOutputMsg {
	return PaneOutputMsg{PaneID: "p1", Data: []byte(text), Generation: gen}
}

// seedFixBoot delivers the first state a destination sends — the attach
// snapshot, on which no fix edge acts — so the broadcasts a test sends after
// it are edges.
func seedFixBoot(t *testing.T, m Model) Model {
	t.Helper()
	got := pluginUpdate(t, m, fixBootWorkspace("", false, 1))
	wantNoBoot(t, p1Of(t, got))
	return got
}

func p1Of(t *testing.T, m Model) *PaneModel {
	t.Helper()
	leaf := m.projectByID("proj-source").tabs[0].Root.FindLeaf("p1")
	if leaf == nil || leaf.Pane == nil {
		t.Fatal("fixture: p1 not found")
	}
	return leaf.Pane
}

func wantBoot(t *testing.T, p *PaneModel, label string) {
	t.Helper()
	if !p.preparing || p.bootLabel != label {
		t.Errorf("preparing=%v bootLabel=%q, want the boot mark %q", p.preparing, p.bootLabel, label)
	}
}

func wantNoBoot(t *testing.T, p *PaneModel) {
	t.Helper()
	if p.preparing || p.bootLabel != "" || p.bootAwaitRun {
		t.Errorf("preparing=%v bootLabel=%q awaitRun=%v, want no boot mark", p.preparing, p.bootLabel, p.bootAwaitRun)
	}
}

func TestFixBoot_SendMarksThePane(t *testing.T) {
	t.Parallel()
	m, _, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	clk.advance(pluginLetterGrace)
	wantNoBoot(t, p1Of(t, got))

	got = pluginUpdate(t, got, letterKey('l'))

	p := p1Of(t, got)
	wantBoot(t, p, "Running Log in…")
	if !p.bootAwaitRun || p.bootAwaitGen != 0 {
		t.Errorf("awaitRun=%v awaitGen=%d — a press waits for the next run, whatever its number", p.bootAwaitRun, p.bootAwaitGen)
	}
	if !p.bootOwnsPreparing {
		t.Error("the mark raised `preparing` and must own it")
	}
	if !p.spinnerTickRunning {
		t.Error("no spinner chain armed for the mark")
	}
	if out := stripANSI(p.View()); !strings.Contains(out, "Running Log in…") {
		t.Errorf("pane border does not show the mark:\n%s", out)
	}
}

func TestFixBoot_UpdateSendMarksThePane(t *testing.T) {
	t.Parallel()
	m, _, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginUpdateFrame("p1"))
	clk.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('u'))
	got = pluginUpdate(t, got, letterKey('y'))
	wantBoot(t, p1Of(t, got), "Running Update to 1.56.0…")
}

func TestFixBoot_RefusalClearsTheMark(t *testing.T) {
	t.Parallel()
	m, fake, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginErrorFrame("p1"))
	clk.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('l'))
	msg, _ := fixReqSent(t, fake)
	wantBoot(t, p1Of(t, got), "Running Log in…")

	got = pluginUpdate(t, got, pluginFixRespMsg{ID: msg.ID, Resp: ipc.PluginFixRespPayload{Error: "a fix is already running"}})

	wantNoBoot(t, p1Of(t, got))
	if !got.flashErr {
		t.Error("the refusal did not flash")
	}
	if len(got.pluginBootReqs) != 0 {
		t.Errorf("pluginBootReqs = %d after the answer, want 0", len(got.pluginBootReqs))
	}
}

// A Not now sends a request too, and must not put a boot mark on anything.
func TestFixBoot_NotNowDoesNotMark(t *testing.T) {
	t.Parallel()
	m, _, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, *m, pluginUpdateFrame("p1"))
	clk.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('n'))
	wantNoBoot(t, p1Of(t, got))
}

func TestFixBoot_FixLabelFromStateMarks(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))

	// Another client pressed Log in: the broadcast carries the daemon's label
	// and the fix child's generation, whose output has not arrived.
	got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
	p := p1Of(t, got)
	wantBoot(t, p, "Running Log in…")
	if !p.bootAwaitRun || p.bootAwaitGen != 2 {
		t.Errorf("awaitRun=%v awaitGen=%d, want a wait for generation 2", p.bootAwaitRun, p.bootAwaitGen)
	}

	// The same broadcast again is not a new edge.
	p.resumeStart = time.Now().Add(-time.Second)
	got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
	if time.Since(p1Of(t, got).resumeStart) < 500*time.Millisecond {
		t.Error("a repeated FixLabel re-marked the pane")
	}

	// The fix exited 0 and the daemon restarted stripe: the label turns empty
	// with the pane still running, the new child's output still to come.
	got = pluginUpdate(t, got, fixBootWorkspace("", false, 3))
	p = p1Of(t, got)
	wantBoot(t, p, "Starting stripe…")
	if !p.bootAwaitRun || p.bootAwaitGen != 3 {
		t.Errorf("awaitRun=%v awaitGen=%d, want a wait for generation 3", p.bootAwaitRun, p.bootAwaitGen)
	}
}

func TestFixBoot_ExitedPaneIsNotMarked(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, seedFixBoot(t, *m), fixBootWorkspace("Log in", false, 2))
	wantBoot(t, p1Of(t, got), "Running Log in…")

	// The fix failed: the daemon wrote "fix failed (exit N)" into the pane
	// and the child is gone. Nothing may cover that output.
	got = pluginUpdate(t, got, fixBootWorkspace("", true, 2))
	wantNoBoot(t, p1Of(t, got))
}

// A restart that could not spawn is a SpawnError, not a boot.
func TestFixBoot_SpawnErrorIsNotMarked(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, seedFixBoot(t, *m), fixBootWorkspace("Log in", false, 2))
	ws := fixBootWorkspace("", false, 2)
	ws.Panes[0].SpawnError = "worktree is gone"
	got = pluginUpdate(t, got, ws)
	wantNoBoot(t, p1Of(t, got))
}

// The daemon spawns, flushes the child's first bytes 2 ms later, and only
// then broadcasts: when the run's output beat the edge, there is nothing to
// cover and a mark would wait for a generation that never comes.
func TestFixBoot_EdgeAfterTheRunArrivedDoesNotMark(t *testing.T) {
	t.Parallel()
	t.Run("running", func(t *testing.T) {
		m, _, _ := newPluginDialogTestModel(t)
		got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))
		got = pluginUpdate(t, got, p1Output(2, "Your browser has been opened to visit:\r\n"))
		got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
		wantNoBoot(t, p1Of(t, got))
	})
	t.Run("starting", func(t *testing.T) {
		m, _, _ := newPluginDialogTestModel(t)
		got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))
		got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
		wantBoot(t, p1Of(t, got), "Running Log in…")
		got = pluginUpdate(t, got, p1Output(2, "Done! The Stripe CLI is configured\r\n"))
		got = pluginUpdate(t, got, p1Output(3, "Ready! Your webhook signing secret is…"))
		// The restart's edge lands after its first frame: the stale Running
		// mark goes, and no Starting mark is put on a pane that is painting.
		got = pluginUpdate(t, got, fixBootWorkspace("", false, 3))
		wantNoBoot(t, p1Of(t, got))
	})
}

// The mark survives the old child's frames and the new run's VT reset, and
// clears on the first frame of the new run that shows something.
func TestFixBoot_FirstOutputOfTheNewRunClears(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	// The pane has produced live output in this session (generation 1); the
	// fix ran as generation 2 and its restart is generation 3.
	got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))
	got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
	got = pluginUpdate(t, got, fixBootWorkspace("", false, 3))
	p := p1Of(t, got)
	wantBoot(t, p, "Starting stripe…")

	// A late frame of the FIX's run, past the minimum display, on a screen
	// that is anything but blank: not the run the mark waits for.
	p.resumeStart = time.Now().Add(-3 * time.Second)
	got = pluginUpdate(t, got, p1Output(2, "fix done\r\n"))
	wantBoot(t, p1Of(t, got), "Starting stripe…")

	// The restart's first frame only clears the screen: the VT resets, the
	// wait is over, and the indicator now covers the blank pane.
	got = pluginUpdate(t, got, p1Output(3, "\x1b[2J\x1b[H"))
	p = p1Of(t, got)
	wantBoot(t, p, "Starting stripe…")
	if p.bootAwaitRun {
		t.Error("the new run's frame did not end the wait")
	}

	// The frame that paints, past the minimum display, settles it.
	p.resumeStart = time.Now().Add(-3 * time.Second)
	got = pluginUpdate(t, got, p1Output(3, "Ready! Your webhook signing secret is…"))
	wantNoBoot(t, p1Of(t, got))
}

// A press mark keeps its wait when the fix's edge arrives (same fix, run not
// here yet), learning the generation; the fix's output then ends it.
func TestFixBoot_PressMarkTakesTheEdgesGeneration(t *testing.T) {
	t.Parallel()
	m, _, clk := newPluginDialogTestModel(t)
	got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))
	got = pluginUpdate(t, got, pluginErrorFrame("p1"))
	clk.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('l'))
	p := p1Of(t, got)
	start := p.resumeStart

	got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
	p = p1Of(t, got)
	wantBoot(t, p, "Running Log in…")
	if p.bootAwaitGen != 2 || !p.resumeStart.Equal(start) {
		t.Errorf("awaitGen=%d resumeStart moved=%v, want the press's mark kept with generation 2", p.bootAwaitGen, !p.resumeStart.Equal(start))
	}
	got = pluginUpdate(t, got, p1Output(2, "\x1b[2J"))
	if p1Of(t, got).bootAwaitRun {
		t.Error("the fix's first frame did not end the wait")
	}
}

// A fresh pane's own boot is not ours to end: clearing a mark that found
// `preparing` already up leaves it up.
func TestFixBoot_ClearKeepsAFreshPanesOwnBoot(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	// Seeded first, then the pane set booting: seedFixBoot's no-mark check
	// would read the pane's own `preparing` as a mark.
	seeded := seedFixBoot(t, *m)
	p1Of(t, seeded).preparing = true // a pane created moments ago, still booting
	got := pluginUpdate(t, seeded, fixBootWorkspace("Log in", false, 2))
	p := p1Of(t, got)
	wantBoot(t, p, "Running Log in…")
	if p.bootOwnsPreparing {
		t.Fatal("the mark claims a `preparing` it did not raise")
	}
	got = pluginUpdate(t, got, fixBootWorkspace("", true, 2))
	p = p1Of(t, got)
	if !p.preparing || p.bootLabel != "" {
		t.Errorf("preparing=%v bootLabel=%q, want the pane's own boot kept and only the label gone", p.preparing, p.bootLabel)
	}
}

// Bookkeeping follows the panes: a pane that left the broadcast drops its
// seen label and its request; a destination that left takes its panes'.
func TestFixBoot_BookkeepingPrunedWithThePane(t *testing.T) {
	t.Parallel()
	m, _, clk := newPluginDialogTestModel(t)
	got := pluginUpdate(t, seedFixBoot(t, *m), fixBootWorkspace("Log in", false, 2))
	got = pluginUpdate(t, got, pluginErrorFrame("p2"))
	clk.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('l'))
	if len(got.fixBootSeen) != 1 || len(got.pluginBootReqs) != 1 {
		t.Fatalf("setup: seen=%d reqs=%d, want 1 and 1", len(got.fixBootSeen), len(got.pluginBootReqs))
	}

	ws := fixBootWorkspace("Log in", false, 2)
	ws.Tabs[0].Panes = []string{"p3"} // p1 and p2 are gone; p3 moved in
	ws.Panes = append(ws.Panes[:0], PaneInfo{ID: "p3", TabID: "tab-src-1", Type: "terminal"}, ws.Panes[3])
	ws.Tabs[1].Panes = nil
	got = pluginUpdate(t, got, ws)
	if len(got.fixBootSeen) != 0 || len(got.pluginBootReqs) != 0 {
		t.Errorf("seen=%d reqs=%d after the panes left, want 0 and 0", len(got.fixBootSeen), len(got.pluginBootReqs))
	}

	// A destination leaving: the remote pane's request goes with it.
	m2, _, clk2 := newPluginDialogTestModel(t)
	remotePaneOnScreen(t, m2)
	frame := pluginErrorFrame("rp")
	frame.Dest = "user@TEST-host"
	got = pluginUpdate(t, *m2, frame)
	clk2.advance(pluginLetterGrace)
	got = pluginUpdate(t, got, letterKey('l'))
	if len(got.pluginBootReqs) != 1 {
		t.Fatalf("setup: reqs=%d, want 1", len(got.pluginBootReqs))
	}
	got.dropPluginDialogsFor("user@TEST-host")
	if len(got.pluginBootReqs) != 0 {
		t.Errorf("reqs=%d after the destination left, want 0", len(got.pluginBootReqs))
	}
}

// The first state after a reattach is a snapshot: a fix edge that happened
// during the outage must not mark (its run may have painted before the
// outage, and outputGeneration is zeroed, so the wait would run to the cap),
// and a mark still up from before is settled. Later edges act again.
func TestFixBoot_FirstStateAfterReattachDoesNotMark(t *testing.T) {
	t.Parallel()
	t.Run("fix ended during the outage", func(t *testing.T) {
		m, _, _ := newPluginDialogTestModel(t)
		got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))
		got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 2))
		wantBoot(t, p1Of(t, got), "Running Log in…")

		got.armReattachReset("")
		got = pluginUpdate(t, got, fixBootWorkspace("", false, 3))
		wantNoBoot(t, p1Of(t, got))
		if _, still := got.fixBootSeen["p1"]; still {
			t.Error("fixBootSeen still holds the pre-outage label")
		}

		// The connection is live again: the next edge is a real one.
		got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 4))
		wantBoot(t, p1Of(t, got), "Running Log in…")
	})
	t.Run("fix started during the outage", func(t *testing.T) {
		m, _, _ := newPluginDialogTestModel(t)
		got := seedFixBoot(t, pluginUpdate(t, *m, p1Output(1, "stripe listen\r\n")))

		got.armReattachReset("")
		got = pluginUpdate(t, got, fixBootWorkspace("Log in", false, 3))
		wantNoBoot(t, p1Of(t, got))
		if got.fixBootSeen["p1"] != "Log in" {
			t.Errorf("fixBootSeen[p1] = %q, want the snapshot's label seeded", got.fixBootSeen["p1"])
		}

		// The fix ends after the reattach: the restart gap is real.
		got = pluginUpdate(t, got, fixBootWorkspace("", false, 4))
		wantBoot(t, p1Of(t, got), "Starting stripe…")
	})
}

// The spinner chain's own stop clears the mark's words with the flag.
func TestFixBoot_SpinnerStopClearsTheLabel(t *testing.T) {
	t.Parallel()
	m, _, _ := newPluginDialogTestModel(t)
	got := pluginUpdate(t, seedFixBoot(t, *m), fixBootWorkspace("Log in", false, 2))
	p := p1Of(t, got)
	wantBoot(t, p, "Running Log in…")

	// Past the safety cap the chain gives up, label and all.
	p.resumeStart = time.Now().Add(-restoreSafetyCap)
	got = pluginUpdate(t, got, spinnerTickMsg{paneID: "p1", frame: 3})
	wantNoBoot(t, p1Of(t, got))
	if p1Of(t, got).spinnerTickRunning {
		t.Error("spinnerTickRunning left set after the chain stopped")
	}
}

// The body indicator uses the mark's words, one line, on a blank screen.
func TestFixBoot_BodyShowsTheLabel(t *testing.T) {
	t.Parallel()
	p := NewPaneModel("p1", 1024)
	p.Width, p.Height = 60, 12
	p.preparing = true
	p.bootLabel = "Starting Stripe…"
	out := stripANSI(p.View())
	if !strings.Contains(out, "Starting Stripe…") {
		t.Errorf("pane body does not show the boot label:\n%s", out)
	}
	if strings.Contains(out, "waiting for first output") {
		t.Errorf("a fix boot renders the restore checklist:\n%s", out)
	}
}
