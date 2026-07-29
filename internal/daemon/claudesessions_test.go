package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/claudesessions"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// stubSessionList swaps the discovery seam for the duration of a test.
func stubSessionList(t *testing.T, fn func(cwd string) ([]claudesessions.Session, bool, error)) {
	t.Helper()
	prev := listClaudeSessionsFn
	// Adapts the ctx-free stub the tests write onto the ctx-aware seam. Keeping
	// the ergonomic signature here avoids threading a parameter through every
	// stub body when none of them exercise cancellation — the packages that own
	// that behaviour test it directly.
	listClaudeSessionsFn = func(_ context.Context, cwd string) ([]claudesessions.Session, bool, error) {
		return fn(cwd)
	}
	t.Cleanup(func() { listClaudeSessionsFn = prev })
}

// stubHookSessionID swaps the hook-file reader so tests never touch
// $QUIL_HOME/sessions/.
func stubHookSessionID(t *testing.T, byPane map[string]string) {
	t.Helper()
	prev := readHookSessionIDFn
	readHookSessionIDFn = func(paneID string) (string, error) {
		if id, ok := byPane[paneID]; ok {
			return id, nil
		}
		return "", errors.New("no hook file")
	}
	t.Cleanup(func() { readHookSessionIDFn = prev })
}

// withClaudePlugin loads the SHIPPED default plugins into the daemon's
// registry. Daemon.New leaves it empty (Start does the loading), so without
// this spawnPane resolves "claude-code" to nil, falls back to the terminal
// built-in, and never reaches the preassign_id branch under test. Loading the
// real defaults also means these tests exercise the config that actually ships.
func withClaudePlugin(t *testing.T, d *Daemon) {
	t.Helper()
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatalf("EnsureDefaultPlugins: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if reg.Get("claude-code") == nil {
		t.Fatal("claude-code plugin missing from the loaded defaults")
	}
	d.registry = reg
}

func sessionsReq(t *testing.T, cwd string) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgClaudeSessionsReq, ipc.ClaudeSessionsReqPayload{CWD: cwd})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// TestClaudeSessionsResponse_EchoesCWDVerbatim guards the staleness contract.
// The TUI drops any response whose echoed CWD differs from the directory it is
// showing, so normalizing here would make a legitimate request look permanently
// stale and hang the field on "Scanning…".
func TestClaudeSessionsResponse_EchoesCWDVerbatim(t *testing.T) {
	d := newTestDaemon(t)
	stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) { return nil, false, nil })

	for _, cwd := range []string{
		`E:\Projects\quil`,
		`E:\Projects\quil\`,
		"/home/user/proj",
		"  /padded/path  ",
		"relative/path",
	} {
		t.Run(cwd, func(t *testing.T) {
			resp := d.claudeSessionsResponse(sessionsReq(t, cwd))
			if resp.CWD != cwd {
				t.Errorf("echoed CWD = %q, want %q verbatim", resp.CWD, cwd)
			}
		})
	}
}

func TestClaudeSessionsResponse_EmptyCWD_NoScan(t *testing.T) {
	d := newTestDaemon(t)
	called := false
	stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) {
		called = true
		return nil, false, nil
	})

	resp := d.claudeSessionsResponse(sessionsReq(t, ""))
	if called {
		t.Error("empty CWD must not trigger a filesystem scan")
	}
	if resp.Error != "" || len(resp.Sessions) != 0 {
		t.Errorf("resp = %+v, want empty with no error", resp)
	}
}

func TestClaudeSessionsResponse_ListError_ReportsWithoutSessions(t *testing.T) {
	d := newTestDaemon(t)
	stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) {
		return nil, false, errors.New("permission denied")
	})

	resp := d.claudeSessionsResponse(sessionsReq(t, "/some/dir"))
	if resp.Error == "" {
		t.Error("want a non-empty Error when discovery fails")
	}
	if len(resp.Sessions) != 0 {
		t.Errorf("Sessions = %d, want 0 on error", len(resp.Sessions))
	}
	// The CWD must still echo, or the TUI drops the error as stale and keeps
	// showing "Scanning…" — the exact failure this field's timeout exists for.
	if resp.CWD != "/some/dir" {
		t.Errorf("echoed CWD = %q, want it echoed even on the error path", resp.CWD)
	}
}

func TestClaudeSessionsResponse_MarksSessionsHeldByLivePanes(t *testing.T) {
	d := newTestDaemon(t)
	modified := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) {
		return []claudesessions.Session{
			{ID: "sess-free", Title: "free one", Modified: modified},
			{ID: "sess-live-hook", Title: "held via hook file", Modified: modified},
			{ID: "sess-live-state", Title: "held via plugin state", Modified: modified},
		}, false, nil
	})

	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a", "pane-0000000b", "pane-0000000c"}},
		[]*Pane{
			// Hook file is authoritative: this pane rotated away from the
			// preassigned id (a /clear), so the CURRENT id must be marked.
			{ID: "pane-0000000a", TabID: "tab-0000000a", Type: "claude-code",
				PTY:         &fakeSession{},
				PluginState: map[string]string{"session_id": "sess-rotated-away"}},
			// No hook file yet — PluginState is the fallback.
			{ID: "pane-0000000b", TabID: "tab-0000000a", Type: "claude-code",
				PTY:         &fakeSession{},
				PluginState: map[string]string{"session_id": "sess-live-state"}},
			// Not a claude pane; its state must not mark anything.
			{ID: "pane-0000000c", TabID: "tab-0000000a", Type: "terminal",
				PTY:         &fakeSession{},
				PluginState: map[string]string{"session_id": "sess-free"}},
		},
	)
	stubHookSessionID(t, map[string]string{"pane-0000000a": "sess-live-hook"})

	resp := d.claudeSessionsResponse(sessionsReq(t, "/proj"))
	byID := map[string]ipc.ClaudeSessionInfo{}
	for _, s := range resp.Sessions {
		byID[s.ID] = s
	}
	if len(byID) != 3 {
		t.Fatalf("got %d sessions, want 3", len(byID))
	}
	if got := byID["sess-live-hook"].InUsePaneID; got != "pane-0000000a" {
		t.Errorf("hook-recorded session InUsePaneID = %q, want pane-0000000a (hook file must win over PluginState)", got)
	}
	if got := byID["sess-live-state"].InUsePaneID; got != "pane-0000000b" {
		t.Errorf("PluginState session InUsePaneID = %q, want pane-0000000b (fallback when no hook file)", got)
	}
	if got := byID["sess-free"].InUsePaneID; got != "" {
		t.Errorf("free session InUsePaneID = %q, want empty — a terminal pane's state must not mark a claude session", got)
	}
	if byID["sess-free"].ModifiedMs != modified.UnixMilli() {
		t.Errorf("ModifiedMs = %d, want %d", byID["sess-free"].ModifiedMs, modified.UnixMilli())
	}
}

// TestClaudeSessionsResponse_TruncatedFromDiscovery: the flag is reported by
// discovery, not inferred from a full-looking result. Inferring it
// (len == MaxSessions) mislabels a directory holding exactly the cap, telling
// the user older sessions are hidden when none are.
func TestClaudeSessionsResponse_TruncatedFromDiscovery(t *testing.T) {
	full := func() []claudesessions.Session {
		out := make([]claudesessions.Session, claudesessions.MaxSessions)
		for i := range out {
			out[i] = claudesessions.Session{ID: "s", Modified: time.Now()}
		}
		return out
	}

	t.Run("reported truncation propagates", func(t *testing.T) {
		d := newTestDaemon(t)
		stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) {
			return full(), true, nil
		})
		if resp := d.claudeSessionsResponse(sessionsReq(t, "/proj")); !resp.Truncated {
			t.Error("Truncated = false, want true when discovery reports it")
		}
	})

	t.Run("exactly the cap is not truncated", func(t *testing.T) {
		d := newTestDaemon(t)
		stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) {
			return full(), false, nil
		})
		if resp := d.claudeSessionsResponse(sessionsReq(t, "/proj")); resp.Truncated {
			t.Error("Truncated = true for exactly MaxSessions with nothing dropped")
		}
	})
}

func TestApplyResumeSessionID(t *testing.T) {
	valid := "2db05609-f1d5-4576-b5b2-ff114519726b"
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"valid uuid stored", valid, valid},
		{"empty ignored", "", ""},
		{"too short rejected", "abc123", ""},
		{"path traversal rejected", "../../etc/passwd", ""},
		{"flag injection rejected", "--dangerously-skip-permissions", ""},
		{"non-hex rejected", strings.Repeat("z", 36), ""},
		{"overlong rejected", strings.Repeat("a", 65), ""},
		// The looser `^[0-9a-fA-F-]{32,64}$` shape these values were first
		// checked against accepted all of the following. Each becomes the
		// operand of --resume in argv, and the leading-dash forms are
		// flag-shaped tokens; the canonical UUID pattern rejects them.
		{"all dashes rejected", strings.Repeat("-", 36), ""},
		{"leading dash rejected", "-" + strings.Repeat("a", 35), ""},
		{"hex without dashes rejected", strings.Repeat("a", 32), ""},
		{"wrong group layout rejected", "2db05609f1d5-4576-b5b2-ff114519726b-x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDaemon(t)
			pane := &Pane{ID: "pane-0000000a"}
			d.applyResumeSessionID(pane, tt.raw)
			if got := pane.PluginState["resume_session_id"]; got != tt.want {
				t.Errorf("resume_session_id = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyResumeSessionID_RefusesSessionHeldByLivePane is where the
// "already open in another pane" guarantee actually lives. The TUI greys those
// rows out, but that is presentation: any IPC client can send the id directly,
// and the TUI's own listing is fetched at T0 and committed at T1, so a second
// pane can claim the session in between. Two claude processes appending to one
// transcript destroy each other's history.
func TestApplyResumeSessionID_RefusesSessionHeldByLivePane(t *testing.T) {
	held := "2db05609-f1d5-4576-b5b2-ff114519726b"
	d := newTestDaemon(t)
	stubHookSessionID(t, nil)

	live := &Pane{
		ID: "pane-0000000a", TabID: "tab-0000000a", Type: "claude-code",
		PTY:         &fakeSession{},
		PluginState: map[string]string{"session_id": held},
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}},
		[]*Pane{live},
	)

	fresh := &Pane{ID: "pane-0000000b"}
	d.applyResumeSessionID(fresh, held)

	if got := fresh.PluginState["resume_session_id"]; got != "" {
		t.Errorf("resume_session_id = %q, want empty — the session is held by a live pane", got)
	}
}

// TestApplyResumeSessionID_RefusesSessionClaimedByUnspawnedPane is the race a
// running-only occupancy test cannot see: a pane created moments ago has
// claimed a session but has no PTY yet, so it looks exactly like "not running".
// Two clients creating panes for the same session on their own dispatch
// goroutines would both find it free and both spawn `claude --resume` on one
// transcript.
func TestApplyResumeSessionID_RefusesSessionClaimedByUnspawnedPane(t *testing.T) {
	wanted := "2db05609-f1d5-4576-b5b2-ff114519726b"
	d := newTestDaemon(t)
	stubHookSessionID(t, nil)

	// First pane claimed the session but has not spawned — no PTY.
	claimed := &Pane{
		ID: "pane-0000000a", TabID: "tab-0000000a", Type: "claude-code",
		PluginState: map[string]string{"resume_session_id": wanted},
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}},
		[]*Pane{claimed},
	)

	second := &Pane{ID: "pane-0000000b"}
	d.applyResumeSessionID(second, wanted)

	if got := second.PluginState["resume_session_id"]; got != "" {
		t.Errorf("resume_session_id = %q, want empty — the session is already claimed by an unspawned pane", got)
	}
	// The picker's own display still only marks RUNNING panes, so an unspawned
	// claim must not surface as "open in another pane".
	if _, shown := d.liveClaudeSessionIDs()[wanted]; shown {
		t.Error("an unspawned claim must not render as in-use in the picker")
	}
}

// TestApplyResumeSessionID_ConcurrentCreatesClaimOnce drives the actual race:
// N goroutines racing to claim one session must produce exactly one winner.
func TestApplyResumeSessionID_ConcurrentCreatesClaimOnce(t *testing.T) {
	wanted := "2db05609-f1d5-4576-b5b2-ff114519726b"
	d := newTestDaemon(t)
	stubHookSessionID(t, nil)

	const racers = 8
	panes := make([]*Pane, racers)
	ids := make([]string, racers)
	for i := range panes {
		ids[i] = fmt.Sprintf("pane-0000000%x", i)
		panes[i] = &Pane{ID: ids[i], TabID: "tab-0000000a", Type: "claude-code"}
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: ids},
		panes,
	)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, p := range panes {
		wg.Add(1)
		go func(p *Pane) {
			defer wg.Done()
			<-start
			d.applyResumeSessionID(p, wanted)
		}(p)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, p := range panes {
		p.PluginMu.Lock()
		if p.PluginState["resume_session_id"] == wanted {
			winners++
		}
		p.PluginMu.Unlock()
	}
	if winners != 1 {
		t.Errorf("%d panes claimed the same session, want exactly 1 — concurrent claims must be serialized", winners)
	}
}

// TestApplyResumeSessionID_AllowsSessionOfExitedPane is the other half of the
// same rule: a pane whose claude exited still sits in its tab holding the id,
// but nothing is running, so the session must be resumable again rather than
// blocked forever.
func TestApplyResumeSessionID_AllowsSessionOfExitedPane(t *testing.T) {
	freed := "2db05609-f1d5-4576-b5b2-ff114519726b"
	d := newTestDaemon(t)
	stubHookSessionID(t, nil)

	exitCode := 0
	dead := &Pane{
		ID: "pane-0000000a", TabID: "tab-0000000a", Type: "claude-code",
		PTY:         &fakeSession{},
		ExitCode:    &exitCode,
		PluginState: map[string]string{"session_id": freed},
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}},
		[]*Pane{dead},
	)

	fresh := &Pane{ID: "pane-0000000b"}
	d.applyResumeSessionID(fresh, freed)

	if got := fresh.PluginState["resume_session_id"]; got != freed {
		t.Errorf("resume_session_id = %q, want %q — an exited pane must not hold its session hostage", got, freed)
	}
}

// TestRefreshPluginStateFromHooks_RetiresResumeTarget: once the hook has
// recorded the pane's own session, the creation-time resume target has served
// its only purpose (covering the window before the first hook fired). Leaving
// it would let a later restore pull the pane back into a conversation the user
// walked away from via /clear.
func TestRefreshPluginStateFromHooks_RetiresResumeTarget(t *testing.T) {
	original := "2db05609-f1d5-4576-b5b2-ff114519726b"
	rotated := "9f8e7d6c-1a2b-3c4d-5e6f-0a1b2c3d4e5f"
	d := newTestDaemon(t)
	stubHookSessionID(t, map[string]string{"pane-0000000a": rotated})

	pane := &Pane{
		ID: "pane-0000000a", TabID: "tab-0000000a", Type: "claude-code",
		PluginState: map[string]string{
			"session_id":        original,
			"resume_session_id": original,
		},
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}},
		[]*Pane{pane},
	)

	d.refreshPluginStateFromHooks()

	if got := pane.PluginState["session_id"]; got != rotated {
		t.Errorf("session_id = %q, want the hook-recorded %q", got, rotated)
	}
	if got, ok := pane.PluginState["resume_session_id"]; ok {
		t.Errorf("resume_session_id = %q, want it removed once the hook confirmed a session", got)
	}
}

// TestBeginClaudeSessionsScan_SingleFlight: one listing reads up to 200
// transcript heads, far more work than parsing the frame that asked for it, so
// a client looping on this message type must not be able to stack workers.
//
// Drives beginClaudeSessionsScan rather than handleClaudeSessionsReq because
// ipc.Conn has no exported constructor, so the handler wrapper cannot be called
// from this package. The wrapper is three lines around this function; the
// decision is here.
func TestBeginClaudeSessionsScan_SingleFlight(t *testing.T) {
	d := newTestDaemon(t)
	msg := sessionsReq(t, "/proj")

	// First caller claims the slot and gets no rejection payload.
	rejection, ok := d.beginClaudeSessionsScan(msg)
	if !ok {
		t.Fatal("first scan must claim the slot")
	}
	if rejection.Error != "" {
		t.Errorf("first scan returned a rejection %+v, want none", rejection)
	}

	// Second caller is refused while the first is still in flight.
	rejection, ok = d.beginClaudeSessionsScan(msg)
	if ok {
		t.Fatal("a second concurrent scan must be refused")
	}
	if rejection.Error == "" {
		t.Error("refusal must carry an Error the field can display")
	}
	// The rejection has to echo the CWD, or the TUI drops it as stale and sits
	// on "Scanning…" until its own timeout instead of showing the reason.
	if rejection.CWD != "/proj" {
		t.Errorf("rejection CWD = %q, want /proj echoed", rejection.CWD)
	}

	// Releasing makes the slot reusable — otherwise one scan would disable the
	// picker for the rest of the daemon's life.
	d.sessionScanning.Store(false)
	if _, ok := d.beginClaudeSessionsScan(msg); !ok {
		t.Error("slot must be reusable once the in-flight scan releases it")
	}
	d.sessionScanning.Store(false)
}

// TestSpawnPane_SeedsSessionIDFromResumeTarget covers the seeding branch end to
// end at the spawnPane level: a resume target must become the pane's session_id
// rather than having a fresh UUID minted alongside it, or every downstream
// consumer (restore probe, model/context readout, hook reconciliation) would be
// looking at an id that never existed.
func TestSpawnPane_SeedsSessionIDFromResumeTarget(t *testing.T) {
	resumeID := "2db05609-f1d5-4576-b5b2-ff114519726b"
	d := newTestDaemon(t)
	withClaudePlugin(t, d)
	stubHookSessionID(t, nil) // no hook file yet — the pane has just been created

	pane := &Pane{
		ID:          "pane-0000000a",
		Type:        "claude-code",
		PluginState: map[string]string{"resume_session_id": resumeID},
	}
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	if got := pane.PluginState["session_id"]; got != resumeID {
		t.Errorf("session_id = %q, want the resume target %q (not a minted UUID)", got, resumeID)
	}
}

// TestSpawnPane_RestartPrefersHookSessionOverStaleResumeTarget: Alt+R spawns
// with restoring=false, so it takes the same branch as creation. A pane created
// to resume session A, whose conversation later rotated to B via /clear, must
// restart into B — replaying A would silently abandon the live conversation.
func TestSpawnPane_RestartPrefersHookSessionOverStaleResumeTarget(t *testing.T) {
	original := "2db05609-f1d5-4576-b5b2-ff114519726b"
	rotated := "9f8e7d6c-1a2b-3c4d-5e6f-0a1b2c3d4e5f"
	d := newTestDaemon(t)
	withClaudePlugin(t, d)
	stubHookSessionID(t, map[string]string{"pane-0000000a": rotated})

	pane := &Pane{
		ID:   "pane-0000000a",
		Type: "claude-code",
		PluginState: map[string]string{
			"session_id":        original,
			"resume_session_id": original,
		},
	}
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	if got := pane.PluginState["session_id"]; got != rotated {
		t.Errorf("session_id = %q, want the hook-recorded %q", got, rotated)
	}
	if got, ok := pane.PluginState["resume_session_id"]; ok {
		t.Errorf("resume_session_id = %q, want it retired once the pane has its own session", got)
	}
}

// NOTE: handleCreatePane and handleReplacePane both call applyResumeSessionID,
// but neither is covered end to end here — both construct their PTY through
// apty.New() rather than the newSessionFn seam, so driving them would spawn a
// real process. The same gap exists for every other handler in this package.
// applyResumeSessionID's own behaviour is pinned by the tests above; what is
// untested is only that the two call sites keep calling it.

// claudeResumePlugin mirrors the shipped claude-code plugin's spawn config.
func claudeResumePlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{Cmd: "claude"},
		Persistence: plugin.PersistenceConfig{
			Strategy:   "preassign_id",
			StartArgs:  []string{"--session-id", "{session_id}"},
			ResumeArgs: []string{"--continue"},
		},
	}
}

// TestResolveSpawnArgs_FreshResume covers the branch that makes the picker
// work: --resume REPLACES --session-id rather than joining it. Passing both is
// not a valid claude invocation.
func TestResolveSpawnArgs_FreshResume(t *testing.T) {
	p := claudeResumePlugin()
	resumeID := "2db05609-f1d5-4576-b5b2-ff114519726b"

	tests := []struct {
		name     string
		pane     *Pane
		resumeID string
		want     []string
		notIn    string
	}{
		{
			name: "resume target replaces start args",
			pane: &Pane{ID: "pane-0000000a", PluginState: map[string]string{
				"session_id": resumeID,
			}},
			resumeID: resumeID,
			want:     []string{"--resume", resumeID},
			notIn:    "--session-id",
		},
		{
			name: "no resume target keeps preassign start args",
			pane: &Pane{ID: "pane-0000000a", PluginState: map[string]string{
				"session_id": "fresh-uuid",
			}},
			want:  []string{"--session-id", "fresh-uuid"},
			notIn: "--resume",
		},
		{
			name: "runtime toggles compose with resume",
			pane: &Pane{ID: "pane-0000000a",
				InstanceArgs: []string{"--dangerously-skip-permissions", "--chrome"},
				PluginState: map[string]string{
					"session_id": resumeID,
				}},
			resumeID: resumeID,
			want:     []string{"--dangerously-skip-permissions", "--chrome", "--resume", resumeID},
			notIn:    "--session-id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSpawnArgs(p, tt.pane, false, tt.resumeID)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
			for _, a := range got {
				if a == tt.notIn {
					t.Errorf("args contain %q, which must not appear: %v", tt.notIn, got)
				}
			}
		})
	}
}

// TestClaudeResumeTemplate_ResumeIDFallback covers the narrow restart window:
// a pane created to resume a chosen session, restarted before its SessionStart
// hook recorded an id and before claude wrote the transcript the probe looks
// for. Falling through to --continue would attach it to whatever session is
// most recent in the CWD — precisely the one the user did not choose.
func TestClaudeResumeTemplate_ResumeIDFallback(t *testing.T) {
	p := claudeResumePlugin()
	chosen := "2db05609-f1d5-4576-b5b2-ff114519726b"

	// No hook file, and no transcript on disk for any id.
	stubHookSessionID(t, nil)
	prevExists := claudeSessionExistsFn
	claudeSessionExistsFn = func(string, string) bool { return false }
	t.Cleanup(func() { claudeSessionExistsFn = prevExists })

	t.Run("falls back to the chosen resume id", func(t *testing.T) {
		pane := &Pane{ID: "pane-0000000a", CWD: `E:\proj`, PluginState: map[string]string{
			"session_id":        chosen,
			"resume_session_id": chosen,
		}}
		got := claudeResumeTemplate(p, pane)
		want := []string{"--resume", "{session_id}"}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("template = %v, want %v", got, want)
		}
		if pane.PluginState["session_id"] != chosen {
			t.Errorf("session_id = %q, want the chosen id %q so {session_id} expands correctly",
				pane.PluginState["session_id"], chosen)
		}
	})

	t.Run("no resume id still falls back to configured resume args", func(t *testing.T) {
		pane := &Pane{ID: "pane-0000000b", CWD: `E:\proj`, PluginState: map[string]string{
			"session_id": "some-preassigned-id",
		}}
		got := claudeResumeTemplate(p, pane)
		if strings.Join(got, " ") != "--continue" {
			t.Errorf("template = %v, want [--continue]", got)
		}
	})
}

// --- session detail --------------------------------------------------------

// stubSessionDetail swaps the deep-read seam for the duration of a test.
func stubSessionDetail(t *testing.T, fn func(cwd, id string) (claudesessions.Detail, error)) {
	t.Helper()
	prev := readClaudeSessionDetailFn
	readClaudeSessionDetailFn = func(_ context.Context, cwd, id string) (claudesessions.Detail, error) {
		return fn(cwd, id)
	}
	t.Cleanup(func() { readClaudeSessionDetailFn = prev })
}

func sessionDetailReq(t *testing.T, cwd, id string) *ipc.Message {
	t.Helper()
	msg, err := ipc.NewMessage(ipc.MsgClaudeSessionDetailReq,
		ipc.ClaudeSessionDetailReqPayload{CWD: cwd, SessionID: id})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

const testSessionUUID = "2db05609-f1d5-4576-b2a1-9e0c3a7f1188"

func TestClaudeSessionDetailResponse_ReturnsSummary(t *testing.T) {
	started := time.Now().Add(-48 * time.Hour)
	modified := time.Now().Add(-30 * time.Minute)
	stubSessionDetail(t, func(cwd, id string) (claudesessions.Detail, error) {
		return claudesessions.Detail{
			ID:          id,
			FirstPrompt: "first",
			LastPrompt:  "last",
			UserPrompts: 47,
			Started:     started,
			Modified:    modified,
			SizeBytes:   1800000,
		}, nil
	})

	resp := claudeSessionDetailResponse(sessionDetailReq(t, "/proj", testSessionUUID))
	if resp.Error != "" {
		t.Fatalf("unexpected error: %q", resp.Error)
	}
	if resp.FirstPrompt != "first" || resp.LastPrompt != "last" {
		t.Errorf("prompts = %q / %q, want first / last", resp.FirstPrompt, resp.LastPrompt)
	}
	if resp.UserPrompts != 47 || resp.SizeBytes != 1800000 {
		t.Errorf("UserPrompts = %d, SizeBytes = %d", resp.UserPrompts, resp.SizeBytes)
	}
	if resp.StartedMs != started.UnixMilli() || resp.ModifiedMs != modified.UnixMilli() {
		t.Errorf("timestamps not carried: started=%d modified=%d", resp.StartedMs, resp.ModifiedMs)
	}
}

// TestClaudeSessionDetailResponse_EchoesKeyVerbatim: the TUI matches responses
// on BOTH echoed keys to decide whether the answer belongs to the row the
// cursor is on. Any normalization here would make a legitimate request look
// permanently stale and hang the panel on "Reading…".
func TestClaudeSessionDetailResponse_EchoesKeyVerbatim(t *testing.T) {
	stubSessionDetail(t, func(cwd, id string) (claudesessions.Detail, error) {
		return claudesessions.Detail{ID: id}, nil
	})
	const raw = `C:\Projects\Quil\..\Quil` // deliberately un-cleaned

	resp := claudeSessionDetailResponse(sessionDetailReq(t, raw, testSessionUUID))
	if resp.CWD != raw {
		t.Errorf("CWD = %q, want the request echoed verbatim (%q)", resp.CWD, raw)
	}
	if resp.SessionID != testSessionUUID {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, testSessionUUID)
	}
}

// TestClaudeSessionDetailResponse_RejectsMalformedSessionID is the security
// case: the id becomes a filename component, and any process inside the 0600
// socket can send one. A traversal attempt must never reach the filesystem.
func TestClaudeSessionDetailResponse_RejectsMalformedSessionID(t *testing.T) {
	reached := false
	stubSessionDetail(t, func(cwd, id string) (claudesessions.Detail, error) {
		reached = true
		return claudesessions.Detail{}, nil
	})

	for _, id := range []string{
		"../../../etc/passwd",
		"..",
		`..\..\Windows\System32\config\SAM`,
		"not-a-uuid",
		"2db05609-f1d5-4576-b2a1-9e0c3a7f1188.jsonl",
		"--dangerously-skip-permissions",
		strings.Repeat("a", 200),
	} {
		resp := claudeSessionDetailResponse(sessionDetailReq(t, "/proj", id))
		if resp.Error == "" {
			t.Errorf("session id %q was accepted", id)
		}
		// The rejection still identifies the row, or the TUI drops it as stale.
		if resp.SessionID != id {
			t.Errorf("rejection for %q echoed SessionID %q", id, resp.SessionID)
		}
	}
	if reached {
		t.Error("a malformed session id reached the filesystem read")
	}
}

func TestClaudeSessionDetailResponse_EmptyRequestIsRefused(t *testing.T) {
	stubSessionDetail(t, func(cwd, id string) (claudesessions.Detail, error) {
		t.Error("read must not be attempted for an empty request")
		return claudesessions.Detail{}, nil
	})
	for _, tc := range []struct{ cwd, id string }{{"", testSessionUUID}, {"/proj", ""}} {
		if resp := claudeSessionDetailResponse(sessionDetailReq(t, tc.cwd, tc.id)); resp.Error == "" {
			t.Errorf("cwd=%q id=%q was accepted", tc.cwd, tc.id)
		}
	}
}

func TestClaudeSessionDetailResponse_ReadFailureIsReported(t *testing.T) {
	stubSessionDetail(t, func(cwd, id string) (claudesessions.Detail, error) {
		return claudesessions.Detail{}, errors.New("permission denied")
	})

	resp := claudeSessionDetailResponse(sessionDetailReq(t, "/proj", testSessionUUID))
	if resp.Error == "" {
		t.Fatal("a read failure must be reported so the panel can say so")
	}
	// The daemon-side error text is logged, not forwarded: it can name paths.
	if strings.Contains(resp.Error, "permission denied") {
		t.Errorf("Error = %q, want a message that does not forward the raw error", resp.Error)
	}
}

func TestBeginClaudeSessionDetailRead_SingleFlight(t *testing.T) {
	d := newTestDaemon(t)
	msg := sessionDetailReq(t, "/proj", testSessionUUID)

	rejection, ok := d.beginClaudeSessionDetailRead(msg)
	if !ok {
		t.Fatal("first read must claim the slot")
	}
	if rejection.Error != "" {
		t.Errorf("first read returned a rejection %+v, want none", rejection)
	}

	rejection, ok = d.beginClaudeSessionDetailRead(msg)
	if ok {
		t.Fatal("a second concurrent read must be refused")
	}
	if rejection.Error == "" {
		t.Error("refusal must carry an Error the panel can display")
	}
	if rejection.CWD != "/proj" || rejection.SessionID != testSessionUUID {
		t.Errorf("rejection must echo the key, got cwd=%q id=%q", rejection.CWD, rejection.SessionID)
	}

	d.sessionDetailReading.Store(false)
	if _, ok := d.beginClaudeSessionDetailRead(msg); !ok {
		t.Error("slot must be reusable once the in-flight read releases it")
	}
	d.sessionDetailReading.Store(false)
}

// TestSessionDetailAndListingAreIndependent pins why the two single-flight
// guards are separate atomics: sharing one would make opening the info panel
// fail whenever a directory listing happened to be in flight, which is exactly
// when the user opens it.
func TestSessionDetailAndListingAreIndependent(t *testing.T) {
	d := newTestDaemon(t)

	if _, ok := d.beginClaudeSessionsScan(sessionsReq(t, "/proj")); !ok {
		t.Fatal("listing must claim its slot")
	}
	if _, ok := d.beginClaudeSessionDetailRead(sessionDetailReq(t, "/proj", testSessionUUID)); !ok {
		t.Error("a detail read must not be blocked by an in-flight listing")
	}
	d.sessionScanning.Store(false)
	d.sessionDetailReading.Store(false)
}

// The stub helpers above adapt a ctx-free function onto the ctx-aware seam,
// which is convenient but blind: every test that uses them would still pass if
// the handler stopped deriving a bounded context and passed a bare
// context.Background() instead.
//
// That is the exact regression RD-004 exists to prevent. These two tests bypass
// the adapters and assign the seam directly so the context the handler actually
// builds can be inspected. Both handlers hold a single-flight slot for the life
// of their call, so an unbounded scan does not stall one request — it makes the
// feature answer "already running" until the daemon is restarted.
func TestClaudeSessionsResponse_PassesABoundedContext(t *testing.T) {
	d := newTestDaemon(t)

	prev := listClaudeSessionsFn
	t.Cleanup(func() { listClaudeSessionsFn = prev })

	var gotCtx context.Context
	listClaudeSessionsFn = func(ctx context.Context, _ string) ([]claudesessions.Session, bool, error) {
		gotCtx = ctx
		return nil, false, nil
	}

	d.claudeSessionsResponse(sessionsReq(t, t.TempDir()))

	if gotCtx == nil {
		t.Fatal("handler never called the discovery seam")
	}
	deadline, ok := gotCtx.Deadline()
	if !ok {
		t.Fatal("discovery ran on a context with no deadline; the scan is unbounded " +
			"and holds the single-flight slot for as long as it takes")
	}
	if d := time.Until(deadline); d <= 0 || d > discoveryTimeout {
		t.Errorf("deadline is %v away, want (0, %v]", d, discoveryTimeout)
	}
}

func TestClaudeSessionDetailResponse_PassesABoundedContext(t *testing.T) {
	prev := readClaudeSessionDetailFn
	t.Cleanup(func() { readClaudeSessionDetailFn = prev })

	var gotCtx context.Context
	readClaudeSessionDetailFn = func(ctx context.Context, _, _ string) (claudesessions.Detail, error) {
		gotCtx = ctx
		return claudesessions.Detail{}, nil
	}

	claudeSessionDetailResponse(sessionDetailReq(t, t.TempDir(), "aaaaaaaa-0000-4000-8000-000000000000"))

	if gotCtx == nil {
		t.Fatal("handler never called the detail seam")
	}
	deadline, ok := gotCtx.Deadline()
	if !ok {
		t.Fatal("detail read ran on a context with no deadline; it streams a whole " +
			"transcript and holds sessionDetailReading while it does")
	}
	if d := time.Until(deadline); d <= 0 || d > discoveryTimeout {
		t.Errorf("deadline is %v away, want (0, %v]", d, discoveryTimeout)
	}
}
