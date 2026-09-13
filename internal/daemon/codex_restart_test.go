package daemon

import (
	"os"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/codexhook"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

const restartTestSessionID = "01a05db1-9f44-73b2-b426-8aad5f5232f4"

// TestHandleRestartPaneReq_CodexResumesItsRecordedSession drives the CALL SITE,
// and that is the whole point of it existing beside the resolveSpawnArgs table
// below.
//
// The bug was not in how a template becomes argv — that was always right. It
// was that restart reaches spawnPane with restoring=false, where only
// preassign_id was handled, so a codex pane was respawned with no resume
// argument at all and silently started a new conversation. Every helper on the
// path had a green test. A test that calls resolveSpawnArgs directly can be
// made to pass by a fix that the restart handler never reaches.
//
// Observed 2026-09-11 in a production daemon log: two codex panes restarted a
// minute apart, both `spawn: … restoring=false` with no `resume`, while
// $QUIL_HOME/sessions/codex-<paneID>.id sat on disk holding the id each had
// just been resumed with hours earlier.
func TestHandleRestartPaneReq_CodexResumesItsRecordedSession(t *testing.T) {
	origExe, origRead := quildExeFn, readCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: restartTestSessionID}, nil
	}
	t.Cleanup(func() { quildExeFn, readCodexSessionFn = origExe, origRead })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	// AFTER newTestDaemon: newTestDaemonInDir installs its own newSessionFn and
	// restores the previous one in a Cleanup, so an override set before it is
	// silently replaced and every assertion below reads an untouched fake.
	origNew := newSessionFn
	fake := &fakeSession{}
	newSessionFn = func(cols, rows int) apty.Session { return fake }
	t.Cleanup(func() { newSessionFn = origNew })

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "codex"

	// The pane has to have RUN before it can be restarted, and saying so with a
	// real spawn is the point: ranBefore is pane.ptyGen, which only a spawn
	// moves. CreatePane alone makes a pane record with no child, which is a
	// state no restart reaches in production — a Pending pane is spawned by
	// handleRestartPaneReq's own ensurePaneSpawned first.
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("initial spawn: %v", err)
	}

	msg, err := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleRestartPaneReq(nil, msg)

	if !fake.started {
		t.Fatal("restart did not spawn a child at all")
	}
	if !containsPair(fake.startArgs, "resume", restartTestSessionID) {
		t.Errorf("restart argv = %q\nwant it to carry `resume %s` — without it the pane abandons a live conversation and there is no way back to it",
			fake.startArgs, restartTestSessionID)
	}
}

// TestHandleRestartPaneReq_CodexWithNoRecordStartsFresh pins the OTHER
// direction, because the fix must not invent a session.
//
// codex.toml leaves resume_args empty on purpose: `resume --last` is codex's
// most-recent-session-in-CWD lookup, which on a multi-pane workspace finds a
// SIBLING pane's conversation. A restart with nothing recorded must therefore
// start clean rather than reach for the nearest transcript.
func TestHandleRestartPaneReq_CodexWithNoRecordStartsFresh(t *testing.T) {
	origExe, origRead := quildExeFn, readCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{}, nil
	}
	t.Cleanup(func() { quildExeFn, readCodexSessionFn = origExe, origRead })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	// AFTER newTestDaemon: newTestDaemonInDir installs its own newSessionFn and
	// restores the previous one in a Cleanup, so an override set before it is
	// silently replaced and every assertion below reads an untouched fake.
	origNew := newSessionFn
	fake := &fakeSession{}
	newSessionFn = func(cols, rows int) apty.Session { return fake }
	t.Cleanup(func() { newSessionFn = origNew })

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "codex"

	msg, err := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleRestartPaneReq(nil, msg)

	for _, a := range fake.startArgs {
		if a == "resume" || a == "--last" {
			t.Fatalf("restart argv = %q, want no resume with nothing recorded", fake.startArgs)
		}
	}
}

// TestResolveSpawnArgs_RestartResumesSessionScrape is the arg-merging matrix for
// the restart branch. Restore is covered by TestResolveSpawnArgs_CodexResume;
// this one fixes restoring=false, which is what Alt+R and the MCP restart_pane
// tool both pass.
func TestResolveSpawnArgs_RestartResumesSessionScrape(t *testing.T) {
	codexPlugin := &plugin.PanePlugin{
		Name:        plugin.CodexPluginName,
		Command:     plugin.CommandConfig{Cmd: "codex"},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape", ResumeArgs: nil},
	}

	tests := []struct {
		name string
		pane *Pane
		rec  codexhook.SessionRecord
		want []string
	}{
		{
			"recorded id — restart rejoins it",
			&Pane{ID: "pane-abc"},
			codexhook.SessionRecord{ID: restartTestSessionID},
			[]string{"resume", restartTestSessionID},
		},
		{
			// The toggles the pane was created with are on InstanceArgs and must
			// survive a restart exactly as they survive a daemon restore —
			// otherwise Alt+R silently drops --search or the approval mode and
			// the pane comes back with different permissions than it had.
			"runtime toggles survive the restart",
			&Pane{ID: "pane-abc", InstanceArgs: []string{"--search"}},
			codexhook.SessionRecord{ID: restartTestSessionID},
			[]string{"--search", "resume", restartTestSessionID},
		},
		{
			"nothing recorded — restart starts fresh",
			&Pane{ID: "pane-abc"},
			codexhook.SessionRecord{},
			nil,
		},
		{
			// Shape validation is upstream in codexResumeTemplate, but it has to
			// hold on THIS branch too: a flag-shaped id reaching argv would make
			// the restart run `codex resume --last`, which is the sibling-session
			// trap the empty fallback exists to avoid.
			"flag-shaped id — restart starts fresh",
			&Pane{ID: "pane-abc"},
			codexhook.SessionRecord{ID: "--last"},
			nil,
		},
	}

	orig := readCodexSessionFn
	t.Cleanup(func() { readCodexSessionFn = orig })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readCodexSessionFn = func(string) (codexhook.SessionRecord, error) { return tt.rec, nil }
			got := resolveSpawnArgs(codexPlugin, tt.pane, false, true, "", claimAny)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs(restoring=false):\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestResolveSpawnArgs_RestartLeavesOtherStrategiesAlone keeps the new branch
// from widening past session_scrape.
//
// preassign_id has its OWN restart handling a few lines above — it reads the
// pane's current record and emits `--resume <id>`, and reaching this branch as
// well would append a second resume to the same argv. cwd_only, rerun and none
// never resume at all, on restore or restart.
func TestResolveSpawnArgs_RestartLeavesOtherStrategiesAlone(t *testing.T) {
	orig := readCodexSessionFn
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: restartTestSessionID}, nil
	}
	t.Cleanup(func() { readCodexSessionFn = orig })

	for _, strategy := range []string{"cwd_only", "rerun", "none", ""} {
		t.Run(strategy, func(t *testing.T) {
			p := &plugin.PanePlugin{
				Name:    plugin.CodexPluginName,
				Command: plugin.CommandConfig{Cmd: "codex"},
				Persistence: plugin.PersistenceConfig{
					Strategy:   strategy,
					ResumeArgs: []string{"resume", "{session_id}"},
				},
			}
			got := resolveSpawnArgs(p, &Pane{ID: "pane-abc"}, false, true, "", claimAny)
			if len(got) != 0 {
				t.Errorf("strategy %q restart argv = %v, want none", strategy, got)
			}
		})
	}
}

func containsPair(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}


// TestResolveSpawnArgs_FreshPaneIgnoresALeftoverRecord is the guard on the
// restart branch, and it protects against a WORSE bug than the one that branch
// fixes.
//
// Eight call sites reach spawnPane with restoring=false, and only one of them
// is a restart: pane creation, both replace paths, the tab-bootstrap spawns and
// the sandbox sign-in respawn arrive the same way. Pane ids are 32 bits and
// nothing ever deletes $QUIL_HOME/sessions/codex-<paneID>.id, so a freshly
// minted id can collide with a destroyed pane's leftover — and an ungated
// branch would have the new pane silently open a stranger's conversation.
//
// ranBefore (pane.ptyGen > 0) is what separates the two, and it had to be a new
// signal rather than preassign_id's `hadSession`: that reads
// PluginState["session_id"], whose only writer for a session_scrape pane is
// refreshPluginStateFromHooks, which runs at SHUTDOWN — so a codex pane created
// and used within one daemon lifetime still has it empty, and reusing it would
// skip the resume in exactly the case the fix exists for.
func TestResolveSpawnArgs_FreshPaneIgnoresALeftoverRecord(t *testing.T) {
	orig := readCodexSessionFn
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		// A perfectly valid record — the point is that it is not THIS pane's.
		return codexhook.SessionRecord{ID: restartTestSessionID}, nil
	}
	t.Cleanup(func() { readCodexSessionFn = orig })

	p := &plugin.PanePlugin{
		Name:        plugin.CodexPluginName,
		Command:     plugin.CommandConfig{Cmd: "codex"},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape"},
	}

	got := resolveSpawnArgs(p, &Pane{ID: "pane-abc"}, false, false, "", claimAny)
	if len(got) != 0 {
		t.Errorf("a pane whose child has never run must ignore the record under its id, got %v — "+
			"nothing deletes those files, so it can only belong to a destroyed pane that drew the same id", got)
	}

	// The control: the SAME record, the same pane, one bit different. Without
	// it a fix that simply stopped resuming would pass the assertion above.
	if got := resolveSpawnArgs(p, &Pane{ID: "pane-abc"}, false, true, "", claimAny); len(got) == 0 {
		t.Error("a pane that HAS run must still resume its own recorded session")
	}
}

// TestSpawnPane_FreshCodexPaneDoesNotAdoptALeftoverRecord drives the same guard
// through spawnPane, because ranBefore is computed THERE — from pane.ptyGen,
// under PluginMu, before the counter is incremented for this spawn. A table
// test passing the bool directly cannot catch the counter being read after its
// own increment, which would make every create look like a restart.
func TestSpawnPane_FreshCodexPaneDoesNotAdoptALeftoverRecord(t *testing.T) {
	origExe, origRead := quildExeFn, readCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: restartTestSessionID}, nil
	}
	t.Cleanup(func() { quildExeFn, readCodexSessionFn = origExe, origRead })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	fake := &fakeSession{}
	// freshID: this models a pane whose id was MINTED here. The restore path's
	// literal leaves it false, which is a different pane with different rights
	// over the record under its id — see the restored-pane test below.
	pane := &Pane{ID: "pane-c0dec0de", Type: "codex", CWD: t.TempDir(), freshID: true}
	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	if containsPair(fake.startArgs, "resume", restartTestSessionID) {
		t.Fatalf("a brand-new codex pane resumed a record it did not write: %q", fake.startArgs)
	}

	// That spawn incremented ptyGen, so the NEXT one is a restart by the same
	// rule — and must resume. Asserting both against one pane is what pins the
	// counter's ordering rather than merely its value.
	second := &fakeSession{}
	if err := d.spawnPane(pane, second, false); err != nil {
		t.Fatalf("spawnPane (restart): %v", err)
	}
	if !containsPair(second.startArgs, "resume", restartTestSessionID) {
		t.Errorf("the second spawn of the same pane is a restart and must resume: %q", second.startArgs)
	}
}

// TestSpawnPane_FreshPaneRetiresAStaleRecordSoALaterRestartCannotFindIt closes
// the gap ranBefore alone leaves open.
//
// ranBefore says a child of this pane has RUN, which is not the same as saying
// this pane OWNS the record under its id. The difference is reachable: a fresh
// spawn correctly ignores a leftover record but still increments ptyGen, so a
// child that dies before its SessionStart hook fires (a crash, a missing
// binary, a login prompt the user closes) leaves the next restart looking at a
// record the pane never wrote — a destroyed pane's conversation, opened in
// somebody else's tab.
//
// The fresh spawn therefore DELETES the record first, which makes the invariant
// true rather than merely unlikely: after it, any record under this id was
// written by this pane's own child. Safe by the uniqueness that creates the
// hazard — ids are unique among LIVE panes, so a record under a pane that has
// never run can only belong to one that no longer exists.
func TestSpawnPane_FreshPaneRetiresAStaleRecordSoALaterRestartCannotFindIt(t *testing.T) {
	origExe, origRead, origRemove := quildExeFn, readCodexSessionFn, removeCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }

	// A leftover record from a destroyed pane that once held this id. The fake
	// store is what lets the test assert the DELETE rather than trust it.
	store := map[string]string{"pane-c0dec0de": restartTestSessionID}
	readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
		id, ok := store[paneID]
		if !ok {
			return codexhook.SessionRecord{}, os.ErrNotExist
		}
		return codexhook.SessionRecord{ID: id}, nil
	}
	removeCodexSessionFn = func(paneID string) error {
		delete(store, paneID)
		return nil
	}
	t.Cleanup(func() {
		quildExeFn, readCodexSessionFn, removeCodexSessionFn = origExe, origRead, origRemove
	})

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	// freshID: this models a pane whose id was MINTED here. The restore path's
	// literal leaves it false, which is a different pane with different rights
	// over the record under its id — see the restored-pane test below.
	pane := &Pane{ID: "pane-c0dec0de", Type: "codex", CWD: t.TempDir(), freshID: true}
	if err := d.spawnPane(pane, &fakeSession{}, false); err != nil {
		t.Fatalf("fresh spawn: %v", err)
	}
	if _, still := store["pane-c0dec0de"]; still {
		t.Fatal("the fresh spawn left the stale record in place — a later restart will read it")
	}

	// The child died without writing its own record. ranBefore is now true, so
	// the OLD gate would resume here; with the record retired there is nothing
	// to resume and the pane correctly starts clean.
	restarted := &fakeSession{}
	if err := d.spawnPane(pane, restarted, false); err != nil {
		t.Fatalf("restart spawn: %v", err)
	}
	if containsPair(restarted.startArgs, "resume", restartTestSessionID) {
		t.Errorf("restart resumed a record this pane never wrote: %q", restarted.startArgs)
	}
}

// The control: a fresh spawn must NOT delete a record on the restore path, and
// must not delete the one its own child goes on to write.
//
// Deleting on restore would throw away exactly the conversation this feature
// exists to keep — a restored pane's record IS its own, since the id comes from
// the snapshot rather than being newly minted.
func TestSpawnPane_RestoreDoesNotRetireTheRecordItIsAboutToResume(t *testing.T) {
	origExe, origRead, origRemove := quildExeFn, readCodexSessionFn, removeCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }

	store := map[string]string{"pane-c0dec0de": restartTestSessionID}
	readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
		id, ok := store[paneID]
		if !ok {
			return codexhook.SessionRecord{}, os.ErrNotExist
		}
		return codexhook.SessionRecord{ID: id}, nil
	}
	removeCodexSessionFn = func(paneID string) error {
		delete(store, paneID)
		return nil
	}
	t.Cleanup(func() {
		quildExeFn, readCodexSessionFn, removeCodexSessionFn = origExe, origRead, origRemove
	})

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	fake := &fakeSession{}
	pane := &Pane{ID: "pane-c0dec0de", Type: "codex", CWD: t.TempDir()}
	if err := d.spawnPane(pane, fake, true); err != nil {
		t.Fatalf("restore spawn: %v", err)
	}
	if _, still := store["pane-c0dec0de"]; !still {
		t.Error("the restore path deleted the record it was restoring from")
	}
	if !containsPair(fake.startArgs, "resume", restartTestSessionID) {
		t.Errorf("restore did not resume: %q", fake.startArgs)
	}
}

// TestSpawnPane_RestoredPaneRetryingItsFirstSpawnKeepsItsRecord is the
// regression test for the review finding that ptyGen alone is the wrong signal.
//
// A restored codex pane whose worktree is temporarily gone never spawns:
// spawnRestoredPane returns early on refuseMissingWorktree, and
// ensurePaneSpawned clears Pending anyway. That leaves a pane with ptyGen 0
// holding a session record that is entirely its own — the id came off the
// snapshot, so the record was written by this same pane in an earlier daemon.
//
// Alt+R once the directory is back arrives with restoring=false and ptyGen 0.
// Under a ptyGen-only gate that read as "brand new": the resume was skipped AND
// the retire deleted the record, so the conversation was destroyed rather than
// merely not reopened. freshID is what tells the two apart — a restored pane's
// id was never minted here.
func TestSpawnPane_RestoredPaneRetryingItsFirstSpawnKeepsItsRecord(t *testing.T) {
	origExe, origRead, origRemove := quildExeFn, readCodexSessionFn, removeCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }

	store := map[string]string{"pane-c0dec0de": restartTestSessionID}
	readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
		id, ok := store[paneID]
		if !ok {
			return codexhook.SessionRecord{}, os.ErrNotExist
		}
		return codexhook.SessionRecord{ID: id}, nil
	}
	removeCodexSessionFn = func(paneID string) error {
		delete(store, paneID)
		return nil
	}
	t.Cleanup(func() {
		quildExeFn, readCodexSessionFn, removeCodexSessionFn = origExe, origRead, origRemove
	})

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	// A pane as restoreWorkspace builds one: the id comes from the snapshot, so
	// freshID is false. ptyGen is 0 because its lazy spawn was refused.
	pane := &Pane{ID: "pane-c0dec0de", Type: "codex", CWD: t.TempDir()}

	fake := &fakeSession{}
	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("retry spawn: %v", err)
	}
	if _, still := store["pane-c0dec0de"]; !still {
		t.Error("a restored pane retrying its first spawn had its own session record DELETED")
	}
	if !containsPair(fake.startArgs, "resume", restartTestSessionID) {
		t.Errorf("a restored pane retrying its first spawn must resume its own session, got %q", fake.startArgs)
	}
}

// The control for the test above, and the reason freshID cannot simply be
// assumed: a pane whose id was MINTED here, with a record under it, must still
// ignore and retire that record. Without this a "always treat ptyGen 0 as
// owning" fix would pass the restored-pane test and reopen the original P1.
func TestSpawnPane_MintedPaneStillRetiresALeftoverRecord(t *testing.T) {
	origExe, origRead, origRemove := quildExeFn, readCodexSessionFn, removeCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "codex"

	store := map[string]string{pane.ID: restartTestSessionID}
	readCodexSessionFn = func(paneID string) (codexhook.SessionRecord, error) {
		id, ok := store[paneID]
		if !ok {
			return codexhook.SessionRecord{}, os.ErrNotExist
		}
		return codexhook.SessionRecord{ID: id}, nil
	}
	removeCodexSessionFn = func(paneID string) error {
		delete(store, paneID)
		return nil
	}
	t.Cleanup(func() {
		quildExeFn, readCodexSessionFn, removeCodexSessionFn = origExe, origRead, origRemove
	})

	fake := &fakeSession{}
	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("fresh spawn: %v", err)
	}
	if containsPair(fake.startArgs, "resume", restartTestSessionID) {
		t.Errorf("a minted pane resumed a record it did not write: %q", fake.startArgs)
	}
	if _, still := store[pane.ID]; still {
		t.Error("a minted pane left a stranger's record in place for a later restart to read")
	}
}

// CreatePane and NewPane are the only two sites that invent a pane id, and
// freshID has to be set at BOTH — NewPane feeds ReplacePane, which is one of
// the eight restoring=false spawn paths. A pane built by the restore path must
// NOT be marked fresh, which is the half that carries the data-loss risk.
func TestPaneFreshID_SetByBothMintersAndNotByTheRestoreLiteral(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")

	created, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	if !created.freshID {
		t.Error("CreatePane must mark the pane fresh — its id has just been invented")
	}
	if replacement := d.session.NewPane(t.TempDir()); !replacement.freshID {
		t.Error("NewPane must mark the pane fresh — ReplacePane is a restoring=false spawn path")
	}
	// The restore path builds its Pane literal directly; this is the shape it
	// produces, and it must never look minted.
	if restored := (&Pane{ID: "pane-fromdisk"}); restored.freshID {
		t.Error("a pane built from a snapshot must not be marked fresh")
	}
}
