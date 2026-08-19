package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
)

// seedPaneArtifacts creates the on-disk files cleanupPaneArtifacts must remove.
func seedPaneArtifacts(t *testing.T, paneID string) (spoolFile, sessFile string) {
	t.Helper()
	if err := os.MkdirAll(config.EventsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.SessionsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	spoolFile = filepath.Join(config.EventsDir(), paneID+".jsonl")
	sessFile = filepath.Join(config.SessionsDir(), paneID+".id")
	if err := os.WriteFile(spoolFile, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessFile, []byte("sess"), 0600); err != nil {
		t.Fatal(err)
	}
	return spoolFile, sessFile
}

func assertGone(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after cleanup (err=%v)", p, err)
		}
	}
}

// TestHandleDestroyTab_CleansHookArtifacts: destroying a tab must clean every
// contained pane's hook artifacts, same as destroying the pane directly.
func TestHandleDestroyTab_CleansHookArtifacts(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	d.hookSpool = hookevents.NewSpool(config.EventsDir())
	d.hookIngester = hookevents.NewIngester(func(hookevents.Payload) {})

	// Two tabs: destroying the first tab leaves the second, avoiding the
	// auto-create-replacement path that spawns a real PTY.
	tab := d.session.CreateTab("Shell")
	d.session.CreateTab("Keep")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spoolFile, sessFile := seedPaneArtifacts(t, pane.ID)

	msg, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{TabID: tab.ID})
	d.handleDestroyTab(msg)

	assertGone(t, spoolFile, sessFile)
}

// TestHandleDestroyProject_CleansHookArtifacts: destroying a project destroys
// every tab and pane under it, so it is a pane-destruction path and owes the
// same cleanup as destroy-pane and destroy-tab. It is the newest of the three
// and was the one that forgot: the panes' PTYs were closed but their hook
// spool files, ingester coalescers and session-id files were left behind, so
// the daemon kept re-polling dead spools every 200 ms until restart.
func TestHandleDestroyProject_CleansHookArtifacts(t *testing.T) {
	d := newTestDaemon(t)
	d.hookSpool = hookevents.NewSpool(config.EventsDir())
	d.hookIngester = hookevents.NewIngester(func(hookevents.Payload) {})

	// Two projects, both with a tab: destroying the second leaves the first
	// populated, so recoverEmptyProject has nothing to bootstrap and the test
	// exercises the destroy path alone.
	keep := d.session.CreateProject("keep", t.TempDir())
	d.session.CreateTabInProject(keep.ID, "Keep")
	doomed := d.session.CreateProject("doomed", t.TempDir())
	tab := d.session.CreateTabInProject(doomed.ID, "Shell")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spoolFile, sessFile := seedPaneArtifacts(t, pane.ID)

	msg, _ := ipc.NewMessage(ipc.MsgDestroyProject, ipc.DestroyProjectPayload{ProjectID: doomed.ID})
	d.handleMessage(nil, msg)

	assertGone(t, spoolFile, sessFile)
}

// TestCleanupPaneArtifacts_RemovesAll covers the helper directly, including
// the opencode session-id variant.
func TestCleanupPaneArtifacts_RemovesAll(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	d.hookSpool = hookevents.NewSpool(config.EventsDir())
	d.hookIngester = hookevents.NewIngester(func(hookevents.Payload) {})

	spoolFile, sessFile := seedPaneArtifacts(t, "pane-abc12345")
	ocFile := filepath.Join(config.SessionsDir(), "opencode-pane-abc12345.id")
	if err := os.WriteFile(ocFile, []byte("oc"), 0600); err != nil {
		t.Fatal(err)
	}
	// The hook-settings file a claude-code spawn writes. Seeded here because
	// the removal list is a slice of filename strings: a typo or a dropped
	// entry leaves the file behind on every pane destroy with nothing failing.
	setFile := filepath.Join(config.SessionsDir(), "pane-abc12345.settings.json")
	if err := os.WriteFile(setFile, []byte(`{"hooks":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	d.cleanupPaneArtifacts("pane-abc12345")

	assertGone(t, spoolFile, sessFile, ocFile, setFile)
}
