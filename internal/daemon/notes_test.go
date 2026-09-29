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

func noteRevOf(d *Daemon, paneID string) uint64 {
	p := d.session.Pane(paneID)
	p.noteMu.Lock()
	defer p.noteMu.Unlock()
	return p.NoteRev
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
	results := make([]ipc.NoteSetRespPayload, 2)
	for i, c := range []*ipc.Client{a, b} {
		wg.Add(1)
		go func(i int, c *ipc.Client) {
			defer wg.Done()
			results[i] = decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, c, ipc.MsgNoteSet, ipc.MsgNoteSetResp,
				ipc.NoteSetPayload{PaneID: paneID, Text: "from " + string(rune('a'+i)) + "\n", BaseRev: 0}))
		}(i, c)
	}
	wg.Wait()
	oks := 0
	for _, r := range results {
		if r.OK {
			oks++
		} else if !r.Conflict || r.CurrentRev != 1 {
			t.Errorf("loser = %+v, want conflict current_rev 1", r)
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

func TestHandleMessage_NoteSet_EmptyTextDeletesAndZeroesRev(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "one\n", BaseRev: 0})
	resp := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "", BaseRev: 1}))
	if !resp.OK || resp.Rev != 0 {
		t.Fatalf("delete = %+v, want ok rev 0", resp)
	}
	path, _ := persist.NotesPath(config.NotesDir(), paneID)
	if _, err := os.Lstat(path); err == nil {
		t.Error("note file still exists after an empty save")
	}
	if noteRevOf(d, paneID) != 0 {
		t.Errorf("rev = %d, want 0", noteRevOf(d, paneID))
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
