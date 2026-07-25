package daemon

import (
	"errors"
	"strings"
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
	listClaudeSessionsFn = fn
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

// TestHandleClaudeSessionsReq_SingleFlight: one listing reads up to 200
// transcript heads, far more work than parsing the frame that asked for it, so
// a client looping on this message type must not be able to stack workers.
func TestHandleClaudeSessionsReq_SingleFlight(t *testing.T) {
	d := newTestDaemon(t)
	if !d.sessionScanning.CompareAndSwap(false, true) {
		t.Fatal("guard should start unset")
	}
	t.Cleanup(func() { d.sessionScanning.Store(false) })

	// With a scan already in flight the handler must answer immediately rather
	// than spawning a second worker — and the rejection still has to echo the
	// CWD, or the TUI drops it as stale and waits out its timeout instead of
	// showing the reason.
	called := false
	stubSessionList(t, func(string) ([]claudesessions.Session, bool, error) {
		called = true
		return nil, false, nil
	})

	msg := sessionsReq(t, "/proj")
	if got := claudeSessionsReqCWD(msg); got != "/proj" {
		t.Errorf("claudeSessionsReqCWD = %q, want /proj", got)
	}
	if called {
		t.Error("no scan should have run")
	}
}

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
