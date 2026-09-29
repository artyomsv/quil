package daemon

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

// notesTestDaemon is mcpTestDaemon plus one live pane, and the socket so a
// second client can be dialled for the race test.
func notesTestDaemon(t *testing.T) (*Daemon, *ipc.Client, string, string) {
	t.Helper()
	d, sock := overlayServerDaemonWithConfig(t, config.Default())
	registerShippedPlugins(t, d)
	client, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	sendNoID(t, client, ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "bridge", PID: os.Getpid()})
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d, client, sock, pane.ID
}

// noteRevOf reads NoteRev the way the workspace-state build does: a bare
// Load(), no noteMu — the field is atomic precisely so this never waits
// behind a note_set holding noteMu across disk I/O.
func noteRevOf(d *Daemon, paneID string) uint64 {
	p := d.session.Pane(paneID)
	return p.NoteRev.Load()
}

// noteSetNoT sends a note_set and returns its response or an error, calling
// no *testing.T method — t.Fatal/t.Errorf are unsafe from any goroutine but
// the test's own, so the concurrent test below cannot use roundTrip/decodeInto
// (both call t.Fatalf) from its worker goroutines.
func noteSetNoT(client *ipc.Client, tag string, payload ipc.NoteSetPayload) (ipc.NoteSetRespPayload, error) {
	msg, err := ipc.NewMessage(ipc.MsgNoteSet, payload)
	if err != nil {
		return ipc.NoteSetRespPayload{}, err
	}
	msg.ID = "conc-" + tag + "-" + time.Now().Format("150405.000000000")
	if err := client.Send(msg); err != nil {
		return ipc.NoteSetRespPayload{}, err
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return ipc.NoteSetRespPayload{}, err
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		resp, err := client.Receive()
		if err != nil {
			return ipc.NoteSetRespPayload{}, err
		}
		if resp.Type == ipc.MsgNoteSetResp && resp.ID == msg.ID {
			var out ipc.NoteSetRespPayload
			if err := resp.DecodePayload(&out); err != nil {
				return ipc.NoteSetRespPayload{}, err
			}
			return out, nil
		}
	}
}

func TestHandleMessage_NoteGetSet_RoundTrip(t *testing.T) {
	_, client, _, paneID := notesTestDaemon(t)
	empty := decodeInto[ipc.NoteRespPayload](t, roundTrip(t, client, ipc.MsgNoteGet, ipc.MsgNoteResp, ipc.NoteGetPayload{PaneID: paneID}))
	if empty.Rev != 0 || empty.Text != "" {
		t.Fatalf("fresh pane note = %+v, want rev 0 and no text", empty)
	}
	set := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "hello\n", BaseRev: 0}))
	if !set.OK || set.Rev != 1 {
		t.Fatalf("note_set = %+v, want ok rev 1", set)
	}
	got := decodeInto[ipc.NoteRespPayload](t, roundTrip(t, client, ipc.MsgNoteGet, ipc.MsgNoteResp, ipc.NoteGetPayload{PaneID: paneID}))
	if got.Text != "hello\n" || got.Rev != 1 {
		t.Errorf("note_get = %+v", got)
	}
	onDisk, err := persist.LoadNotes(config.NotesDir(), paneID)
	if err != nil || onDisk != "hello\n" {
		t.Errorf("file = %q, %v", onDisk, err)
	}
}

func TestHandleMessage_NoteSet_StaleBaseIsConflictAndWritesNothing(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "one\n", BaseRev: 0})
	resp := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "stale\n", BaseRev: 0}))
	if resp.OK || !resp.Conflict || resp.CurrentRev != 1 {
		t.Fatalf("stale save = %+v, want conflict with current_rev 1", resp)
	}
	if onDisk, _ := persist.LoadNotes(config.NotesDir(), paneID); onDisk != "one\n" {
		t.Errorf("a refused save reached the disk: %q", onDisk)
	}
	if noteRevOf(d, paneID) != 1 {
		t.Errorf("rev moved on a refused save: %d", noteRevOf(d, paneID))
	}
}

// Review focus 2.
func TestHandleMessage_NoteSet_ConcurrentSameBase_ExactlyOneWins(t *testing.T) {
	d, a, sock, paneID := notesTestDaemon(t)
	b, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	sendNoID(t, b, ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "bridge", PID: os.Getpid()})
	var wg sync.WaitGroup
	type result struct {
		resp ipc.NoteSetRespPayload
		err  error
	}
	results := make([]result, 2)
	for i, c := range []*ipc.Client{a, b} {
		wg.Add(1)
		go func(i int, c *ipc.Client) {
			defer wg.Done()
			resp, err := noteSetNoT(c, string(rune('a'+i)),
				ipc.NoteSetPayload{PaneID: paneID, Text: "from " + string(rune('a'+i)) + "\n", BaseRev: 0})
			results[i] = result{resp, err}
		}(i, c)
	}
	wg.Wait()
	oks := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("goroutine %d: %v", i, r.err)
		}
		if r.resp.OK {
			oks++
		} else if !r.resp.Conflict || r.resp.CurrentRev != 1 {
			t.Errorf("loser = %+v, want conflict current_rev 1", r.resp)
		}
	}
	if oks != 1 {
		t.Fatalf("%d saves applied, want exactly 1: %+v", oks, results)
	}
	if noteRevOf(d, paneID) != 1 {
		t.Errorf("rev = %d, want 1", noteRevOf(d, paneID))
	}
}

func TestHandleMessage_NoteSet_OverCapRefused(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	big := strings.Repeat("x", ipc.MaxNoteBytes+1)
	resp := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: big}))
	if resp.OK || resp.Error == "" {
		t.Errorf("over-cap save = %+v", resp)
	}
	if noteRevOf(d, paneID) != 0 {
		t.Error("rev moved on a refused save")
	}
}

// Pins > vs >= at the cap boundary: exactly MaxNoteBytes must be accepted.
func TestHandleMessage_NoteSet_ExactlyMaxBytesAccepted(t *testing.T) {
	_, client, _, paneID := notesTestDaemon(t)
	exact := strings.Repeat("x", ipc.MaxNoteBytes)
	resp := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: exact, BaseRev: 0}))
	if !resp.OK || resp.Rev != 1 {
		t.Fatalf("exactly-max-size save = %+v, want ok rev 1", resp)
	}
}

// R-2: NoteRev is monotonic. A delete increments it like any other write, so
// a stale save based on the pre-delete rev is refused rather than silently
// accepted and overwriting whatever a concurrent writer put there afterward.
func TestHandleMessage_NoteSet_EmptyTextDeletesAndIncrementsRev(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "one\n", BaseRev: 0})
	resp := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "", BaseRev: 1}))
	if !resp.OK || resp.Rev != 2 {
		t.Fatalf("delete = %+v, want ok rev 2 (monotonic: previous rev + 1)", resp)
	}
	path, _ := persist.NotesPath(config.NotesDir(), paneID)
	if _, err := os.Lstat(path); err == nil {
		t.Error("note file still exists after an empty save")
	}
	if noteRevOf(d, paneID) != 2 {
		t.Errorf("rev = %d, want 2", noteRevOf(d, paneID))
	}

	// A save against the PRE-delete rev (1) must be refused: that rev has
	// already been invalidated by the delete, so accepting it would silently
	// resurrect text the deleting client had removed.
	stale := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "resurrected\n", BaseRev: 1}))
	if stale.OK || !stale.Conflict || stale.CurrentRev != 2 {
		t.Fatalf("save against the pre-delete rev = %+v, want conflict current_rev 2", stale)
	}

	// A save against the POST-delete rev (2) succeeds normally.
	revived := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "new\n", BaseRev: 2}))
	if !revived.OK || revived.Rev != 3 {
		t.Fatalf("save against rev 2 = %+v, want ok rev 3", revived)
	}
}

// A delete refused for a stale base must leave the file untouched.
func TestHandleMessage_NoteSet_DeleteWithStaleBaseIsConflictAndFileStays(t *testing.T) {
	_, client, _, paneID := notesTestDaemon(t)
	roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "one\n", BaseRev: 0})
	resp := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "", BaseRev: 0}))
	if resp.OK || !resp.Conflict || resp.CurrentRev != 1 {
		t.Fatalf("stale delete = %+v, want conflict current_rev 1", resp)
	}
	if onDisk, err := persist.LoadNotes(config.NotesDir(), paneID); err != nil || onDisk != "one\n" {
		t.Errorf("file changed or missing after a refused delete: %q, %v", onDisk, err)
	}
}

func TestHandleMessage_NoteGet_UnknownPaneIsBadPayloadToHelloedConn(t *testing.T) {
	_, client, _, _ := notesTestDaemon(t)
	helloAsScript(t, client)
	resp := roundTrip(t, client, ipc.MsgNoteGet, ipc.MsgError, ipc.NoteGetPayload{PaneID: "pane-00000000"})
	if e := decodeInto[ipc.ErrorPayload](t, resp); e.Code != ipc.ErrCodeBadPayload {
		t.Errorf("error = %+v", e)
	}
	for _, typ := range []string{ipc.MsgNoteGet, ipc.MsgNoteSet} {
		msg := &ipc.Message{Type: typ, ID: "bad-" + typ, Payload: []byte(`"not an object"`)}
		if err := client.Send(msg); err != nil {
			t.Fatal(err)
		}
		waitFrameWithID(t, client, msg.ID, 5*time.Second)
	}
}

// F-3: the saving client must learn its new rev BEFORE the frame that carries
// it, or it reads its own save back as another client's change.
func TestHandleMessage_NoteSet_ResponsePrecedesTheBroadcast(t *testing.T) {
	_, client, _, paneID := notesTestDaemon(t)
	msg, err := ipc.NewMessage(ipc.MsgNoteSet, ipc.NoteSetPayload{PaneID: paneID, Text: "x\n", BaseRev: 0})
	if err != nil {
		t.Fatal(err)
	}
	msg.ID = "order-1"
	if err := client.Send(msg); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		m, err := client.Receive()
		if err != nil {
			t.Fatalf("no answer: %v", err)
		}
		if m.ID == "order-1" {
			return // the answer came first
		}
		if m.Type == ipc.MsgWorkspaceState {
			ws := decodeInto[ipc.WorkspaceState](t, m)
			for _, p := range ws.Panes {
				if p.ID == paneID && p.NoteRev == 1 {
					t.Fatal("the state frame carrying rev 1 arrived before note_set_resp")
				}
			}
		}
	}
}

// Restore adopts a note file that predates note_rev (the local TUI's own
// notes for this daemon's panes) as rev 1; a pane with a recorded rev keeps it.
func TestRestoreWorkspace_AdoptsAnExistingNoteFileAsRevOne(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.snapshot() // written with no note_rev (rev is 0)
	if err := persist.SaveNotes(config.NotesDir(), pane.ID, "old local note\n"); err != nil {
		t.Fatal(err)
	}
	fresh := newTestDaemonInDir(t, os.Getenv("QUIL_HOME"))
	if err := fresh.restoreWorkspace(); err != nil {
		t.Fatal(err)
	}
	if noteRevOf(fresh, pane.ID) != 1 {
		t.Errorf("rev = %d, want 1 (adopted)", noteRevOf(fresh, pane.ID))
	}
}

// The adoption above must not fire when there is nothing to adopt: no
// note_rev in the snapshot AND no note file on disk stays at rev 0. Pins the
// noteFileExists gate — removing it would make every restored pane look like
// it has a note.
func TestRestoreWorkspace_NoNoteRevNoFileStaysAtRevZero(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.snapshot() // no note_rev, no note file
	fresh := newTestDaemonInDir(t, os.Getenv("QUIL_HOME"))
	if err := fresh.restoreWorkspace(); err != nil {
		t.Fatal(err)
	}
	if noteRevOf(fresh, pane.ID) != 0 {
		t.Errorf("rev = %d, want 0 (nothing to adopt)", noteRevOf(fresh, pane.ID))
	}
}
