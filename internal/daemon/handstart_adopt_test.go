package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/config"
)

// A transcript older than the launch is a previous conversation in the same
// directory. Adopting it would make the next restart resume work the user did
// yesterday instead of what they just started.
func TestNewestSessionSince(t *testing.T) {
	now := time.Now()
	sessions := []claudesessions.Session{
		{ID: "old", Modified: now.Add(-time.Hour)},
		{ID: "recent", Modified: now.Add(-2 * time.Second)},
		{ID: "newest", Modified: now.Add(-time.Second)},
		{ID: "", Modified: now},
	}
	got, ok := newestSessionSince(sessions, now.Add(-30*time.Second))
	if !ok || got != "newest" {
		t.Fatalf("got %q ok=%v, want \"newest\"", got, ok)
	}
	if _, ok := newestSessionSince(sessions[:1], now.Add(-30*time.Second)); ok {
		t.Fatal("adopted a transcript written before the launch")
	}
	if _, ok := newestSessionSince(nil, now); ok {
		t.Fatal("adopted a session from an empty listing")
	}
}

// Two shells in the same directory scan the same project tree. Refusing to
// adopt is an acceptable outcome; adopting the wrong session is not, because
// the next restart would resume someone else's conversation into this pane.
func TestClaimAdoptedSession_RefusesASessionAnotherPaneHolds(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	held := mustPane(t, d, tab.ID)
	held.PluginState = map[string]string{"session_id": "SHARED"}

	mine := mustPane(t, d, tab.ID)
	if d.claimAdoptedSession(mine, "SHARED") {
		t.Fatal("adopted a session another pane already holds")
	}
	if mine.PluginState["session_id"] == "SHARED" {
		t.Fatal("wrote the contested session onto the pane anyway")
	}
	if d.claimAdoptedSession(mine, "MINE") == false {
		t.Fatal("refused an unheld session")
	}
	if mine.PluginState["session_id"] != "MINE" || !mine.Adopted {
		t.Fatalf("session_id=%q adopted=%v", mine.PluginState["session_id"], mine.Adopted)
	}
}

// Adoption records the session and leaves the pane a terminal: its type is what
// it RUNS, and it is running a shell.
func TestAdoptClaudeSession_RecordsTheSessionWithoutRetypingThePane(t *testing.T) {
	prevDelay, prevList := handStartAdoptDelayForTest(t, 0), listSessionsFn
	_ = prevDelay
	listSessionsFn = func(context.Context, string) ([]claudesessions.Session, error) {
		return []claudesessions.Session{{ID: "ADOPTED", Modified: time.Now()}}, nil
	}
	t.Cleanup(func() { listSessionsFn = prevList })

	d, pane, rec := handStartFixture(t, config.HandStartedAdopt)
	pane.handStart.token = "TOK"
	pane.CWD = t.TempDir()

	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;"+pane.CWD+";;;"+"\x1b\\"), time.Now())

	if got := rec.drain(); got != handStartReplyRun {
		t.Fatalf("reply = %q, want %q — adopt must let the command run as typed", got, handStartReplyRun)
	}
	if !waitUntilTrue(t, func() bool {
		pane.PluginMu.Lock()
		defer pane.PluginMu.Unlock()
		return pane.PluginState["session_id"] == "ADOPTED"
	}, 3*time.Second) {
		t.Fatal("the session was never adopted")
	}

	if pane.Type != "terminal" {
		t.Errorf("pane type = %q, want terminal — adoption does not re-type a pane", pane.Type)
	}
	if !pane.Adopted {
		t.Error("pane not marked adopted")
	}
}

// A pane whose agent CRASHED must show that it did. Replacing the evidence
// with a fresh prompt would hide the failure.
func TestReturnToShellOnCleanExit(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")

	crashed := mustPane(t, d, tab.ID)
	crashed.Type = "claude-code"
	crashed.ConvertedFromTerminal = "terminal"
	if d.returnToShellOnCleanExit(crashed, 1) {
		t.Fatal("returned to a shell after a non-zero exit")
	}
	if crashed.Type != "claude-code" {
		t.Fatalf("type = %q, want the failed agent left in place", crashed.Type)
	}

	// A pane that was never converted stays exactly as it is.
	plain := mustPane(t, d, tab.ID)
	plain.Type = "claude-code"
	if d.returnToShellOnCleanExit(plain, 0) {
		t.Fatal("re-typed a pane that was never converted from a terminal")
	}
	if plain.Type != "claude-code" {
		t.Fatalf("type = %q, want claude-code", plain.Type)
	}
}

func TestReturnToShellOnCleanExit_RestoresTheTerminal(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	p := mustPane(t, d, tab.ID)
	p.Type = "claude-code"
	p.ConvertedFromTerminal = "terminal"
	p.InstanceArgs = []string{"--resume", "abc"}
	p.Adopted = true

	if !d.returnToShellOnCleanExit(p, 0) {
		t.Fatal("a cleanly-exited converted pane was not returned to its shell")
	}
	if p.Type != "terminal" {
		t.Errorf("type = %q, want terminal", p.Type)
	}
	if p.ConvertedFromTerminal != "" {
		t.Error("the conversion mark survived, so the next clean exit would fire again")
	}
	if p.InstanceArgs != nil {
		t.Errorf("InstanceArgs = %v, want the agent's arguments dropped", p.InstanceArgs)
	}
	if p.Adopted {
		t.Error("the pane is still marked adopted")
	}
}

// The card must say what to do, not only that something is wrong. A card that
// only reports a fault leaves the user exactly where #221 left its reporter.
func TestUntrackedMessage_NamesTheRemedy(t *testing.T) {
	msg := untrackedMessage("claude", "a typed --settings would contend with Quil's hook settings")
	for _, want := range []string{"claude", "not resume", "--settings", "Ctrl+N"} {
		if !contains(msg, want) {
			t.Errorf("message does not mention %q: %s", want, msg)
		}
	}
	if bare := untrackedMessage("codex", ""); contains(bare, "Reason:") {
		t.Errorf("a reasonless card invented one: %s", bare)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// handStartAdoptDelayForTest shortens the wait the adoption scan takes before
// looking for a transcript, so a test does not sleep for it.
func handStartAdoptDelayForTest(t *testing.T, d time.Duration) time.Duration {
	t.Helper()
	prev := handStartAdoptDelayVar
	handStartAdoptDelayVar = d
	t.Cleanup(func() { handStartAdoptDelayVar = prev })
	return prev
}

// mustPane creates a pane of the given type in a tab, failing the test if the
// session manager refuses.
func mustPane(t *testing.T, d *Daemon, tabID string) *Pane {
	t.Helper()
	p, err := d.session.CreatePane(tabID, "")
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}
	return p
}
