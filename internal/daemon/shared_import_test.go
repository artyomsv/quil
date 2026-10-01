package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

func TestHandleMessage_SharedImport_AppliesEveryKindOnceOnAnEmptyDaemon(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	// Built from the code point (U+202E, RIGHT-TO-LEFT OVERRIDE) rather than an
	// escape literal in source: this file must never carry the raw rune.
	bidiOverride := "a" + string(rune(0x202e)) + "b"
	req := ipc.SharedImportPayload{
		Kinds:  []string{ipc.ImportKindGroups, ipc.ImportKindRecent, ipc.ImportKindNotes},
		Groups: []ipc.SharedImportGroup{{Name: "Infra", ProjectIDs: []string{p.ID, "proj-unknown"}}, {Name: "Empty"}, {Name: bidiOverride}},
		Recent: []string{"/one", "/two", "", "/one"},
		Notes:  []ipc.SharedImportNote{{PaneID: paneID, Text: "old\n"}, {PaneID: "pane-00000000", Text: "x"}},
	}
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, req))
	if len(resp.Answered) != 3 || !resp.GroupsApplied || !resp.RecentApplied || resp.NotesApplied != 1 || resp.NotesSkipped != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	groups, recent := d.session.SharedSnapshot()
	if len(groups) != 2 || groups[0] != "Infra" || groups[1] != "Empty" {
		t.Errorf("groups = %v (the invalid name must be skipped)", groups)
	}
	if len(recent) != 2 || recent[0] != "/one" {
		t.Errorf("recent = %v", recent)
	}
	for _, ps := range d.buildWorkspaceState().Projects {
		if ps.ID == p.ID && ps.Group != "Infra" {
			t.Errorf("project group = %q", ps.Group)
		}
	}
	if noteRevOf(d, paneID) != 1 {
		t.Errorf("note rev = %d, want 1", noteRevOf(d, paneID))
	}
	if text, _ := persist.LoadNotes(config.NotesDir(), paneID); text != "old\n" {
		t.Errorf("note text = %q", text)
	}
	// Second import: every kind is skipped, and still answered.
	again := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, req))
	if len(again.Answered) != 3 || again.GroupsApplied || again.RecentApplied || again.NotesApplied != 0 {
		t.Errorf("second import = %+v, want all answered, nothing applied", again)
	}
}

func TestHandleMessage_SharedImport_SkipsAKindTheDaemonAlreadyHolds(t *testing.T) {
	d, client, _, _ := notesTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	if err := d.session.SetProjectGroup(p.ID, "Mine"); err != nil {
		t.Fatal(err)
	}
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, ipc.SharedImportPayload{
		Kinds:  []string{ipc.ImportKindGroups},
		Groups: []ipc.SharedImportGroup{{Name: "Theirs", ProjectIDs: []string{p.ID}}},
	}))
	if resp.GroupsApplied || len(resp.Answered) != 1 {
		t.Errorf("resp = %+v", resp)
	}
	if groups, _ := d.session.SharedSnapshot(); len(groups) != 1 || groups[0] != "Mine" {
		t.Errorf("groups = %v", groups)
	}
}

// An import prepared from a frame at note_rev 0 can arrive after another
// client saved and then deleted the note. The delete leaves no file and a
// nonzero rev, and the old text must not come back.
func TestHandleMessage_SharedImport_DoesNotRestoreANoteDeletedMeanwhile(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "saved\n", BaseRev: 0})
	del := decodeInto[ipc.NoteSetRespPayload](t, roundTrip(t, client, ipc.MsgNoteSet, ipc.MsgNoteSetResp, ipc.NoteSetPayload{PaneID: paneID, Text: "", BaseRev: 1}))
	if !del.OK || del.Rev != 2 {
		t.Fatalf("delete = %+v, want ok rev 2", del)
	}
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, ipc.SharedImportPayload{
		Kinds: []string{ipc.ImportKindNotes},
		Notes: []ipc.SharedImportNote{{PaneID: paneID, Text: "old client text\n"}},
	}))
	if resp.NotesApplied != 0 || resp.NotesSkipped != 1 {
		t.Errorf("resp = %+v, want the note skipped", resp)
	}
	path, _ := persist.NotesPath(config.NotesDir(), paneID)
	if _, err := os.Lstat(path); err == nil {
		t.Error("the deleted note was written back")
	}
	if noteRevOf(d, paneID) != 2 {
		t.Errorf("rev = %d, want 2", noteRevOf(d, paneID))
	}
}

func TestHandleMessage_SharedImport_KindNotListedIsNeitherAppliedNorAnswered(t *testing.T) {
	d, client, _, _ := notesTestDaemon(t)
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, ipc.SharedImportPayload{
		Kinds:  []string{ipc.ImportKindRecent},
		Groups: []ipc.SharedImportGroup{{Name: "Stray"}},
		Recent: []string{"/r"},
	}))
	if len(resp.Answered) != 1 || resp.Answered[0] != ipc.ImportKindRecent || resp.GroupsApplied {
		t.Errorf("resp = %+v", resp)
	}
	if groups, _ := d.session.SharedSnapshot(); len(groups) != 0 {
		t.Errorf("an unlisted kind was applied: %v", groups)
	}
}

// sharedImportNoT sends a shared_import and returns its response or an
// error, calling no *testing.T method — t.Fatal/t.Errorf are unsafe from any
// goroutine but the test's own, so the concurrent test below cannot use
// roundTrip/decodeInto (both call t.Fatalf) from its worker goroutines.
// Mirrors noteSetNoT (notes_test.go).
func sharedImportNoT(client *ipc.Client, tag string, payload ipc.SharedImportPayload) (ipc.SharedImportRespPayload, error) {
	msg, err := ipc.NewMessage(ipc.MsgSharedImport, payload)
	if err != nil {
		return ipc.SharedImportRespPayload{}, err
	}
	msg.ID = "conc-" + tag + "-" + time.Now().Format("150405.000000000")
	if err := client.Send(msg); err != nil {
		return ipc.SharedImportRespPayload{}, err
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return ipc.SharedImportRespPayload{}, err
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		resp, err := client.Receive()
		if err != nil {
			return ipc.SharedImportRespPayload{}, err
		}
		if resp.Type == ipc.MsgSharedImportResp && resp.ID == msg.ID {
			var out ipc.SharedImportRespPayload
			if err := resp.DecodePayload(&out); err != nil {
				return ipc.SharedImportRespPayload{}, err
			}
			return out, nil
		}
	}
}

// Two clients importing at once — exactly one applies.
func TestHandleMessage_SharedImport_ConcurrentImports_ExactlyOneApplies(t *testing.T) {
	d, a, sock, _ := notesTestDaemon(t)
	b, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	sendNoID(t, b, ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "bridge", PID: os.Getpid()})
	var wg sync.WaitGroup
	type result struct {
		resp ipc.SharedImportRespPayload
		err  error
	}
	results := make([]result, 2)
	for i, c := range []*ipc.Client{a, b} {
		wg.Add(1)
		go func(i int, c *ipc.Client) {
			defer wg.Done()
			resp, err := sharedImportNoT(c, string(rune('a'+i)), ipc.SharedImportPayload{
				Kinds:  []string{ipc.ImportKindGroups, ipc.ImportKindRecent},
				Groups: []ipc.SharedImportGroup{{Name: "G" + string(rune('a'+i))}},
				Recent: []string{"/r" + string(rune('a'+i))},
			})
			results[i] = result{resp, err}
		}(i, c)
	}
	wg.Wait()
	applied := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("goroutine %d: %v", i, r.err)
		}
		if r.resp.GroupsApplied {
			applied++
		}
		if r.resp.GroupsApplied != r.resp.RecentApplied {
			t.Errorf("one client's kinds split: %+v", r.resp)
		}
	}
	if applied != 1 {
		t.Fatalf("%d imports applied, want 1: %+v", applied, results)
	}
	if groups, _ := d.session.SharedSnapshot(); len(groups) != 1 {
		t.Errorf("groups = %v", groups)
	}
}

func TestHandleMessage_SharedImport_OversizeAndBadPayloadAnswered(t *testing.T) {
	_, client, _, _ := notesTestDaemon(t)
	helloAsScript(t, client)
	big, _ := json.Marshal(ipc.SharedImportPayload{Kinds: []string{ipc.ImportKindNotes},
		Notes: []ipc.SharedImportNote{{PaneID: "pane-00000000", Text: strings.Repeat("x", ipc.MaxSharedImportBytes+1)}}})
	msg := &ipc.Message{Type: ipc.MsgSharedImport, ID: "big-1", Payload: big}
	if err := client.Send(msg); err != nil {
		t.Fatal(err)
	}
	if got := waitFrameWithID(t, client, "big-1", 10*time.Second); got.Type != ipc.MsgError {
		t.Errorf("oversize import answered with %s, want error", got.Type)
	}
	bad := &ipc.Message{Type: ipc.MsgSharedImport, ID: "bad-1", Payload: []byte(`"not an object"`)}
	if err := client.Send(bad); err != nil {
		t.Fatal(err)
	}
	waitFrameWithID(t, client, "bad-1", 5*time.Second)
}
